// Package store reads the no-mistakes daemon's local state database.
//
// The database is owned by no-mistakes; this program only ever reads it. Every
// connection is opened with PRAGMA query_only so a bug here can never write to
// a live pipeline's state, and with a busy_timeout so a concurrent daemon write
// makes us wait rather than fail. The daemon runs the database in WAL mode, so
// readers never block the writer either.
//
// The schema belongs to another project and can drift. Probe() checks the exact
// columns we depend on up front so a version mismatch reports what is missing
// instead of surfacing as an opaque scan error mid-refresh.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// DefaultPath returns the state database path, honoring NM_HOME the same way
// no-mistakes' own internal/paths package does.
func DefaultPath() (string, error) {
	if env := os.Getenv("NM_HOME"); env != "" {
		return filepath.Join(env, "state.sqlite"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".no-mistakes", "state.sqlite"), nil
}

// Store is a read-only handle on the state database.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens the state database read-only. It fails with a friendly error when
// no-mistakes has never run on this machine.
func Open(path string) (*Store, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("no no-mistakes state database at %s (has no-mistakes ever run here?)", path)
		}
		return nil, err
	}
	dsn := "file:" + path + "?_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// One connection is plenty for a poll every few seconds, and it keeps the
	// per-connection query_only pragma trivially true for every query we run.
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Path returns the database path this store reads.
func (s *Store) Path() string { return s.path }

// requiredColumns is every column this program selects, per table. Keep it in
// sync with the queries below; Probe is only useful if it is exhaustive.
var requiredColumns = map[string][]string{
	"repos": {"id", "working_path"},
	"runs": {
		"id", "repo_id", "branch", "head_sha", "status", "error", "pr_url", "pr_state",
		"intent", "intent_source", "awaiting_agent_since", "parked_ms", "created_at", "updated_at",
	},
	"step_results": {
		"run_id", "step_name", "step_order", "status", "duration_ms", "log_path",
		"findings_json", "error", "started_at", "completed_at", "last_activity",
		"last_activity_at", "agent_pid",
	},
}

// Probe verifies the schema exposes every column the queries depend on.
func (s *Store) Probe(ctx context.Context) error {
	var missing []string
	for table, columns := range requiredColumns {
		present, err := s.columns(ctx, table)
		if err != nil {
			return err
		}
		if len(present) == 0 {
			missing = append(missing, table+" (whole table)")
			continue
		}
		for _, col := range columns {
			if !present[col] {
				missing = append(missing, table+"."+col)
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("unsupported no-mistakes schema in %s: missing %s", s.path, strings.Join(missing, ", "))
	}
	return nil
}

func (s *Store) columns(ctx context.Context, table string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// Snapshot is one poll of the whole machine: every in-flight run plus recently
// finished ones.
type Snapshot struct {
	Active  []Run
	Recent  []Run
	TakenAt time.Time
	// Baselines is the typical duration of each step, learned from this
	// machine's own finished runs. It is what makes "how much longer" an
	// answer rather than a guess.
	Baselines Baselines
}

// Baselines holds a typical step duration per repository and step name.
type Baselines map[string]time.Duration

// baselineKey scopes a duration to one repo's copy of one step. A test step
// means something different in every project, so they must never be averaged
// together.
func baselineKey(repoPath, stepName string) string { return repoPath + "\x00" + stepName }

// Lookup returns the typical duration of a step in a repo.
func (b Baselines) Lookup(repoPath, stepName string) (time.Duration, bool) {
	d, ok := b[baselineKey(repoPath, stepName)]
	return d, ok
}

// Run is one pipeline run with its steps.
type Run struct {
	ID           string
	RepoPath     string
	Branch       string
	HeadSHA      string
	Status       string
	Error        string
	PRURL        string
	PRState      string
	Intent       string
	IntentSource string
	ParkedSince  time.Time
	ParkedMS     int64
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Steps        []Step
}

// Step is one pipeline stage of a run.
type Step struct {
	Name           string
	Order          int
	Status         string
	Duration       time.Duration
	LogPath        string
	Error          string
	Findings       []Finding
	FindingCount   int
	NeedsUser      int
	Summary        string
	StartedAt      time.Time
	CompletedAt    time.Time
	LastActivity   string
	LastActivityAt time.Time
	AgentPID       int
}

// Finding is one thing a step flagged: a review comment, a failing test, a lint
// complaint. Action is what the pipeline decided to do about it.
type Finding struct {
	Severity    string `json:"severity"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Description string `json:"description"`
	Action      string `json:"action"`
	Category    string `json:"category"`
}

// NeedsUser reports whether a finding is parked for a human decision. An empty
// or unrecognized action counts as needing a human, matching no-mistakes' own
// fail-closed rule that an unclassified finding is never auto-fixed.
func (f Finding) NeedsUser() bool {
	switch strings.TrimSpace(f.Action) {
	case "auto-fix", "no-op":
		return false
	default:
		return true
	}
}

// Location renders "file:line", or an empty string when the finding is not
// pinned to one place.
func (f Finding) Location() string {
	if f.File == "" {
		return ""
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

// RepoName is the last path element of the repo's working directory, which is
// what a human calls the project.
func (r Run) RepoName() string {
	if r.RepoPath == "" {
		return "(unknown)"
	}
	return filepath.Base(r.RepoPath)
}

// Parked reports whether the run is sitting at a gate waiting on a human or an
// agent decision.
func (r Run) Parked() bool { return !r.ParkedSince.IsZero() }

// Terminal reports whether the run has finished, one way or another.
func (r Run) Terminal() bool {
	switch r.Status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// Current returns the step the run is standing on: the first non-terminal step,
// or the last step once the run is over.
func (r Run) Current() (Step, bool) {
	if len(r.Steps) == 0 {
		return Step{}, false
	}
	for _, s := range r.Steps {
		switch s.Status {
		case "running", "fixing", "awaiting_approval", "fix_review", "failed":
			return s, true
		}
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].Status != "pending" {
			return r.Steps[i], true
		}
	}
	return r.Steps[0], true
}

// Findings totals the findings recorded across every step of the run.
func (r Run) Findings() (total, needsUser int) {
	for _, s := range r.Steps {
		total += s.FindingCount
		needsUser += s.NeedsUser
	}
	return total, needsUser
}

// LatestLog returns the log file worth opening: the step the run is standing on
// when it has written one, otherwise the last step that did. A step that has
// just started has no log yet, and an empty pager is useless.
func (r Run) LatestLog() (step Step, path string, ok bool) {
	if current, found := r.Current(); found && current.LogPath != "" {
		return current, current.LogPath, true
	}
	for i := len(r.Steps) - 1; i >= 0; i-- {
		if r.Steps[i].LogPath != "" {
			return r.Steps[i], r.Steps[i].LogPath, true
		}
	}
	return Step{}, "", false
}

const runSelect = `
SELECT r.id, r.repo_id, COALESCE(p.working_path, ''), r.branch, r.head_sha, r.status,
       COALESCE(r.error, ''), COALESCE(r.pr_url, ''), COALESCE(r.pr_state, ''),
       COALESCE(r.intent, ''), COALESCE(r.intent_source, ''),
       r.awaiting_agent_since, COALESCE(r.parked_ms, 0), r.created_at, r.updated_at
FROM runs r
LEFT JOIN repos p ON p.id = r.repo_id
WHERE r.status IN ('pending', 'running') OR r.updated_at >= ?
ORDER BY r.created_at DESC`

// Read returns every active run plus runs that finished within recentWindow,
// capped at recentLimit finished runs (0 means no cap).
func (s *Store) Read(ctx context.Context, now time.Time, recentWindow time.Duration, recentLimit int) (Snapshot, error) {
	cutoff := now.Add(-recentWindow).Unix()
	rows, err := s.db.QueryContext(ctx, runSelect, cutoff)
	if err != nil {
		return Snapshot{}, fmt.Errorf("query runs: %w", err)
	}
	defer rows.Close()

	snap := Snapshot{TakenAt: now}
	byID := map[string]*Run{}
	var order []*Run
	for rows.Next() {
		var (
			run      Run
			repoID   string
			parked   sql.NullInt64
			created  int64
			updated  int64
			parkedMS sql.NullInt64
		)
		if err := rows.Scan(
			&run.ID, &repoID, &run.RepoPath, &run.Branch, &run.HeadSHA, &run.Status,
			&run.Error, &run.PRURL, &run.PRState, &run.Intent, &run.IntentSource,
			&parked, &parkedMS, &created, &updated,
		); err != nil {
			return Snapshot{}, fmt.Errorf("scan run: %w", err)
		}
		if parked.Valid && parked.Int64 > 0 {
			run.ParkedSince = time.Unix(parked.Int64, 0)
		}
		run.ParkedMS = parkedMS.Int64
		run.CreatedAt = time.Unix(created, 0)
		run.UpdatedAt = time.Unix(updated, 0)
		order = append(order, &run)
		byID[run.ID] = order[len(order)-1]
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}

	if err := s.loadSteps(ctx, byID); err != nil {
		return Snapshot{}, err
	}

	baselines, err := s.baselines(ctx, now)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Baselines = baselines

	for _, run := range order {
		if run.Terminal() {
			if recentLimit > 0 && len(snap.Recent) >= recentLimit {
				continue
			}
			snap.Recent = append(snap.Recent, *run)
			continue
		}
		snap.Active = append(snap.Active, *run)
	}
	return snap, nil
}

const stepSelect = `
SELECT run_id, step_name, step_order, status, COALESCE(duration_ms, 0), COALESCE(log_path, ''),
       COALESCE(findings_json, ''), COALESCE(error, ''), started_at, completed_at,
       COALESCE(last_activity, ''), last_activity_at, agent_pid
FROM step_results
WHERE run_id IN (%s)
ORDER BY step_order`

func (s *Store) loadSteps(ctx context.Context, runs map[string]*Run) error {
	if len(runs) == 0 {
		return nil
	}
	ids := make([]any, 0, len(runs))
	for id := range runs {
		ids = append(ids, id)
	}
	query := fmt.Sprintf(stepSelect, placeholders(len(ids)))
	rows, err := s.db.QueryContext(ctx, query, ids...)
	if err != nil {
		return fmt.Errorf("query steps: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			runID        string
			step         Step
			durationMS   int64
			findingsJSON string
			started      sql.NullInt64
			completed    sql.NullInt64
			activityAt   sql.NullInt64
			agentPID     sql.NullInt64
		)
		if err := rows.Scan(
			&runID, &step.Name, &step.Order, &step.Status, &durationMS, &step.LogPath,
			&findingsJSON, &step.Error, &started, &completed,
			&step.LastActivity, &activityAt, &agentPID,
		); err != nil {
			return fmt.Errorf("scan step: %w", err)
		}
		step.Duration = time.Duration(durationMS) * time.Millisecond
		if started.Valid {
			step.StartedAt = time.Unix(started.Int64, 0)
		}
		if completed.Valid {
			step.CompletedAt = time.Unix(completed.Int64, 0)
		}
		if activityAt.Valid {
			step.LastActivityAt = time.Unix(activityAt.Int64, 0)
		}
		step.AgentPID = int(agentPID.Int64)
		step.Findings, step.NeedsUser, step.Summary = parseFindings(findingsJSON)
		step.FindingCount = len(step.Findings)
		if run, ok := runs[runID]; ok {
			run.Steps = append(run.Steps, step)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, run := range runs {
		sort.Slice(run.Steps, func(i, j int) bool { return run.Steps[i].Order < run.Steps[j].Order })
	}
	return nil
}

// baselineWindow is how far back to learn typical step durations. Long enough
// to have samples for slow steps, short enough that a project's habits from
// last quarter do not distort today's estimate.
const baselineWindow = 30 * 24 * time.Hour

const baselineSelect = `
SELECT p.working_path, s.step_name, s.duration_ms
FROM step_results s
JOIN runs r ON r.id = s.run_id
JOIN repos p ON p.id = r.repo_id
WHERE s.status = 'completed' AND s.duration_ms > 0 AND r.created_at >= ?`

// baselines learns the median duration of every step in every repo. The median,
// not the mean: one CI step that sat for six hours waiting on a merge should not
// move the estimate for the next twenty runs.
func (s *Store) baselines(ctx context.Context, now time.Time) (Baselines, error) {
	rows, err := s.db.QueryContext(ctx, baselineSelect, now.Add(-baselineWindow).Unix())
	if err != nil {
		return nil, fmt.Errorf("query baselines: %w", err)
	}
	defer rows.Close()

	samples := map[string][]int64{}
	for rows.Next() {
		var (
			repoPath string
			stepName string
			ms       int64
		)
		if err := rows.Scan(&repoPath, &stepName, &ms); err != nil {
			return nil, fmt.Errorf("scan baseline: %w", err)
		}
		key := baselineKey(repoPath, stepName)
		samples[key] = append(samples[key], ms)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make(Baselines, len(samples))
	for key, values := range samples {
		sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
		out[key] = time.Duration(values[len(values)/2]) * time.Millisecond
	}
	return out, nil
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

type findingsPayload struct {
	Findings []Finding `json:"findings"`
	Summary  string    `json:"summary"`
}

// parseFindings reads a step's recorded findings. Unparseable JSON yields no
// findings rather than an error: a step's payload is written by an agent, and a
// malformed one must not take down the whole refresh.
func parseFindings(raw string) (findings []Finding, needsUser int, summary string) {
	if strings.TrimSpace(raw) == "" {
		return nil, 0, ""
	}
	var payload findingsPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return nil, 0, ""
	}
	for _, f := range payload.Findings {
		if f.NeedsUser() {
			needsUser++
		}
	}
	return payload.Findings, needsUser, payload.Summary
}

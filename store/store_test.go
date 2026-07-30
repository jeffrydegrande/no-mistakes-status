package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// schema mirrors the subset of no-mistakes' state database this program reads.
const schema = `
CREATE TABLE repos (
    id             TEXT PRIMARY KEY,
    working_path   TEXT NOT NULL UNIQUE,
    upstream_url   TEXT NOT NULL,
    fork_url       TEXT,
    default_branch TEXT NOT NULL DEFAULT 'main',
    created_at     INTEGER NOT NULL
);
CREATE TABLE runs (
    id                   TEXT PRIMARY KEY,
    repo_id              TEXT NOT NULL,
    branch               TEXT NOT NULL,
    head_sha             TEXT NOT NULL,
    base_sha             TEXT NOT NULL,
    status               TEXT NOT NULL DEFAULT 'pending',
    pr_url               TEXT,
    error                TEXT,
    awaiting_agent_since INTEGER,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL,
    intent               TEXT,
    intent_source        TEXT,
    parked_ms            INTEGER,
    pr_state             TEXT
);
CREATE TABLE step_results (
    id               TEXT PRIMARY KEY,
    run_id           TEXT NOT NULL,
    step_name        TEXT NOT NULL,
    step_order       INTEGER NOT NULL,
    status           TEXT NOT NULL DEFAULT 'pending',
    duration_ms      INTEGER,
    log_path         TEXT,
    findings_json    TEXT,
    error            TEXT,
    started_at       INTEGER,
    completed_at     INTEGER,
    last_activity    TEXT,
    last_activity_at INTEGER,
    agent_pid        INTEGER
);`

func newTestDB(t *testing.T, extra ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := append([]string{schema}, extra...)
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	return path
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestOpenMissingDatabaseExplainsItself(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope.sqlite"))
	if err == nil {
		t.Fatal("expected an error for a missing database")
	}
	if !strings.Contains(err.Error(), "has no-mistakes ever run here") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestProbeAcceptsCurrentSchema(t *testing.T) {
	st := openTestStore(t, newTestDB(t))
	if err := st.Probe(context.Background()); err != nil {
		t.Fatalf("probe rejected a valid schema: %v", err)
	}
}

func TestProbeNamesMissingColumns(t *testing.T) {
	// A schema without the columns this program depends on must be reported
	// clearly, not discovered as a scan failure during a refresh.
	path := filepath.Join(t.TempDir(), "state.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE repos (id TEXT PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st := openTestStore(t, path)
	err = st.Probe(context.Background())
	if err == nil {
		t.Fatal("expected probe to reject an incompatible schema")
	}
	for _, want := range []string{"repos.working_path", "runs (whole table)", "step_results (whole table)"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("probe error does not name %q: %v", want, err)
		}
	}
}

func TestReadSplitsActiveFromRecentAndOrdersSteps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	path := newTestDB(t, `
INSERT INTO repos VALUES ('r1', '/home/x/Code/nova-go', 'git@github.com:x/nova-go', NULL, 'main', 1);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, pr_url, pr_state, created_at, updated_at, awaiting_agent_since)
VALUES ('run-active', 'r1', 'feature/a', 'abcdef1234567890', 'base', 'running', 'https://github.com/x/nova-go/pull/192', 'open', 1699999000, 1699999900, 1699999800);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at)
VALUES ('run-recent', 'r1', 'feature/b', 'beef', 'base', 'completed', 1699998000, 1699999000);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at)
VALUES ('run-old', 'r1', 'feature/c', 'cafe', 'base', 'completed', 1600000000, 1600000000);
INSERT INTO step_results (id, run_id, step_name, step_order, status)
VALUES ('s2', 'run-active', 'rebase', 2, 'completed');
INSERT INTO step_results (id, run_id, step_name, step_order, status, agent_pid)
VALUES ('s3', 'run-active', 'review', 3, 'running', 4242);
INSERT INTO step_results (id, run_id, step_name, step_order, status)
VALUES ('s1', 'run-active', 'intent', 1, 'completed');
`)
	st := openTestStore(t, path)

	snap, err := st.Read(context.Background(), now, 6*time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Active) != 1 || snap.Active[0].ID != "run-active" {
		t.Fatalf("active runs = %+v", snap.Active)
	}
	if len(snap.Recent) != 1 || snap.Recent[0].ID != "run-recent" {
		t.Fatalf("recent runs = %+v, want only run-recent (run-old is outside the window)", snap.Recent)
	}

	run := snap.Active[0]
	if got := run.RepoName(); got != "nova-go" {
		t.Errorf("RepoName = %q, want nova-go", got)
	}
	if !run.Parked() {
		t.Error("run with awaiting_agent_since should report as parked")
	}
	if run.PRState != "open" || run.PRURL == "" {
		t.Errorf("PR fields not loaded: %q %q", run.PRState, run.PRURL)
	}

	// Steps must come back in pipeline order even though they were inserted
	// out of order, because the stage strip reads them positionally.
	var names []string
	for _, s := range run.Steps {
		names = append(names, s.Name)
	}
	if len(names) != 3 || names[0] != "intent" || names[1] != "rebase" || names[2] != "review" {
		t.Fatalf("steps = %v, want intent, rebase, review", names)
	}
	if run.Steps[2].AgentPID != 4242 {
		t.Errorf("agent pid = %d, want 4242", run.Steps[2].AgentPID)
	}

	current, ok := run.Current()
	if !ok || current.Name != "review" {
		t.Errorf("Current = %+v, want the running review step", current)
	}
}

func TestReadCapsRecentRuns(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	path := newTestDB(t, `
INSERT INTO repos VALUES ('r1', '/tmp/repo', 'url', NULL, 'main', 1);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('a', 'r1', 'b1', 'h', 'b', 'completed', 1699999000, 1699999000);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('b', 'r1', 'b2', 'h', 'b', 'failed',    1699999100, 1699999100);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('c', 'r1', 'b3', 'h', 'b', 'cancelled', 1699999200, 1699999200);
`)
	st := openTestStore(t, path)

	snap, err := st.Read(context.Background(), now, 6*time.Hour, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Recent) != 2 {
		t.Fatalf("recent = %d, want the 2 newest", len(snap.Recent))
	}
	if snap.Recent[0].ID != "c" || snap.Recent[1].ID != "b" {
		t.Errorf("recent order = %s, %s; want newest first", snap.Recent[0].ID, snap.Recent[1].ID)
	}
}

func TestReadIsNotBlockedByAConcurrentWriter(t *testing.T) {
	// The daemon writes this database while we poll it. Reading must not fail
	// just because another connection has it open.
	path := newTestDB(t, `INSERT INTO repos VALUES ('r1', '/tmp/repo', 'url', NULL, 'main', 1);`)
	writer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at)
		VALUES ('live', 'r1', 'b', 'h', 'b', 'running', 1, 2)`); err != nil {
		t.Fatal(err)
	}

	st := openTestStore(t, path)
	snap, err := st.Read(context.Background(), time.Unix(1_700_000_000, 0), time.Hour, 0)
	if err != nil {
		t.Fatalf("read with a live writer: %v", err)
	}
	if len(snap.Active) != 1 {
		t.Fatalf("active = %d, want 1", len(snap.Active))
	}
}

func TestStoreNeverWrites(t *testing.T) {
	// query_only is the safety net that makes it impossible for a bug here to
	// corrupt a running pipeline's state.
	path := newTestDB(t)
	st := openTestStore(t, path)
	_, err := st.db.Exec(`INSERT INTO repos VALUES ('r9', '/tmp/x', 'url', NULL, 'main', 1)`)
	if err == nil {
		t.Fatal("expected the connection to refuse a write")
	}
}

func TestParseFindings(t *testing.T) {
	tests := []struct {
		name          string
		raw           string
		total         int
		needsUser     int
		wantSummaryIs string
	}{
		{name: "empty", raw: "", total: 0, needsUser: 0},
		{name: "garbage is ignored", raw: "not json", total: 0, needsUser: 0},
		{
			name:          "no findings keeps the summary",
			raw:           `{"findings":[],"summary":"all good"}`,
			wantSummaryIs: "all good",
		},
		{
			name:      "auto-fix does not need a human",
			raw:       `{"findings":[{"action":"auto-fix"},{"action":"no-op"}]}`,
			total:     2,
			needsUser: 0,
		},
		{
			name:      "ask-user needs a human",
			raw:       `{"findings":[{"action":"ask-user"},{"action":"auto-fix"}]}`,
			total:     2,
			needsUser: 1,
		},
		{
			name:      "an unclassified finding fails closed to needing a human",
			raw:       `{"findings":[{"severity":"high"}]}`,
			total:     1,
			needsUser: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total, needsUser, summary := parseFindings(tc.raw)
			if total != tc.total || needsUser != tc.needsUser {
				t.Errorf("got total=%d needsUser=%d, want %d/%d", total, needsUser, tc.total, tc.needsUser)
			}
			if summary != tc.wantSummaryIs {
				t.Errorf("summary = %q, want %q", summary, tc.wantSummaryIs)
			}
		})
	}
}

func TestRunCurrentPrefersTheStepThatNeedsAttention(t *testing.T) {
	run := Run{Steps: []Step{
		{Name: "intent", Status: "completed"},
		{Name: "review", Status: "awaiting_approval"},
		{Name: "test", Status: "pending"},
	}}
	step, ok := run.Current()
	if !ok || step.Name != "review" {
		t.Fatalf("Current = %+v, want the parked review step", step)
	}

	done := Run{Status: "completed", Steps: []Step{
		{Name: "intent", Status: "completed"},
		{Name: "ci", Status: "completed"},
	}}
	step, ok = done.Current()
	if !ok || step.Name != "ci" {
		t.Fatalf("Current on a finished run = %+v, want the last step", step)
	}
}

func TestDefaultPathHonorsNMHome(t *testing.T) {
	t.Setenv("NM_HOME", "/custom/home")
	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/custom/home/state.sqlite" {
		t.Errorf("DefaultPath = %q", got)
	}
}

package ui

import (
	"context"
	"encoding/json"
	"io"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// JSONRun is one run in the -json output. Scripts depend on these field
// names: add fields, never rename or remove them.
type JSONRun struct {
	ID       string `json:"id"`
	Repo     string `json:"repo"`
	RepoPath string `json:"repo_path"`
	Branch   string `json:"branch"`
	HeadSHA  string `json:"head_sha"`
	// Status is pending, running, completed, failed or cancelled.
	Status string `json:"status"`
	// Active is true while the run is pending or running.
	Active bool `json:"active"`
	// Step is the step the run is standing on, StepStatus its status.
	Step       string `json:"step"`
	StepStatus string `json:"step_status"`
	StepsDone  int    `json:"steps_done"`
	StepsTotal int    `json:"steps_total"`
	// Parked is true while the run waits at a gate for a human or an agent.
	Parked      bool       `json:"parked"`
	ParkedSince *time.Time `json:"parked_since,omitempty"`
	// Stalled is "silent" when the running step has reported nothing for
	// longer than -stall, "gone" when its agent process is gone, else empty.
	Stalled   string    `json:"stalled,omitempty"`
	Findings  int       `json:"findings"`
	NeedsUser int       `json:"needs_user"`
	PRURL     string    `json:"pr_url,omitempty"`
	PRState   string    `json:"pr_state,omitempty"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// JSONSnapshot is the whole -json document.
type JSONSnapshot struct {
	TakenAt time.Time `json:"taken_at"`
	Runs    []JSONRun `json:"runs"`
}

// PrintJSON reads the database once and writes active runs, then recent
// finished runs, as one JSON document.
func PrintJSON(ctx context.Context, w io.Writer, reader Reader, opts Options) error {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	snap, err := reader.Read(ctx, now(), opts.RecentWindow, opts.RecentLimit)
	if err != nil {
		return err
	}
	stall := newStallCheck(opts.StallAfter, now())
	out := JSONSnapshot{TakenAt: snap.TakenAt, Runs: []JSONRun{}}
	for _, r := range append(append([]store.Run{}, snap.Active...), snap.Recent...) {
		out.Runs = append(out.Runs, jsonRun(r, stall))
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func jsonRun(r store.Run, stall stallCheck) JSONRun {
	j := JSONRun{
		ID: r.ID, Repo: r.RepoName(), RepoPath: r.RepoPath, Branch: r.Branch, HeadSHA: r.HeadSHA,
		Status: r.Status, Active: !r.Terminal(), Parked: r.Parked(), StepsTotal: len(r.Steps),
		PRURL: r.PRURL, PRState: r.PRState, Error: r.Error, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
	if r.Parked() {
		since := r.ParkedSince
		j.ParkedSince = &since
	}
	if step, ok := r.Current(); ok {
		j.Step, j.StepStatus = step.Name, step.Status
	}
	for _, s := range r.Steps {
		if s.Status == "completed" || s.Status == "skipped" {
			j.StepsDone++
		}
	}
	switch kind, _ := stall.classify(r); kind {
	case stallSilent:
		j.Stalled = "silent"
	case stallGone:
		j.Stalled = "gone"
	}
	j.Findings, j.NeedsUser = r.Findings()
	return j
}

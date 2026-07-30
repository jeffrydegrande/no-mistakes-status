package store

import (
	"context"
	"testing"
	"time"
)

func TestBaselinesUseTheMedianPerRepoAndStep(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	// One CI step that sat for hours waiting on a merge must not move the
	// estimate for every future run, which is why this is a median.
	path := newTestDB(t, `
INSERT INTO repos VALUES ('r1', '/repo/one', 'url', NULL, 'main', 1);
INSERT INTO repos VALUES ('r2', '/repo/two', 'url', NULL, 'main', 1);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('a', 'r1', 'b', 'h', 'b', 'completed', 1699990000, 1699990000);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('b', 'r2', 'b', 'h', 'b', 'completed', 1699990000, 1699990000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('1', 'a', 'test', 4, 'completed', 1000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('2', 'a', 'test', 4, 'completed', 3000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('3', 'a', 'test', 4, 'completed', 900000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('4', 'b', 'test', 4, 'completed', 50000);
`)
	st := openTestStore(t, path)

	snap, err := st.Read(context.Background(), now, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}

	got, ok := snap.Baselines.Lookup("/repo/one", "test")
	if !ok {
		t.Fatal("no baseline learned for /repo/one test")
	}
	if got != 3*time.Second {
		t.Errorf("baseline = %s, want the 3s median, not an average dragged up by the outlier", got)
	}

	// A test step means something different in every project, so baselines
	// must never be shared across repos.
	other, ok := snap.Baselines.Lookup("/repo/two", "test")
	if !ok || other != 50*time.Second {
		t.Errorf("other repo baseline = %s (%v), want 50s", other, ok)
	}

	if _, ok := snap.Baselines.Lookup("/repo/one", "lint"); ok {
		t.Error("a step with no history should have no baseline")
	}
}

func TestBaselinesIgnoreUnfinishedAndAncientSteps(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	path := newTestDB(t, `
INSERT INTO repos VALUES ('r1', '/repo/one', 'url', NULL, 'main', 1);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('recent', 'r1', 'b', 'h', 'b', 'completed', 1699990000, 1699990000);
INSERT INTO runs (id, repo_id, branch, head_sha, base_sha, status, created_at, updated_at) VALUES ('ancient', 'r1', 'b', 'h', 'b', 'completed', 1600000000, 1600000000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('1', 'recent', 'lint', 6, 'running', 5000);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('2', 'recent', 'lint', 6, 'completed', 0);
INSERT INTO step_results (id, run_id, step_name, step_order, status, duration_ms) VALUES ('3', 'ancient', 'lint', 6, 'completed', 90000);
`)
	st := openTestStore(t, path)

	snap, err := st.Read(context.Background(), now, time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	if d, ok := snap.Baselines.Lookup("/repo/one", "lint"); ok {
		t.Errorf("baseline = %s, want none: the only samples are unfinished, zero, or outside the window", d)
	}
}

func TestLatestLogPrefersTheCurrentStep(t *testing.T) {
	run := Run{Steps: []Step{
		{Name: "review", Order: 3, Status: "completed", LogPath: "/logs/review.log"},
		{Name: "test", Order: 4, Status: "running", LogPath: "/logs/test.log"},
	}}
	step, path, ok := run.LatestLog()
	if !ok || path != "/logs/test.log" || step.Name != "test" {
		t.Fatalf("LatestLog = %q (%s), want the running step's log", path, step.Name)
	}

	// A step that has just started has not written a log yet, so fall back to
	// the last one that did rather than showing an empty pane.
	fresh := Run{Steps: []Step{
		{Name: "review", Order: 3, Status: "completed", LogPath: "/logs/review.log"},
		{Name: "test", Order: 4, Status: "running"},
	}}
	_, path, ok = fresh.LatestLog()
	if !ok || path != "/logs/review.log" {
		t.Errorf("LatestLog = %q, want the previous step's log", path)
	}

	if _, _, ok := (Run{}).LatestLog(); ok {
		t.Error("a run with no steps has no log")
	}
}

func TestFindingNeedsUserFailsClosed(t *testing.T) {
	for _, action := range []string{"auto-fix", "no-op"} {
		if (Finding{Action: action}).NeedsUser() {
			t.Errorf("action %q should not need a human", action)
		}
	}
	for _, action := range []string{"", "ask-user", "something-new"} {
		if !(Finding{Action: action}).NeedsUser() {
			t.Errorf("action %q should fail closed to needing a human", action)
		}
	}
}

func TestFindingLocation(t *testing.T) {
	if got := (Finding{File: "a.go", Line: 42}).Location(); got != "a.go:42" {
		t.Errorf("Location = %q", got)
	}
	if got := (Finding{File: "a.go"}).Location(); got != "a.go" {
		t.Errorf("Location without a line = %q", got)
	}
	if got := (Finding{}).Location(); got != "" {
		t.Errorf("Location with no file = %q", got)
	}
}

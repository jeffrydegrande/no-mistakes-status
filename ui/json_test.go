package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

type jsonReader struct{ snap store.Snapshot }

func (r jsonReader) Read(context.Context, time.Time, time.Duration, int) (store.Snapshot, error) {
	return r.snap, nil
}

func TestPrintJSON(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	snap := store.Snapshot{
		TakenAt: now,
		Active: []store.Run{{
			ID: "r1", RepoPath: "/src/seq", Branch: "atc/seq-bug-7f3a", Status: "running", PRURL: "https://x/pull/7", PRState: "open",
			Steps: []store.Step{
				{Name: "review", Status: "completed"},
				{Name: "test", Status: "running", StartedAt: now.Add(-20 * time.Minute), LastActivityAt: now.Add(-10 * time.Minute)},
				{Name: "push", Status: "pending"},
			},
		}},
		Recent: []store.Run{{ID: "r0", RepoPath: "/src/web", Branch: "main", Status: "failed", Error: "lint", ParkedSince: now.Add(-time.Hour)}},
	}
	var buf bytes.Buffer
	err := PrintJSON(context.Background(), &buf, jsonReader{snap}, Options{Now: func() time.Time { return now }, StallAfter: 5 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	var got JSONSnapshot
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	if len(got.Runs) != 2 {
		t.Fatalf("runs: %+v", got.Runs)
	}
	a := got.Runs[0]
	if a.Repo != "seq" || !a.Active || a.Step != "test" || a.StepStatus != "running" || a.StepsDone != 1 || a.StepsTotal != 3 || a.Stalled != "silent" || a.Parked || a.PRState != "open" {
		t.Errorf("active run: %+v", a)
	}
	b := got.Runs[1]
	if b.Active || b.Status != "failed" || !b.Parked || b.ParkedSince == nil || b.Stalled != "" || b.Error != "lint" {
		t.Errorf("recent run: %+v", b)
	}
}

func TestPrintJSONEmptyIsAnArray(t *testing.T) {
	var buf bytes.Buffer
	if err := PrintJSON(context.Background(), &buf, jsonReader{}, Options{}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(buf.Bytes(), []byte(`"runs": []`)) {
		t.Fatalf("empty output: %s", buf.String())
	}
}

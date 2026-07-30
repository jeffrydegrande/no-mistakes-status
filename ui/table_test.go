package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// testRowContext builds the surrounding state a row needs, with the repo
// labels derived the same way the model derives them.
func testRowContext(cols columns) rowContext {
	return rowContext{
		cols:   cols,
		labels: map[string]string{},
		slots:  map[string]string{},
		now:    time.Unix(2000, 0),
	}
}

func testRun(id, repo, branch, status string) store.Run {
	return store.Run{
		ID:        id,
		RepoPath:  "/home/x/Code/" + repo,
		Branch:    branch,
		HeadSHA:   "abcdef1234567890",
		Status:    status,
		CreatedAt: time.Unix(1000, 0),
		UpdatedAt: time.Unix(1000, 0),
		Steps: []store.Step{
			{Name: "intent", Order: 1, Status: "completed"},
			{Name: "review", Order: 3, Status: "running"},
			{Name: "ci", Order: 10, Status: "pending"},
		},
	}
}

func TestSortUrgencyPutsWhatNeedsYouOnTop(t *testing.T) {
	pending := testRun("d", "repo", "b4", "pending")
	running := testRun("c", "repo", "b3", "running")
	failed := testRun("b", "repo", "b2", "failed")
	parked := testRun("a", "repo", "b1", "running")
	parked.ParkedSince = time.Unix(900, 0)

	runs := []store.Run{pending, running, failed, parked}
	sortRuns(runs, sortUrgency, nil, testNow)

	var got []string
	for _, r := range runs {
		got = append(got, r.ID)
	}
	want := []string{"a", "b", "c", "d"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("urgency order = %v, want %v (parked, failed, running, pending)", got, want)
		}
	}
}

func TestSortAgeIsNewestFirst(t *testing.T) {
	older := testRun("old", "repo", "b1", "running")
	older.CreatedAt = time.Unix(100, 0)
	newer := testRun("new", "repo", "b2", "running")
	newer.CreatedAt = time.Unix(999, 0)

	runs := []store.Run{older, newer}
	sortRuns(runs, sortAge, nil, testNow)
	if runs[0].ID != "new" {
		t.Errorf("age sort = %s first, want the newest run", runs[0].ID)
	}
}

func TestSortRepoGroupsProjects(t *testing.T) {
	runs := []store.Run{
		testRun("1", "zebra", "b", "running"),
		testRun("2", "alpha", "z-branch", "running"),
		testRun("3", "alpha", "a-branch", "running"),
	}
	sortRuns(runs, sortRepo, nil, testNow)
	if runs[0].ID != "3" || runs[1].ID != "2" || runs[2].ID != "1" {
		t.Errorf("repo sort = %s %s %s, want alpha/a-branch, alpha/z-branch, zebra", runs[0].ID, runs[1].ID, runs[2].ID)
	}
}

func TestSortModeCycles(t *testing.T) {
	mode := sortUrgency
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		seen[mode.String()] = true
		mode = mode.next()
	}
	if mode != sortUrgency {
		t.Error("sort mode should cycle back to urgency")
	}
	for _, want := range []string{"urgency", "finishing next", "age", "repo"} {
		if !seen[want] {
			t.Errorf("sort cycle never reached %q", want)
		}
	}
}

func TestLayoutFitsTheTerminal(t *testing.T) {
	for _, w := range []int{60, 80, 100, 140, 200} {
		cols := layout(w, 10, 13, true)
		total := markerCols + cols.repo + gap + cols.branch + gap + cols.stages + gap +
			cols.step + gap + cols.age + gap + cols.eta + gap + cols.tree + gap + cols.pr
		if !cols.showETA {
			total -= cols.eta + gap
		}
		if w >= etaMinWidth && total > w {
			t.Errorf("width %d: columns total %d, wider than the terminal", w, total)
		}
		if cols.branch < minBranch {
			t.Errorf("width %d: branch column %d is unreadably narrow", w, cols.branch)
		}
		if cols.repo < minRepo || cols.repo > maxRepo {
			t.Errorf("width %d: repo column %d out of bounds", w, cols.repo)
		}
	}
}

func TestLayoutSizesStagesToTheWidestRun(t *testing.T) {
	runs := []store.Run{testRun("a", "repo", "b", "running")}
	wide := testRun("b", "repo", "b", "running")
	wide.Steps = append(wide.Steps, store.Step{Name: "extra", Order: 11, Status: "pending"})

	if got := maxStepCount(runs, []store.Run{wide}); got != 4 {
		t.Errorf("maxStepCount = %d, want 4 (the widest run wins so strips align)", got)
	}
	if got := longestLabel(repoLabels(runs)); got != len("repo") {
		t.Errorf("longestLabel = %d", got)
	}
}

func TestRenderRowStaysInsideTheTerminal(t *testing.T) {
	run := testRun("a", "nova-go", "jeffrydegrande/a-very-long-branch-name-that-will-not-fit-in-any-column", "running")
	run.PRURL = "https://github.com/x/nova-go/pull/192"
	run.PRState = "open"

	for _, w := range []int{80, 100, 160} {
		ctx := testRowContext(layout(w, 3, 7, false))
		line := plain(renderRow(run, ctx, false))
		if len([]rune(line)) > w {
			t.Errorf("width %d: row is %d cells wide: %q", w, len([]rune(line)), line)
		}
		if !strings.Contains(line, "nova-go") {
			t.Errorf("width %d: row lost the repo name: %q", w, line)
		}
		if !strings.Contains(line, "#192 open") {
			t.Errorf("width %d: row lost the PR: %q", w, line)
		}
	}
}

func TestRenderRowMarksTheSelectedRun(t *testing.T) {
	run := testRun("a", "repo", "b", "running")
	ctx := testRowContext(layout(100, 3, 6, false))
	selected := plain(renderRow(run, ctx, true))
	unselected := plain(renderRow(run, ctx, false))

	if !strings.HasPrefix(selected, "▸ ") {
		t.Errorf("selected row = %q, want a leading marker", selected)
	}
	if strings.HasPrefix(unselected, "▸") {
		t.Errorf("unselected row should not be marked: %q", unselected)
	}
	if width(selected) != width(unselected) {
		t.Errorf("selection changed the row width: %d vs %d", width(selected), width(unselected))
	}
}

func TestHeaderRowMatchesColumnWidths(t *testing.T) {
	cols := layout(120, 10, 13, true)
	header := plain(headerRow(cols))
	run := testRun("a", "nova-go", "user/branch", "running")
	row := plain(renderRow(run, testRowContext(cols), false))

	// The STAGES header must start where the stage glyphs start, or the table
	// reads as misaligned even though every cell is padded correctly.
	headerIdx := strings.Index(header, "STAGES")
	stageIdx := strings.Index(row, glyphDone)
	if headerIdx != stageIdx {
		t.Errorf("STAGES header at %d but stages render at %d\nheader: %q\nrow:    %q", headerIdx, stageIdx, header, row)
	}
}

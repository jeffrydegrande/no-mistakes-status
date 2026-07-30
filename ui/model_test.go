package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

var testNow = time.Unix(2000, 0)

// fakeReader serves canned snapshots so the view layer is testable without a
// database.
type fakeReader struct {
	snap  store.Snapshot
	err   error
	calls int
}

func (f *fakeReader) Read(ctx context.Context, now time.Time, window time.Duration, limit int) (store.Snapshot, error) {
	f.calls++
	return f.snap, f.err
}

func newTestModel(snap store.Snapshot) Model {
	m := New(&fakeReader{snap: snap}, Options{Now: func() time.Time { return testNow }})
	return m.applySnapshot(snapshotMsg{snap: snap})
}

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func press(t *testing.T, m Model, k string) Model {
	t.Helper()
	next, _ := m.Update(key(k))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, not a Model", next)
	}
	return got
}

func pressCmd(t *testing.T, m Model, k string) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(key(k))
	got, ok := next.(Model)
	if !ok {
		t.Fatalf("Update returned %T, not a Model", next)
	}
	return got, cmd
}

func TestActiveRunsSortAboveRecentOnes(t *testing.T) {
	snap := store.Snapshot{
		Active: []store.Run{testRun("live", "repo", "b1", "running")},
		Recent: []store.Run{testRun("done", "repo", "b2", "completed")},
	}
	m := newTestModel(snap)
	if len(m.runs) != 2 || m.runs[0].ID != "live" || m.runs[1].ID != "done" {
		t.Fatalf("runs = %+v, want active first", m.runs)
	}
	if m.nActive != 1 {
		t.Errorf("nActive = %d, want 1", m.nActive)
	}

	out := plain(m.renderList())
	activeIdx := strings.Index(out, "active")
	recentIdx := strings.Index(out, "recent")
	if activeIdx < 0 || recentIdx < 0 || activeIdx > recentIdx {
		t.Errorf("sections missing or out of order:\n%s", out)
	}
}

func TestCursorFollowsTheSelectedRunAcrossRefreshes(t *testing.T) {
	first := store.Snapshot{Active: []store.Run{
		testRun("a", "repo", "b1", "running"),
		testRun("b", "repo", "b2", "running"),
		testRun("c", "repo", "b3", "running"),
	}}
	m := newTestModel(first)
	m = press(t, m, "down")
	m = press(t, m, "down")
	if m.selectedID() != "c" {
		t.Fatalf("cursor on %q, want c", m.selectedID())
	}

	// Run "a" finishes and leaves the active list. The cursor must stay on the
	// same run, not on the same row.
	second := store.Snapshot{Active: []store.Run{
		testRun("b", "repo", "b2", "running"),
		testRun("c", "repo", "b3", "running"),
	}}
	m = m.applySnapshot(snapshotMsg{snap: second})
	if m.selectedID() != "c" {
		t.Errorf("cursor moved to %q after a refresh, want it to stay on c", m.selectedID())
	}
}

func TestCursorSurvivesTheSelectedRunDisappearing(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{
		testRun("a", "repo", "b1", "running"),
		testRun("b", "repo", "b2", "running"),
	}})
	m = press(t, m, "down")

	m = m.applySnapshot(snapshotMsg{snap: store.Snapshot{}})
	if m.cursor != 0 {
		t.Errorf("cursor = %d on an empty list, want 0", m.cursor)
	}
	if _, ok := m.selected(); ok {
		t.Error("nothing should be selected when there are no runs")
	}
	if out := plain(m.renderList()); !strings.Contains(out, "nothing running") {
		t.Errorf("empty state missing:\n%s", out)
	}
}

func TestCursorStopsAtTheEnds(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{
		testRun("a", "repo", "b1", "running"),
		testRun("b", "repo", "b2", "running"),
	}})
	m = press(t, m, "up")
	if m.cursor != 0 {
		t.Errorf("cursor went above the first row: %d", m.cursor)
	}
	m = press(t, m, "down")
	m = press(t, m, "down")
	m = press(t, m, "down")
	if m.cursor != 1 {
		t.Errorf("cursor went past the last row: %d", m.cursor)
	}
}

func TestSortKeyCyclesAndReorders(t *testing.T) {
	parked := testRun("parked", "repo", "b1", "running")
	parked.ParkedSince = time.Unix(900, 0)
	parked.CreatedAt = time.Unix(100, 0)
	newer := testRun("newer", "repo", "b2", "running")
	newer.CreatedAt = time.Unix(1500, 0)

	m := newTestModel(store.Snapshot{Active: []store.Run{newer, parked}})
	if m.runs[0].ID != "parked" {
		t.Fatalf("default sort = %s first, want the parked run", m.runs[0].ID)
	}

	m = press(t, m, "s") // urgency -> age
	if m.sort != sortAge {
		t.Fatalf("sort = %s, want age", m.sort)
	}
	if m.runs[0].ID != "newer" {
		t.Errorf("age sort = %s first, want the newest run", m.runs[0].ID)
	}
}

func TestEnterOpensDetailAndEscGoesBack(t *testing.T) {
	run := testRun("a", "nova-go", "user/some-branch", "running")
	run.PRURL = "https://github.com/x/nova-go/pull/192"
	run.PRState = "open"
	run.Intent = "Bind the provider and turn output at the edge."

	m := newTestModel(store.Snapshot{Active: []store.Run{run}})
	m = press(t, m, "enter")
	if m.view != viewDetail {
		t.Fatal("enter did not open the detail view")
	}

	out := plain(m.View())
	for _, want := range []string{"nova-go", "user/some-branch", "STAGES", "#192 open", "Bind the provider"} {
		if !strings.Contains(out, want) {
			t.Errorf("detail view missing %q:\n%s", want, out)
		}
	}

	m = press(t, m, "esc")
	if m.view != viewList {
		t.Error("esc did not return to the list")
	}
}

func TestOpenWithoutAPRSaysSoInsteadOfLaunchingAnything(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{testRun("a", "repo", "b", "running")}})
	m, cmd := pressCmd(t, m, "o")
	if cmd != nil {
		t.Fatal("pressing o without a PR must not run a command")
	}
	if !strings.Contains(m.status, "no PR") {
		t.Errorf("status = %q, want an explanation", m.status)
	}
}

func TestOpenURLRejectsNonHTTPTargets(t *testing.T) {
	// The URL comes from a database this program does not own; it must never be
	// handed to a system opener unchecked.
	msg := openURL("file:///etc/passwd")()
	opened, ok := msg.(openedMsg)
	if !ok {
		t.Fatalf("got %T, want openedMsg", msg)
	}
	if opened.err == nil {
		t.Fatal("expected a non-http URL to be refused")
	}
}

func TestReadErrorIsShownNotSwallowed(t *testing.T) {
	m := New(&fakeReader{err: errors.New("database is locked")}, Options{Now: func() time.Time { return testNow }})
	m = m.applySnapshot(snapshotMsg{err: errors.New("database is locked")})
	out := plain(m.View())
	if !strings.Contains(out, "database is locked") {
		t.Errorf("error not surfaced:\n%s", out)
	}
}

func TestHelpListsEveryStageGlyph(t *testing.T) {
	m := newTestModel(store.Snapshot{})
	m = press(t, m, "?")
	if m.view != viewHelp {
		t.Fatal("? did not open help")
	}
	out := plain(m.View())
	for _, glyph := range []string{glyphDone, glyphParked, glyphFailed, glyphSkipped, glyphPending} {
		if !strings.Contains(out, glyph) {
			t.Errorf("help is missing the %q glyph:\n%s", glyph, out)
		}
	}
}

func TestHeaderCountsParkedRuns(t *testing.T) {
	parked := testRun("p", "repo", "b1", "running")
	parked.ParkedSince = time.Unix(900, 0)
	m := newTestModel(store.Snapshot{
		Active: []store.Run{parked, testRun("r", "repo", "b2", "running")},
		Recent: []store.Run{testRun("d", "repo", "b3", "completed")},
	})
	head := plain(m.renderHeader())
	for _, want := range []string{"2 active", "1 parked", "1 recent"} {
		if !strings.Contains(head, want) {
			t.Errorf("header %q missing %q", head, want)
		}
	}
}

func TestPrintOnceWritesTheTableAndNoKeyHints(t *testing.T) {
	run := testRun("a", "nova-go", "user/branch-name", "running")
	run.PRURL = "https://github.com/x/nova-go/pull/7"
	run.PRState = "open"
	reader := &fakeReader{snap: store.Snapshot{Active: []store.Run{run}}}

	var buf bytes.Buffer
	if err := PrintOnce(context.Background(), &buf, reader, Options{Now: func() time.Time { return testNow }}); err != nil {
		t.Fatal(err)
	}
	out := plain(buf.String())
	for _, want := range []string{"nova-go", "branch-name", "#7 open", "REPO"} {
		if !strings.Contains(out, want) {
			t.Errorf("one-shot output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "q quit") {
		t.Errorf("one-shot output should not print key hints:\n%s", out)
	}
	if reader.calls != 1 {
		t.Errorf("PrintOnce read the database %d times, want 1", reader.calls)
	}
}

func TestPrintOnceReturnsReadErrors(t *testing.T) {
	reader := &fakeReader{err: errors.New("boom")}
	var buf bytes.Buffer
	if err := PrintOnce(context.Background(), &buf, reader, Options{}); err == nil {
		t.Fatal("expected the read error to surface")
	}
}

func TestDetailScrollsWithoutRunningOffTheEnd(t *testing.T) {
	run := testRun("a", "repo", "b", "running")
	run.Intent = strings.Repeat("a long line of intent text that wraps and wraps. ", 40)
	m := newTestModel(store.Snapshot{Active: []store.Run{run}})
	m.height = 20
	m = press(t, m, "enter")

	for i := 0; i < 200; i++ {
		m = press(t, m, "down")
	}
	out := m.View()
	if strings.TrimSpace(plain(out)) == "" {
		t.Fatal("scrolling to the end emptied the detail view")
	}

	for i := 0; i < 500; i++ {
		m = press(t, m, "up")
	}
	if m.detailScroll != 0 {
		t.Errorf("scroll = %d after scrolling up past the top, want 0", m.detailScroll)
	}
}

func TestHyperlinkWrapsTheURL(t *testing.T) {
	got := hyperlink("https://example.com/pr/1", "#1")
	if !strings.Contains(got, "https://example.com/pr/1") || !strings.Contains(got, "#1") {
		t.Errorf("hyperlink = %q", got)
	}
	if hyperlink("", "text") != "text" {
		t.Error("an empty URL should render as plain text")
	}
}

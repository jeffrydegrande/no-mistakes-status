package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// --- notifications ---------------------------------------------------------

func TestNotifierStaysQuietOnTheFirstSnapshot(t *testing.T) {
	// Starting the dashboard with six runs already in flight must not fire six
	// notifications for things you already knew about.
	n := newNotifier(true)
	parked := testRun("a", "repo", "b", "running")
	parked.ParkedSince = time.Unix(900, 0)

	if got := n.events(store.Snapshot{Active: []store.Run{parked}}, nil); len(got) != 0 {
		t.Fatalf("first snapshot produced %d notifications, want 0", len(got))
	}
}

func TestNotifierFiresOnceWhenARunParks(t *testing.T) {
	n := newNotifier(true)
	run := testRun("a", "nova-go", "user/branch", "running")
	n.events(store.Snapshot{Active: []store.Run{run}}, nil) // prime

	run.ParkedSince = time.Unix(900, 0)
	run.Steps[1].Status = "awaiting_approval"
	events := n.events(store.Snapshot{Active: []store.Run{run}}, nil)
	if len(events) != 1 {
		t.Fatalf("got %d notifications, want 1", len(events))
	}
	if !strings.Contains(events[0].Title, "nova-go") || !strings.Contains(events[0].Title, "waiting on you") {
		t.Errorf("title = %q", events[0].Title)
	}

	// Still parked five seconds later is not news.
	if again := n.events(store.Snapshot{Active: []store.Run{run}}, nil); len(again) != 0 {
		t.Errorf("a run that stays parked notified again: %+v", again)
	}
}

func TestNotifierFiresOnFailureAndOnGettingStuck(t *testing.T) {
	n := newNotifier(true)
	run := testRun("a", "repo", "b", "running")
	n.events(store.Snapshot{Active: []store.Run{run}}, nil)

	stuck := n.events(store.Snapshot{Active: []store.Run{run}}, map[string]stallKind{"a": stallSilent})
	if len(stuck) != 1 || !strings.Contains(stuck[0].Title, "stuck") {
		t.Fatalf("stall notification = %+v", stuck)
	}

	failed := testRun("b", "repo", "b2", "running")
	n.events(store.Snapshot{Active: []store.Run{failed}}, nil)
	failed.Status = "failed"
	events := n.events(store.Snapshot{Recent: []store.Run{failed}}, nil)
	if len(events) != 1 || !strings.Contains(events[0].Title, "failed") {
		t.Fatalf("failure notification = %+v", events)
	}
}

func TestNotifierTracksStateEvenWhenDisabled(t *testing.T) {
	// Turning notifications off must not turn the transition tracking off, or
	// enabling them later would fire for everything at once.
	n := newNotifier(false)
	run := testRun("a", "repo", "b", "running")
	n.events(store.Snapshot{Active: []store.Run{run}}, nil)
	run.ParkedSince = time.Unix(900, 0)
	if got := n.events(store.Snapshot{Active: []store.Run{run}}, nil); got != nil {
		t.Errorf("disabled notifier emitted %+v", got)
	}
	if !n.seen["a"].parked {
		t.Error("disabled notifier stopped tracking state")
	}
}

func TestNotifierForgetsRunsThatAgeOut(t *testing.T) {
	n := newNotifier(true)
	n.events(store.Snapshot{Active: []store.Run{testRun("a", "repo", "b", "running")}}, nil)
	n.events(store.Snapshot{Active: []store.Run{testRun("b", "repo", "b2", "running")}}, nil)
	if _, ok := n.seen["a"]; ok {
		t.Error("a run that left the window is still tracked, which leaks for the life of the process")
	}
}

func TestOSAQuoteEscapesArbitraryText(t *testing.T) {
	got := osaQuote(`say "hi"` + "\n" + `c:\path`)
	if strings.Contains(got[1:len(got)-1], `"`) && !strings.Contains(got, `\"`) {
		t.Errorf("unescaped quote in %q", got)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("newline survived into an AppleScript literal: %q", got)
	}
}

// --- stall detection -------------------------------------------------------

func stalledRun(idle time.Duration, pid int) store.Run {
	run := testRun("a", "repo", "b", "running")
	run.Steps = []store.Step{
		{Name: "intent", Order: 1, Status: "completed"},
		{Name: "review", Order: 3, Status: "running", AgentPID: pid, LastActivityAt: testNow.Add(-idle)},
	}
	return run
}

func newTestStallCheck(after time.Duration, alive bool) stallCheck {
	check := newStallCheck(after, testNow)
	check.alive = func(int) bool { return alive }
	return check
}

func TestStallFlagsAStepThatHasGoneQuiet(t *testing.T) {
	check := newTestStallCheck(5*time.Minute, true)

	kind, idle := check.classify(stalledRun(20*time.Minute, 1234))
	if kind != stallSilent {
		t.Fatalf("kind = %v, want silent", kind)
	}
	if idle != 20*time.Minute {
		t.Errorf("idle = %s, want 20m", idle)
	}

	if kind, _ := check.classify(stalledRun(time.Minute, 1234)); kind != stallNone {
		t.Errorf("a step quiet for one minute was flagged as %v", kind)
	}
}

func TestStallFlagsAnAgentThatIsGone(t *testing.T) {
	check := newTestStallCheck(time.Hour, false)
	kind, _ := check.classify(stalledRun(time.Second, 4242))
	if kind != stallGone {
		t.Fatalf("kind = %v, want the missing agent to be caught immediately", kind)
	}

	// No recorded pid means nothing to check; that is not evidence of trouble.
	if kind, _ := check.classify(stalledRun(time.Second, 0)); kind != stallNone {
		t.Errorf("a step with no recorded pid was flagged as %v", kind)
	}
}

func TestStallIgnoresRunsThatAreNotWorking(t *testing.T) {
	check := newTestStallCheck(time.Minute, true)

	parked := stalledRun(time.Hour, 1234)
	parked.ParkedSince = testNow.Add(-time.Hour)
	if kind, _ := check.classify(parked); kind != stallNone {
		t.Error("a parked run is waiting on you on purpose, not stuck")
	}

	done := stalledRun(time.Hour, 1234)
	done.Status = "completed"
	if kind, _ := check.classify(done); kind != stallNone {
		t.Error("a finished run cannot be stuck")
	}
}

func TestStallFallsBackToStartTimeWhenNothingWasEverReported(t *testing.T) {
	// An agent that wedges before its first line of output has no last activity
	// at all, which is exactly the case worth catching.
	run := testRun("a", "repo", "b", "running")
	run.Steps = []store.Step{{Name: "review", Order: 3, Status: "running", StartedAt: testNow.Add(-30 * time.Minute)}}

	check := newTestStallCheck(5*time.Minute, true)
	kind, idle := check.classify(run)
	if kind != stallSilent || idle != 30*time.Minute {
		t.Fatalf("kind = %v idle = %s, want silent after 30m", kind, idle)
	}
}

func TestStuckRunsAreMarkedInTheTableAndHeader(t *testing.T) {
	run := stalledRun(time.Hour, 0)
	m := newTestModel(store.Snapshot{Active: []store.Run{run}})
	m.stalls = map[string]stallKind{run.ID: stallSilent}

	out := plain(m.renderList())
	if !strings.Contains(out, "⚠") {
		t.Errorf("stuck run not marked in the table:\n%s", out)
	}
	if !strings.Contains(out, "1 stuck") {
		t.Errorf("header does not count stuck runs:\n%s", out)
	}
	if !strings.Contains(out, "stuck") {
		t.Errorf("estimate column still forecasts a stuck run:\n%s", out)
	}
}

func TestDetailExplainsWhyARunLooksStuck(t *testing.T) {
	lines := detailLines(stalledRun(time.Hour, 0), detailContext{
		now:       testNow,
		width:     80,
		stall:     stallSilent,
		stallIdle: time.Hour,
	})
	out := plain(strings.Join(lines, "\n"))
	if !strings.Contains(out, "waiting on a prompt nobody can answer") {
		t.Errorf("detail does not explain the stall:\n%s", out)
	}

	gone := detailLines(stalledRun(time.Minute, 99), detailContext{now: testNow, width: 80, stall: stallGone})
	if !strings.Contains(plain(strings.Join(gone, "\n")), "agent process is gone") {
		t.Error("detail does not explain a missing agent")
	}
}

// --- estimates -------------------------------------------------------------

func runWithSteps(steps ...store.Step) store.Run {
	run := testRun("a", "repo", "b", "running")
	run.Steps = steps
	return run
}

func TestEstimateAddsWhatIsLeftToWhatIsAhead(t *testing.T) {
	run := runWithSteps(
		store.Step{Name: "intent", Order: 1, Status: "completed"},
		store.Step{Name: "test", Order: 4, Status: "running", StartedAt: testNow.Add(-time.Minute)},
		store.Step{Name: "lint", Order: 6, Status: "pending"},
		store.Step{Name: "ci", Order: 10, Status: "pending"},
	)
	baselines := store.Baselines{}
	for name, d := range map[string]time.Duration{"test": 5 * time.Minute, "lint": time.Minute, "ci": 4 * time.Minute} {
		baselines[baselineKeyFor(run.RepoPath, name)] = d
	}

	got, ok := estimate(run, baselines, testNow)
	if !ok {
		t.Fatal("no estimate produced")
	}
	// 4m left on test, plus 1m lint, plus 4m ci.
	if got != 9*time.Minute {
		t.Errorf("estimate = %s, want 9m", got)
	}
}

func TestEstimateIsHonestWhenItHasNoHistory(t *testing.T) {
	run := runWithSteps(store.Step{Name: "test", Order: 4, Status: "running"})
	if _, ok := estimate(run, store.Baselines{}, testNow); ok {
		t.Error("produced an estimate with nothing to base it on")
	}
	if got := etaLabel(run, store.Baselines{}, testNow); got != "-" {
		t.Errorf("label = %q, want a dash", got)
	}
}

func TestEstimateNeverGoesNegativeOnAnOverrunningStep(t *testing.T) {
	run := runWithSteps(store.Step{Name: "test", Order: 4, Status: "running", StartedAt: testNow.Add(-time.Hour)})
	baselines := store.Baselines{baselineKeyFor(run.RepoPath, "test"): time.Minute}

	got, ok := estimate(run, baselines, testNow)
	if !ok || got != 0 {
		t.Fatalf("estimate = %s (%v), want zero rather than a negative countdown", got, ok)
	}
	if label := etaLabel(run, baselines, testNow); label != "any time" {
		t.Errorf("label = %q", label)
	}
}

func TestParkedRunsShowWhoTheyAreWaitingOn(t *testing.T) {
	run := runWithSteps(store.Step{Name: "review", Order: 3, Status: "awaiting_approval"})
	run.ParkedSince = testNow.Add(-time.Minute)
	if got := etaLabel(run, store.Baselines{}, testNow); got != "you" {
		t.Errorf("parked label = %q, want 'you'", got)
	}
}

func TestSortByFinishingNextPutsUnknownsLast(t *testing.T) {
	soon := runWithSteps(store.Step{Name: "ci", Order: 10, Status: "running", StartedAt: testNow})
	soon.ID = "soon"
	later := runWithSteps(store.Step{Name: "test", Order: 4, Status: "running", StartedAt: testNow})
	later.ID = "later"
	unknown := runWithSteps(store.Step{Name: "mystery", Order: 5, Status: "running", StartedAt: testNow})
	unknown.ID = "unknown"

	baselines := store.Baselines{
		baselineKeyFor(soon.RepoPath, "ci"):    time.Minute,
		baselineKeyFor(later.RepoPath, "test"): 10 * time.Minute,
	}

	runs := []store.Run{unknown, later, soon}
	sortRuns(runs, sortETA, baselines, testNow)
	if runs[0].ID != "soon" || runs[1].ID != "later" || runs[2].ID != "unknown" {
		t.Errorf("order = %s %s %s, want soon, later, unknown", runs[0].ID, runs[1].ID, runs[2].ID)
	}
}

// baselineKeyFor mirrors the store's key layout for test fixtures.
func baselineKeyFor(repoPath, step string) string { return repoPath + "\x00" + step }

// --- log formatting --------------------------------------------------------

func TestParseLogSeparatesProseFromPayloads(t *testing.T) {
	raw := `reviewing changes...

codex started pid=1053912

{
  "findings": [],
  "summary": "first pass"
}{"findings":[],"summary":"second pass"}
codex exited pid=1053912 status=success`

	entries := parseLog(raw)
	var objects int
	var texts []string
	for _, e := range entries {
		if e.object != nil {
			objects++
			continue
		}
		texts = append(texts, e.text)
	}
	if objects != 2 {
		t.Errorf("found %d payloads, want 2 (one pretty-printed, one concatenated)", objects)
	}
	if len(texts) != 3 {
		t.Errorf("plain lines = %v, want 3", texts)
	}
}

func TestParseLogKeepsMalformedPayloadsAsText(t *testing.T) {
	entries := parseLog(`{"truncated": `)
	if len(entries) != 1 || entries[0].object != nil {
		t.Errorf("a half-written payload should fall back to text: %+v", entries)
	}
}

func TestRenderLogLeadsWithTheSummary(t *testing.T) {
	raw := `{"risk_level":"low","summary":"validated the cap","tested":null,"findings":[]}`
	out := plain(strings.Join(renderLog(raw, 80), "\n"))

	if !strings.Contains(out, "validated the cap") {
		t.Fatalf("summary missing:\n%s", out)
	}
	if strings.Index(out, "summary") > strings.Index(out, "risk_level") {
		t.Errorf("summary should come before schema noise:\n%s", out)
	}
	if strings.Contains(out, "tested") {
		t.Errorf("null fields should be hidden:\n%s", out)
	}
	if strings.Contains(out, "{") || strings.Contains(out, "\"") {
		t.Errorf("payload was not reformatted:\n%s", out)
	}
}

func TestRenderLogListsArrayValues(t *testing.T) {
	raw := `{"tested":["make test","go vet ./..."]}`
	out := plain(strings.Join(renderLog(raw, 80), "\n"))
	for _, want := range []string{"make test", "go vet"} {
		if !strings.Contains(out, want) {
			t.Errorf("array item %q missing:\n%s", want, out)
		}
	}
}

func TestReadTailDropsThePartialFirstLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "step.log")
	if err := os.WriteFile(path, []byte("first line\nsecond line\nthird line\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readTail(path, 20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "first line") {
		t.Errorf("tail included the truncated line: %q", got)
	}
	if !strings.Contains(got, "third line") {
		t.Errorf("tail lost the end of the file: %q", got)
	}

	whole, err := readTail(path, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(whole, "first line") {
		t.Errorf("a file smaller than the limit should be read whole: %q", whole)
	}
}

func TestLogKeyOpensTheLogView(t *testing.T) {
	run := testRun("a", "repo", "b", "running")
	run.Steps[1].LogPath = filepath.Join(t.TempDir(), "review.log")
	if err := os.WriteFile(run.Steps[1].LogPath, []byte("hello from the agent\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestModel(store.Snapshot{Active: []store.Run{run}})
	m, cmd := pressCmd(t, m, "d")
	if m.view != viewLog {
		t.Fatal("d did not open the log view")
	}
	if cmd == nil {
		t.Fatal("d did not start a log read")
	}

	next, _ := m.Update(cmd())
	m = next.(Model)
	if !strings.Contains(plain(m.View()), "hello from the agent") {
		t.Errorf("log view did not show the log:\n%s", plain(m.View()))
	}
}

func TestLogKeySaysSoWhenThereIsNoLogYet(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{testRun("a", "repo", "b", "running")}})
	m, cmd := pressCmd(t, m, "d")
	if m.view == viewLog {
		t.Error("opened an empty log view")
	}
	if cmd != nil {
		t.Error("started a read with no file to read")
	}
	if !strings.Contains(m.status, "no log") {
		t.Errorf("status = %q", m.status)
	}
}

// --- findings --------------------------------------------------------------

func TestDetailListsFindingsWithTheOnesNeedingYouFirst(t *testing.T) {
	run := testRun("a", "repo", "b", "running")
	run.Steps[1].Findings = []store.Finding{
		{Action: "auto-fix", Severity: "low", File: "b.go", Line: 7, Description: "tidy this up"},
		{Action: "ask-user", Severity: "high", File: "a.go", Line: 42, Description: "this changes behavior"},
	}
	run.Steps[1].FindingCount = 2
	run.Steps[1].NeedsUser = 1

	out := plain(strings.Join(detailLines(run, detailContext{now: testNow, width: 90}), "\n"))
	if !strings.Contains(out, "FINDINGS") {
		t.Fatalf("no findings section:\n%s", out)
	}
	if !strings.Contains(out, "a.go:42") || !strings.Contains(out, "this changes behavior") {
		t.Errorf("finding detail missing:\n%s", out)
	}
	if strings.Index(out, "ask-user") > strings.Index(out, "auto-fix") {
		t.Errorf("findings needing a human should be listed first:\n%s", out)
	}
}

func TestFindingsWithNoActionAreShownAsNeedingYou(t *testing.T) {
	run := testRun("a", "repo", "b", "running")
	run.Steps[1].Findings = []store.Finding{{Severity: "high", Description: "unclassified"}}
	out := plain(strings.Join(findingLines(run, 80), "\n"))
	if !strings.Contains(out, "ask-user") {
		t.Errorf("an unclassified finding should read as needing you:\n%s", out)
	}
}

// --- filters ---------------------------------------------------------------

func TestParkedFilterShowsOnlyWhatIsWaitingOnYou(t *testing.T) {
	parked := testRun("parked", "repo", "b1", "running")
	parked.ParkedSince = time.Unix(900, 0)
	m := newTestModel(store.Snapshot{Active: []store.Run{
		parked,
		testRun("busy", "repo", "b2", "running"),
	}})

	m = press(t, m, "p")
	if len(m.runs) != 1 || m.runs[0].ID != "parked" {
		t.Fatalf("parked filter left %+v", m.runs)
	}
	if !strings.Contains(plain(m.renderHeader()), "parked only") {
		t.Errorf("header does not show the filter:\n%s", plain(m.renderHeader()))
	}

	m = press(t, m, "p")
	if len(m.runs) != 2 {
		t.Errorf("pressing p again did not clear the filter: %+v", m.runs)
	}
}

func TestRepoFilterFollowsTheSelectedRunAndClears(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{
		testRun("a", "nova-go", "b1", "running"),
		testRun("b", "portal", "b2", "running"),
	}})
	m = press(t, m, "down") // select the portal run
	m = press(t, m, "f")

	if len(m.runs) != 1 || m.runs[0].ID != "b" {
		t.Fatalf("repo filter left %+v", m.runs)
	}
	if !strings.Contains(plain(m.renderHeader()), "portal") {
		t.Errorf("header does not name the repo:\n%s", plain(m.renderHeader()))
	}

	m = press(t, m, "f")
	if len(m.runs) != 2 {
		t.Errorf("pressing f again did not clear the filter: %+v", m.runs)
	}
}

func TestFilteringEverythingAwayExplainsItself(t *testing.T) {
	m := newTestModel(store.Snapshot{Active: []store.Run{testRun("a", "repo", "b", "running")}})
	m = press(t, m, "p")
	out := plain(m.renderList())
	if !strings.Contains(out, "no runs match this filter") {
		t.Errorf("empty filtered state is confusing:\n%s", out)
	}
}

func TestFiltersSurviveARefresh(t *testing.T) {
	parked := testRun("parked", "repo", "b1", "running")
	parked.ParkedSince = time.Unix(900, 0)
	snap := store.Snapshot{Active: []store.Run{parked, testRun("busy", "repo", "b2", "running")}}

	m := newTestModel(snap)
	m = press(t, m, "p")
	m = m.applySnapshot(snapshotMsg{snap: snap})
	if len(m.runs) != 1 {
		t.Errorf("a refresh dropped the filter: %+v", m.runs)
	}
}

// --- repo labels -----------------------------------------------------------

func TestRepoLabelsDisambiguateTwoCheckoutsOfOneProject(t *testing.T) {
	a := testRun("a", "nova-go", "b1", "running")
	b := testRun("b", "nova-go", "b2", "running")
	b.RepoPath = "/home/x/Work/nova-go"
	c := testRun("c", "portal", "b3", "running")

	labels := repoLabels([]store.Run{a, b, c})
	if labels[a.RepoPath] == labels[b.RepoPath] {
		t.Fatalf("two different checkouts share the label %q", labels[a.RepoPath])
	}
	if labels[a.RepoPath] != "Code/nova-go" || labels[b.RepoPath] != "Work/nova-go" {
		t.Errorf("labels = %q and %q, want the parent directory added", labels[a.RepoPath], labels[b.RepoPath])
	}
	if labels[c.RepoPath] != "portal" {
		t.Errorf("an unambiguous repo should stay short: %q", labels[c.RepoPath])
	}
}

// --- treehouse -------------------------------------------------------------

const treehouseStatus = `1     available    ~/.treehouse/nova-go-95b98f/1/nova-go
2     dirty        ~/.treehouse/nova-go-95b98f/2/nova-go
4     leased       ~/.treehouse/nova-go-95b98f/4/nova-go  (held by NGO-146)
5     leased       ~/.treehouse/nova-go-95b98f/5/nova-go  (held by NGO-167)
                   zsh (554386), claude (554389), codex (554574)
`

func TestParseLeasesReadsOnlyLeasedWorktrees(t *testing.T) {
	leases := parseLeases(treehouseStatus)
	if len(leases) != 2 {
		t.Fatalf("got %d leases, want 2: %+v", len(leases), leases)
	}
	if leases[1].slot != "5" || leases[1].holder != "NGO-167" {
		t.Errorf("lease = %+v", leases[1])
	}
}

func TestSlotForMatchesTheTaskKeyInTheBranch(t *testing.T) {
	leases := parseLeases(treehouseStatus)

	run := testRun("a", "nova-go", "jeffrydegrande/ngo-167-bound-provider", "running")
	if got := slotFor(run, leases); got != "5" {
		t.Errorf("slot = %q, want 5", got)
	}

	// NGO-146 must not match ngo-1467: a prefix is not a task key.
	other := testRun("b", "nova-go", "jeffrydegrande/ngo-1467-something", "running")
	if got := slotFor(other, leases); got != "" {
		t.Errorf("slot = %q, want no match for a longer task number", got)
	}

	unrelated := testRun("c", "nova-go", "jeffrydegrande/fix-a-typo", "running")
	if got := slotFor(unrelated, leases); got != "" {
		t.Errorf("slot = %q, want no match", got)
	}
}

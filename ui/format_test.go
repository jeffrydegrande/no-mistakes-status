package ui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m|\x1b\]8;;[^\x1b]*\x1b\\`)

func plain(s string) string { return ansiRE.ReplaceAllString(s, "") }

func width(s string) int { return len([]rune(plain(s))) }

func TestAge(t *testing.T) {
	tests := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-5 * time.Second, "0s"},
		{45 * time.Second, "45s"},
		{90 * time.Second, "1m"},
		{18 * time.Minute, "18m"},
		{time.Hour, "1h"},
		{2*time.Hour + 5*time.Minute, "2h5m"},
		{25 * time.Hour, "1d1h"},
		{48 * time.Hour, "2d"},
	}
	for _, tc := range tests {
		if got := age(tc.in); got != tc.want {
			t.Errorf("age(%s) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestTruncateAndCellKeepExactWidth(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate kept %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("truncate = %q, want abcd…", got)
	}
	if got := truncate("abc", 1); got != "…" {
		t.Errorf("truncate to 1 = %q", got)
	}
	if got := truncate("abc", 0); got != "" {
		t.Errorf("truncate to 0 = %q", got)
	}
	// Every cell must occupy exactly its column width or the table skews.
	for _, in := range []string{"", "a", "abcdefghijklmnop"} {
		if got := cell(in, 8); len([]rune(got)) != 8 {
			t.Errorf("cell(%q, 8) has width %d: %q", in, len([]rune(got)), got)
		}
	}
}

func TestStageGlyphs(t *testing.T) {
	tests := map[string]string{
		"completed":         glyphDone,
		"pending":           glyphPending,
		"":                  glyphPending,
		"awaiting_approval": glyphParked,
		"fix_review":        glyphParked,
		"fixing":            glyphFixing,
		"failed":            glyphFailed,
		"skipped":           glyphSkipped,
	}
	for status, want := range tests {
		if got := stageGlyph(status, 0); got != want {
			t.Errorf("stageGlyph(%q) = %q, want %q", status, got, want)
		}
	}
	// A running stage animates, and the frame counter is safe at any value.
	if stageGlyph("running", 0) == stageGlyph("running", 1) {
		t.Error("running glyph should change between frames")
	}
	if got := stageGlyph("running", -3); got == "" {
		t.Error("negative frames must not panic or return empty")
	}
	if got := stageGlyph("running", 1_000_003); got == "" {
		t.Error("large frames must stay in range")
	}
}

func TestStageStripPadsToWidth(t *testing.T) {
	// The strip is padded to a fixed number of slots so runs with different
	// stage counts still line up in the table.
	steps := []store.Step{{Status: "completed"}, {Status: "pending"}}
	got := plain(renderStages(steps, 0, 5))
	if len([]rune(got)) != 5 {
		t.Fatalf("strip width = %d, want 5: %q", len([]rune(got)), got)
	}
	if !strings.HasPrefix(got, glyphDone+glyphPending) {
		t.Errorf("strip = %q", got)
	}
}

func TestPRNumberAcrossHosts(t *testing.T) {
	tests := map[string]string{
		"https://github.com/x/y/pull/192":              "#192",
		"https://github.com/x/y/pull/192/":             "#192",
		"https://gitlab.com/x/y/-/merge_requests/5187": "#5187",
		"https://bitbucket.org/x/y/pull-requests/12":   "#12",
		"https://github.com/x/y/pull/192#issuecomment": "#192",
		"":                              "",
		"https://example.com/no-number": "",
	}
	for url, want := range tests {
		if got := prNumber(url); got != want {
			t.Errorf("prNumber(%q) = %q, want %q", url, got, want)
		}
	}
}

func TestPRLabel(t *testing.T) {
	tests := []struct {
		name string
		run  store.Run
		want string
	}{
		{"no pr yet", store.Run{}, "-"},
		{"state none reads as no pr", store.Run{PRState: "none"}, "-"},
		{"open pr", store.Run{PRURL: "https://github.com/x/y/pull/192", PRState: "open"}, "#192 open"},
		{"merged pr", store.Run{PRURL: "https://github.com/x/y/pull/9", PRState: "merged"}, "#9 merged"},
		{"url without a state", store.Run{PRURL: "https://github.com/x/y/pull/3"}, "#3"},
	}
	for _, tc := range tests {
		if got := prLabel(tc.run); got != tc.want {
			t.Errorf("%s: prLabel = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestStepLabelFlagsWhatNeedsYou(t *testing.T) {
	parked := store.Run{
		Status:      "running",
		ParkedSince: time.Unix(100, 0),
		Steps:       []store.Step{{Name: "review", Status: "awaiting_approval"}},
	}
	if got := stepLabel(parked); got != "review !" {
		t.Errorf("parked run label = %q, want 'review !'", got)
	}

	failed := store.Run{Status: "failed", Steps: []store.Step{{Name: "test", Status: "failed"}}}
	if got := stepLabel(failed); got != "test ✗" {
		t.Errorf("failed run label = %q", got)
	}

	fixing := store.Run{Status: "running", Steps: []store.Step{{Name: "review", Status: "fixing"}}}
	if got := stepLabel(fixing); got != "review fix" {
		t.Errorf("fixing run label = %q", got)
	}

	done := store.Run{Status: "completed", Steps: []store.Step{{Name: "ci", Status: "completed"}}}
	if got := stepLabel(done); got != "done" {
		t.Errorf("completed run label = %q", got)
	}

	running := store.Run{Status: "running", Steps: []store.Step{{Name: "ci", Status: "running"}}}
	if got := stepLabel(running); got != "ci" {
		t.Errorf("running run label = %q", got)
	}
}

func TestRunAgeCountsFromTheRightClock(t *testing.T) {
	now := time.Unix(1000, 0)
	active := store.Run{Status: "running", CreatedAt: time.Unix(400, 0), UpdatedAt: time.Unix(900, 0)}
	if got := runAge(active, now); got != 600*time.Second {
		t.Errorf("active run age = %s, want time since it started", got)
	}
	finished := store.Run{Status: "completed", CreatedAt: time.Unix(400, 0), UpdatedAt: time.Unix(900, 0)}
	if got := runAge(finished, now); got != 100*time.Second {
		t.Errorf("finished run age = %s, want time since it ended", got)
	}
}

func TestActivityCollapsesMultilineAgentOutput(t *testing.T) {
	now := time.Unix(1000, 0)
	step := store.Step{
		LastActivity:   "log: line one\n\tline two   with   gaps",
		LastActivityAt: time.Unix(940, 0),
	}
	got := activity(step, now)
	if strings.ContainsAny(got, "\n\t") {
		t.Fatalf("activity leaked a newline or tab: %q", got)
	}
	if got != "1m ago: log: line one line two with gaps" {
		t.Errorf("activity = %q", got)
	}
	if activity(store.Step{}, now) != "" {
		t.Error("an empty activity should render as empty")
	}
}

func TestWrapBreaksOnWords(t *testing.T) {
	lines := wrap("the quick brown fox jumps", 10)
	for _, l := range lines {
		if len([]rune(l)) > 10 {
			t.Errorf("line too wide: %q", l)
		}
	}
	if strings.Join(lines, " ") != "the quick brown fox jumps" {
		t.Errorf("wrap lost text: %v", lines)
	}
	if wrap("   ", 10) != nil {
		t.Error("whitespace should wrap to nothing")
	}
}

func TestShortBranchDropsTheOwnerPrefix(t *testing.T) {
	if got := shortBranch("jeffrydegrande/ngo-167-thing"); got != "ngo-167-thing" {
		t.Errorf("shortBranch = %q", got)
	}
	if got := shortBranch("main"); got != "main" {
		t.Errorf("shortBranch(main) = %q", got)
	}
	if got := shortBranch("trailing/"); got != "trailing/" {
		t.Errorf("shortBranch with a trailing slash = %q", got)
	}
}

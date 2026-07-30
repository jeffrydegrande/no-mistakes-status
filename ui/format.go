package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// Stage glyphs. Every one of these is a single terminal cell wide, which the
// column layout depends on.
const (
	glyphDone    = "●"
	glyphPending = "○"
	glyphParked  = "◍"
	glyphFixing  = "◑"
	glyphFailed  = "✗"
	glyphSkipped = "⊘"
)

// runningFrames animates a running stage.
var runningFrames = []string{"◐", "◓", "◑", "◒"}

// stageGlyph maps a step status to its glyph. frame animates running steps.
func stageGlyph(status string, frame int) string {
	switch status {
	case "completed":
		return glyphDone
	case "running":
		return runningFrames[((frame%len(runningFrames))+len(runningFrames))%len(runningFrames)]
	case "fixing":
		return glyphFixing
	case "awaiting_approval", "fix_review":
		return glyphParked
	case "failed":
		return glyphFailed
	case "skipped":
		return glyphSkipped
	default:
		return glyphPending
	}
}

// age renders a duration the way a human reads a dashboard: two units at most,
// and never more precision than the number deserves.
func age(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		h := int(d.Hours())
		m := int(d.Minutes()) - h*60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	default:
		days := int(d.Hours()) / 24
		h := int(d.Hours()) - days*24
		if h == 0 {
			return fmt.Sprintf("%dd", days)
		}
		return fmt.Sprintf("%dd%dh", days, h)
	}
}

// truncate shortens s to width cells, marking the cut with an ellipsis.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return "…"
	}
	return string(r[:width-1]) + "…"
}

// pad right-pads s to width cells.
func pad(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(r))
}

// cell truncates and pads in one step, so a column always occupies width cells.
func cell(s string, width int) string { return pad(truncate(s, width), width) }

// prLabel renders the PR column: number plus state, or a dash when the run has
// not opened one yet.
func prLabel(run store.Run) string {
	number := prNumber(run.PRURL)
	state := strings.TrimSpace(run.PRState)
	if state == "none" {
		state = ""
	}
	switch {
	case number == "" && state == "":
		return "-"
	case number == "":
		return state
	case state == "":
		return number
	default:
		return number + " " + state
	}
}

// prNumber extracts "#192" from a PR or MR URL. Hosts differ in path shape
// (GitHub /pull/192, GitLab /-/merge_requests/192), so we take the last numeric
// path segment rather than matching a host-specific pattern.
func prNumber(url string) string {
	if url == "" {
		return ""
	}
	url, _, _ = strings.Cut(url, "#")
	url, _, _ = strings.Cut(url, "?")
	parts := strings.Split(strings.TrimRight(url, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if isDigits(parts[i]) {
			return "#" + parts[i]
		}
	}
	return ""
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// stepLabel names the stage a run is standing on, flagged when it needs a
// human: "!" for parked, "✗" for failed.
func stepLabel(run store.Run) string {
	step, ok := run.Current()
	if !ok {
		return run.Status
	}
	switch {
	case run.Status == "cancelled":
		return "cancelled"
	case run.Status == "completed":
		return "done"
	case run.Status == "failed":
		return step.Name + " ✗"
	case run.Parked(), step.Status == "awaiting_approval", step.Status == "fix_review":
		return step.Name + " !"
	case step.Status == "fixing":
		return step.Name + " fix"
	default:
		return step.Name
	}
}

// runAge is time on the clock for an active run, and time since it ended for a
// finished one.
func runAge(run store.Run, now time.Time) time.Duration {
	if run.Terminal() {
		return now.Sub(run.UpdatedAt)
	}
	return now.Sub(run.CreatedAt)
}

// activity renders a step's last reported activity as "52s ago: <text>",
// collapsed to a single line.
func activity(step store.Step, now time.Time) string {
	text := oneLine(step.LastActivity)
	if text == "" {
		return ""
	}
	if step.LastActivityAt.IsZero() {
		return text
	}
	return age(now.Sub(step.LastActivityAt)) + " ago: " + text
}

// oneLine collapses whitespace so multi-line agent output cannot break the
// table layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// hyperlink wraps text in an OSC 8 terminal hyperlink. Terminals that do not
// support it print the text unchanged, so this is always safe to emit.
func hyperlink(url, text string) string {
	if url == "" {
		return text
	}
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// detailContext is everything the detail page needs beyond the run.
type detailContext struct {
	frame     int
	now       time.Time
	width     int
	baselines store.Baselines
	slot      string
	stall     stallKind
	stallIdle time.Duration
	// logLines is the formatted tail of the run's current log, loaded
	// asynchronously; it is empty until the read lands.
	logLines []string
	logName  string
}

// logPreviewLines is how much of the log the detail page shows before telling
// you to open the full view.
const logPreviewLines = 12

// detailLines renders the full picture of one run: where it is, what it is
// working on, what it found, and where the PR lives.
func detailLines(run store.Run, ctx detailContext) []string {
	labelWidth := 8
	field := func(label, value string) string {
		return sDim.Render(pad(label, labelWidth)) + value
	}

	var out []string
	out = append(out,
		sBold.Render(run.RepoName())+"  "+sAccent.Render(run.Branch),
		"",
		field("run", sDim.Render(run.ID)),
		field("dir", run.RepoPath),
	)
	if ctx.slot != "" {
		out = append(out, field("worktree", sDim.Render("treehouse slot "+ctx.slot)))
	}
	out = append(out,
		field("head", shortSHA(run.HeadSHA)),
		field("status", runStyle(run.Status, run.Parked()).Render(run.Status)+sDim.Render("  ·  "+age(runAge(run, ctx.now))+" old")),
	)
	if eta, ok := estimate(run, ctx.baselines, ctx.now); ok && !run.Parked() {
		out = append(out, field("left", sDim.Render("about "+age(eta)+" by past runs of this repo")))
	}
	if run.Parked() {
		out = append(out, field("parked", sYellow.Render(age(ctx.now.Sub(run.ParkedSince))+" waiting on an agent decision")))
	}
	if ctx.stall != stallNone {
		out = append(out, field("stuck", sRed.Render(stallExplanation(ctx.stall, ctx.stallIdle))))
	}
	if run.PRURL != "" {
		label := prLabel(run)
		out = append(out, field("pr", prStyle(run.PRState).Render(label)+"  "+sDim.Render(hyperlink(run.PRURL, run.PRURL))))
	}
	if run.Error != "" {
		out = append(out, field("error", sRed.Render(oneLine(run.Error))))
	}

	out = append(out, "", sHeader.Render("STAGES"))
	out = append(out, stageDetailLines(run, ctx)...)

	if findings := findingLines(run, ctx.width); len(findings) > 0 {
		out = append(out, "", sHeader.Render("FINDINGS"))
		out = append(out, findings...)
	}

	if step, ok := run.Current(); ok {
		if line := activity(step, ctx.now); line != "" {
			out = append(out, "", sHeader.Render("LAST ACTIVITY"))
			for _, l := range wrap(line, ctx.width-2) {
				out = append(out, "  "+sDim.Render(l))
			}
		}
	}

	if len(ctx.logLines) > 0 {
		header := "LOG"
		if ctx.logName != "" {
			header += "  (" + ctx.logName + ")"
		}
		out = append(out, "", sHeader.Render(header))
		preview := ctx.logLines
		hidden := 0
		if len(preview) > logPreviewLines {
			hidden = len(preview) - logPreviewLines
			preview = preview[hidden:]
		}
		if hidden > 0 {
			out = append(out, "  "+sDim.Render(fmt.Sprintf("… %d earlier lines, press d for the whole log", hidden)))
		}
		for _, line := range preview {
			out = append(out, "  "+line)
		}
	}

	if intent := strings.TrimSpace(run.Intent); intent != "" {
		header := "INTENT"
		if run.IntentSource != "" {
			header += "  (" + run.IntentSource + ")"
		}
		out = append(out, "", sHeader.Render(header))
		for _, paragraph := range strings.Split(intent, "\n") {
			if strings.TrimSpace(paragraph) == "" {
				out = append(out, "")
				continue
			}
			for _, l := range wrap(paragraph, ctx.width-2) {
				out = append(out, "  "+sText.Render(l))
			}
		}
	}
	return out
}

// stallExplanation says what the warning means in the terms that caused it: an
// agent asking for permission in a worktree with no terminal attached looks
// exactly like a step that is running and quiet.
func stallExplanation(kind stallKind, idle time.Duration) string {
	switch kind {
	case stallGone:
		return "the agent process is gone but the step still says running"
	default:
		return "nothing reported for " + age(idle) + "; the agent may be waiting on a prompt nobody can answer"
	}
}

// stageDetailLines lists every stage with its duration, findings, and the
// evidence that it is alive (agent pid, last activity).
func stageDetailLines(run store.Run, ctx detailContext) []string {
	var out []string
	for _, step := range run.Steps {
		glyph := stageStyle(step.Status).Render(stageGlyph(step.Status, ctx.frame))
		name := pad(step.Name, 10)
		status := pad(step.Status, 18)

		var detail []string
		if d := stepDuration(step, ctx.now); d > 0 {
			detail = append(detail, age(d))
		}
		if typical, ok := ctx.baselines.Lookup(run.RepoPath, step.Name); ok && step.Status == "running" {
			detail = append(detail, "usually "+age(typical))
		}
		if step.FindingCount > 0 {
			f := fmt.Sprintf("%d findings", step.FindingCount)
			if step.NeedsUser > 0 {
				f += fmt.Sprintf(" (%d need you)", step.NeedsUser)
			}
			detail = append(detail, f)
		}
		if step.AgentPID > 0 && step.Status == "running" {
			detail = append(detail, fmt.Sprintf("pid %d", step.AgentPID))
		}
		if step.Error != "" {
			detail = append(detail, oneLine(step.Error))
		}

		line := "  " + glyph + " " + name + stageStyle(step.Status).Render(status)
		if len(detail) > 0 {
			line += sDim.Render(strings.Join(detail, "  ·  "))
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	if len(out) == 0 {
		out = append(out, sDim.Render("  no stages recorded yet"))
	}
	return out
}

// maxFindingsShown caps the findings list; the full text of every finding
// belongs in the log, not in a dashboard.
const maxFindingsShown = 12

// findingLines shows what the pipeline actually flagged, not just how many.
// Findings waiting on a human are listed first, because they are the reason the
// run is not moving.
func findingLines(run store.Run, width int) []string {
	type entry struct {
		step    string
		finding store.Finding
	}
	var needsUser, rest []entry
	for _, step := range run.Steps {
		for _, f := range step.Findings {
			if f.NeedsUser() {
				needsUser = append(needsUser, entry{step: step.Name, finding: f})
				continue
			}
			rest = append(rest, entry{step: step.Name, finding: f})
		}
	}

	all := append(needsUser, rest...)
	if len(all) == 0 {
		return nil
	}

	var out []string
	for i, e := range all {
		if i == maxFindingsShown {
			out = append(out, "  "+sDim.Render(fmt.Sprintf("… %d more", len(all)-maxFindingsShown)))
			break
		}
		style := sDim
		if e.finding.NeedsUser() {
			style = sYellow
		}
		head := "  " + style.Render(pad(actionLabel(e.finding), 9)) +
			sDim.Render(pad(e.step, 9)) +
			sAccent.Render(e.finding.Location())
		out = append(out, strings.TrimRight(head, " "))

		description := oneLine(e.finding.Description)
		if description == "" {
			continue
		}
		if e.finding.Severity != "" {
			description = "[" + e.finding.Severity + "] " + description
		}
		for _, line := range wrap(description, width-6) {
			out = append(out, "      "+sText.Render(line))
		}
	}
	return out
}

// actionLabel names what the pipeline decided to do about a finding. An empty
// action reads as needing a human, which is how no-mistakes itself treats it.
func actionLabel(f store.Finding) string {
	action := strings.TrimSpace(f.Action)
	if action == "" {
		return "ask-user"
	}
	return action
}

// stepDuration is the recorded duration for a finished step, and the elapsed
// time so far for one still running.
func stepDuration(step store.Step, now time.Time) time.Duration {
	if step.Duration > 0 {
		return step.Duration
	}
	if step.Status == "running" && !step.StartedAt.IsZero() {
		return now.Sub(step.StartedAt)
	}
	return 0
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// wrap breaks text at word boundaries to fit width cells.
func wrap(text string, width int) []string {
	if width < 8 {
		width = 8
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		line  strings.Builder
	)
	for _, word := range words {
		wordLen := len([]rune(word))
		if line.Len() == 0 {
			line.WriteString(word)
			continue
		}
		if len([]rune(line.String()))+1+wordLen > width {
			lines = append(lines, line.String())
			line.Reset()
			line.WriteString(word)
			continue
		}
		line.WriteString(" " + word)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

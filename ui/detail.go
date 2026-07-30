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

// detailLines renders the full picture of one run: where it is, what it is
// working on, what it found, and where the PR lives.
func detailLines(run store.Run, frame int, now time.Time, width int) []string {
	labelWidth := 8
	field := func(label, value string) string {
		return sDim.Render(pad(label, labelWidth)) + value
	}

	var out []string
	out = append(out,
		sBold.Render(run.RepoName())+"  "+sAccent.Render(run.Branch),
		"",
		field("run", sDim.Render(run.ID)),
		field("repo", run.RepoPath),
		field("head", shortSHA(run.HeadSHA)),
		field("status", runStyle(run.Status, run.Parked()).Render(run.Status)+sDim.Render("  ·  "+age(runAge(run, now))+" old")),
	)
	if run.Parked() {
		out = append(out, field("parked", sYellow.Render(age(now.Sub(run.ParkedSince))+" waiting on an agent decision")))
	}
	if run.PRURL != "" {
		label := prLabel(run)
		out = append(out, field("pr", prStyle(run.PRState).Render(label)+"  "+sDim.Render(hyperlink(run.PRURL, run.PRURL))))
	}
	if run.Error != "" {
		out = append(out, field("error", sRed.Render(oneLine(run.Error))))
	}

	out = append(out, "", sHeader.Render("STAGES"))
	out = append(out, stageDetailLines(run, frame, now)...)

	if step, ok := run.Current(); ok {
		if line := activity(step, now); line != "" {
			out = append(out, "", sHeader.Render("LAST ACTIVITY"))
			for _, l := range wrap(line, width-2) {
				out = append(out, "  "+sDim.Render(l))
			}
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
			for _, l := range wrap(paragraph, width-2) {
				out = append(out, "  "+sText.Render(l))
			}
		}
	}
	return out
}

// stageDetailLines lists every stage with its duration, findings, and the
// evidence that it is alive (agent pid, last activity).
func stageDetailLines(run store.Run, frame int, now time.Time) []string {
	var out []string
	for _, step := range run.Steps {
		glyph := stageStyle(step.Status).Render(stageGlyph(step.Status, frame))
		name := pad(step.Name, 10)
		status := pad(step.Status, 18)

		var detail []string
		if d := stepDuration(step, now); d > 0 {
			detail = append(detail, age(d))
		}
		if step.Findings > 0 {
			f := fmt.Sprintf("%d findings", step.Findings)
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

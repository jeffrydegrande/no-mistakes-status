package ui

import (
	"sort"
	"strings"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// sortMode orders the run list.
type sortMode int

const (
	// sortUrgency puts what needs you first: parked, then failed, then running.
	sortUrgency sortMode = iota
	sortAge
	sortRepo
)

func (s sortMode) String() string {
	switch s {
	case sortAge:
		return "age"
	case sortRepo:
		return "repo"
	default:
		return "urgency"
	}
}

func (s sortMode) next() sortMode { return (s + 1) % 3 }

// urgency ranks a run by how much it wants your attention. Lower sorts first.
func urgency(run store.Run) int {
	switch {
	case run.Parked():
		return 0
	case run.Status == "failed":
		return 1
	case run.Status == "running":
		return 2
	case run.Status == "pending":
		return 3
	default:
		return 4
	}
}

// sortRuns orders runs in place.
func sortRuns(runs []store.Run, mode sortMode) {
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		switch mode {
		case sortAge:
			return a.CreatedAt.After(b.CreatedAt)
		case sortRepo:
			if a.RepoName() != b.RepoName() {
				return a.RepoName() < b.RepoName()
			}
			return a.Branch < b.Branch
		default:
			if urgency(a) != urgency(b) {
				return urgency(a) < urgency(b)
			}
			return a.CreatedAt.After(b.CreatedAt)
		}
	})
}

// columns holds the computed width of every table column.
type columns struct {
	repo   int
	branch int
	stages int
	step   int
	age    int
	pr     int
}

const (
	gap        = 2
	markerCols = 2
	minBranch  = 12
	minRepo    = 8
	maxRepo    = 18
	stepCols   = 13
	ageCols    = 6
	prCols     = 12
)

// layout divides the terminal width between the columns. Everything except the
// branch is sized to its content; the branch absorbs whatever is left, because
// it is the one column a human can still read half of.
func layout(width, maxSteps, longestRepo int) columns {
	repo := clamp(longestRepo, minRepo, maxRepo)
	stages := maxSteps
	if stages < 1 {
		stages = 1
	}
	fixed := markerCols + repo + gap + stages + gap + stepCols + gap + ageCols + gap + prCols + gap
	branch := width - fixed
	if branch < minBranch {
		branch = minBranch
	}
	return columns{repo: repo, branch: branch, stages: stages, step: stepCols, age: ageCols, pr: prCols}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// maxStepCount is the widest stage strip across the given runs, so every strip
// lines up even when runs come from different no-mistakes versions.
func maxStepCount(runs ...[]store.Run) int {
	most := 0
	for _, group := range runs {
		for _, run := range group {
			if len(run.Steps) > most {
				most = len(run.Steps)
			}
		}
	}
	return most
}

func longestRepoName(runs ...[]store.Run) int {
	most := 0
	for _, group := range runs {
		for _, run := range group {
			if n := len([]rune(run.RepoName())); n > most {
				most = n
			}
		}
	}
	return most
}

func headerRow(cols columns) string {
	parts := []string{
		strings.Repeat(" ", markerCols),
		cell("REPO", cols.repo),
		cell("BRANCH", cols.branch),
		cell("STAGES", cols.stages),
		cell("STEP", cols.step),
		cell("AGE", cols.age),
		cell("PR", cols.pr),
	}
	return sHeader.Render(joinCells(parts))
}

func joinCells(parts []string) string {
	return strings.TrimRight(parts[0]+strings.Join(parts[1:], strings.Repeat(" ", gap)), " ")
}

// renderRow renders one run as a table line.
func renderRow(run store.Run, cols columns, frame int, now time.Time, selected bool) string {
	marker := "  "
	if selected {
		marker = sSelected.Render("▸ ")
	}

	repo := cell(run.RepoName(), cols.repo)
	branch := cell(shortBranch(run.Branch), cols.branch)
	stages := renderStages(run.Steps, frame, cols.stages)
	step := cell(stepLabel(run), cols.step)
	ageCell := cell(age(runAge(run, now)), cols.age)
	pr := prLabel(run)

	repoStyle, branchStyle := sText, sText
	if run.Terminal() {
		repoStyle, branchStyle = sDim, sDim
	}
	if selected {
		branchStyle = sBold.Foreground(colorAccent)
	}

	line := marker +
		repoStyle.Render(repo) + strings.Repeat(" ", gap) +
		branchStyle.Render(branch) + strings.Repeat(" ", gap) +
		stages + strings.Repeat(" ", gap) +
		runStyle(run.Status, run.Parked()).Render(step) + strings.Repeat(" ", gap) +
		sDim.Render(ageCell) + strings.Repeat(" ", gap) +
		prStyle(run.PRState).Render(cell(pr, cols.pr))
	return strings.TrimRight(line, " ")
}

// renderStages colors each stage glyph individually, so one line shows the
// whole pipeline: what is done, what is running, and what has not started.
func renderStages(steps []store.Step, frame, width int) string {
	var b strings.Builder
	for _, s := range steps {
		b.WriteString(stageStyle(s.Status).Render(stageGlyph(s.Status, frame)))
	}
	for i := len(steps); i < width; i++ {
		b.WriteString(" ")
	}
	return b.String()
}

// shortBranch drops a leading "<user>/" prefix, which is the same on every
// branch here and wastes the column.
func shortBranch(branch string) string {
	if i := strings.Index(branch, "/"); i > 0 && i < len(branch)-1 {
		return branch[i+1:]
	}
	return branch
}

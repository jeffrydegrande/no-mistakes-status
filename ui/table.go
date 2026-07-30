package ui

import (
	"path/filepath"
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
	// sortETA puts whatever is closest to finishing first.
	sortETA
	sortAge
	sortRepo
)

var sortNames = map[sortMode]string{
	sortUrgency: "urgency",
	sortETA:     "finishing next",
	sortAge:     "age",
	sortRepo:    "repo",
}

func (s sortMode) String() string { return sortNames[s] }

func (s sortMode) next() sortMode { return (s + 1) % 4 }

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
func sortRuns(runs []store.Run, mode sortMode, baselines store.Baselines, now time.Time) {
	sort.SliceStable(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		switch mode {
		case sortETA:
			left, leftOK := estimate(a, baselines, now)
			right, rightOK := estimate(b, baselines, now)
			// A run with no history to estimate from sorts last: it is unknown,
			// not imminent.
			if leftOK != rightOK {
				return leftOK
			}
			if leftOK && left != right {
				return left < right
			}
			return a.CreatedAt.After(b.CreatedAt)
		case sortAge:
			return a.CreatedAt.After(b.CreatedAt)
		case sortRepo:
			if a.RepoPath != b.RepoPath {
				return a.RepoPath < b.RepoPath
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

// repoLabels names each repository for the table. Two checkouts of the same
// project share a basename, so when that happens the parent directory is added
// to tell them apart; the full path is always on the detail page.
func repoLabels(runs ...[]store.Run) map[string]string {
	byBase := map[string][]string{}
	for _, group := range runs {
		for _, run := range group {
			base := filepath.Base(run.RepoPath)
			if !contains(byBase[base], run.RepoPath) {
				byBase[base] = append(byBase[base], run.RepoPath)
			}
		}
	}
	out := map[string]string{}
	for base, paths := range byBase {
		for _, path := range paths {
			if len(paths) == 1 {
				out[path] = base
				continue
			}
			out[path] = filepath.Join(filepath.Base(filepath.Dir(path)), base)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// columns holds the computed width of every table column, and which optional
// columns fit or have anything to say.
type columns struct {
	repo    int
	branch  int
	stages  int
	step    int
	age     int
	eta     int
	tree    int
	pr      int
	showETA bool
	showTH  bool
}

const (
	gap        = 2
	markerCols = 2
	minBranch  = 12
	minRepo    = 8
	maxRepo    = 22
	stepCols   = 13
	ageCols    = 6
	etaCols    = 8
	treeCols   = 3
	prCols     = 12
	// etaMinWidth is the terminal width below which the estimate column is
	// dropped: the branch name is worth more than the forecast.
	etaMinWidth = 104
)

// layout divides the terminal width between the columns. Everything except the
// branch is sized to its content; the branch absorbs whatever is left, because
// it is the one column a human can still read half of.
func layout(width, maxSteps, longestRepo int, hasTreehouse bool) columns {
	cols := columns{
		repo:    clamp(longestRepo, minRepo, maxRepo),
		stages:  maxInt(maxSteps, 1),
		step:    stepCols,
		age:     ageCols,
		eta:     etaCols,
		tree:    treeCols,
		pr:      prCols,
		showETA: width >= etaMinWidth,
		showTH:  hasTreehouse,
	}

	fixed := markerCols + cols.repo + gap + cols.stages + gap + cols.step + gap + cols.age + gap + cols.pr + gap
	if cols.showETA {
		fixed += cols.eta + gap
	}
	if cols.showTH {
		fixed += cols.tree + gap
	}
	cols.branch = maxInt(width-fixed, minBranch)
	return cols
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

func longestLabel(labels map[string]string) int {
	most := 0
	for _, label := range labels {
		if n := len([]rune(label)); n > most {
			most = n
		}
	}
	return most
}

// rowContext is everything a row needs beyond the run itself.
type rowContext struct {
	cols      columns
	labels    map[string]string
	slots     map[string]string
	stalls    map[string]stallKind
	baselines store.Baselines
	frame     int
	now       time.Time
}

func headerRow(cols columns) string {
	cells := []string{
		strings.Repeat(" ", markerCols),
		cell("REPO", cols.repo),
		cell("BRANCH", cols.branch),
		cell("STAGES", cols.stages),
		cell("STEP", cols.step),
		cell("AGE", cols.age),
	}
	if cols.showETA {
		cells = append(cells, cell("LEFT", cols.eta))
	}
	if cols.showTH {
		cells = append(cells, cell("TH", cols.tree))
	}
	cells = append(cells, cell("PR", cols.pr))
	return sHeader.Render(joinCells(cells))
}

func joinCells(cells []string) string {
	return strings.TrimRight(cells[0]+strings.Join(cells[1:], strings.Repeat(" ", gap)), " ")
}

// renderRow renders one run as a table line.
func renderRow(run store.Run, ctx rowContext, selected bool) string {
	cols := ctx.cols
	marker := "  "
	if selected {
		marker = sSelected.Render("▸ ")
	}

	repoStyle, branchStyle := sText, sText
	if run.Terminal() {
		repoStyle, branchStyle = sDim, sDim
	}
	if selected {
		branchStyle = sBold.Foreground(colorAccent)
	}

	label := ctx.labels[run.RepoPath]
	if label == "" {
		label = run.RepoName()
	}

	cells := []string{
		marker,
		repoStyle.Render(cell(label, cols.repo)),
		branchStyle.Render(cell(shortBranch(run.Branch), cols.branch)),
		renderStages(run.Steps, ctx.frame, cols.stages),
		stepCellStyle(run, ctx.stalls[run.ID]).Render(cell(stepCellText(run, ctx.stalls[run.ID]), cols.step)),
		sDim.Render(cell(age(runAge(run, ctx.now)), cols.age)),
	}
	if cols.showETA {
		cells = append(cells, etaStyle(run).Render(cell(etaCellText(run, ctx.stalls[run.ID], ctx.baselines, ctx.now), cols.eta)))
	}
	if cols.showTH {
		cells = append(cells, sDim.Render(cell(ctx.slots[run.ID], cols.tree)))
	}
	cells = append(cells, prStyle(run.PRState).Render(cell(prLabel(run), cols.pr)))

	return joinCells(cells)
}

// stepCellText flags a wedged run right where you read its progress.
func stepCellText(run store.Run, stall stallKind) string {
	if stall == stallNone {
		return stepLabel(run)
	}
	return stepLabel(run) + " ⚠"
}

func stepCellStyle(run store.Run, stall stallKind) lipglossStyle {
	if stall != stallNone {
		return sRed
	}
	return runStyle(run.Status, run.Parked())
}

// etaStyle keeps "waiting on you" visually distinct from a countdown.
func etaStyle(run store.Run) lipglossStyle {
	if run.Parked() {
		return sYellow
	}
	return sDim
}

// etaCellText replaces the countdown for a wedged run: it is not going to
// finish in any amount of time, so a forecast would be a lie.
func etaCellText(run store.Run, stall stallKind, baselines store.Baselines, now time.Time) string {
	if stall != stallNone {
		return "stuck"
	}
	return etaLabel(run, baselines, now)
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

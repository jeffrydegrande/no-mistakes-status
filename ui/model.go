package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// Reader is the snapshot source. The TUI depends on the interface, not the
// database, so the whole view layer is testable with a fake.
type Reader interface {
	Read(ctx context.Context, now time.Time, recentWindow time.Duration, recentLimit int) (store.Snapshot, error)
}

// Options configures the dashboard.
type Options struct {
	Interval     time.Duration
	RecentWindow time.Duration
	RecentLimit  int
	DBPath       string
	// Notify sends a desktop notification when a run parks or fails.
	Notify bool
	// Treehouse annotates runs with the worktree slot driving them.
	Treehouse bool
	// StallAfter is how long a running step may report nothing before it is
	// flagged as stuck. Zero uses DefaultStallAfter.
	StallAfter time.Duration
	// Now is injectable so tests get stable output.
	Now func() time.Time
}

// frameInterval drives the running-stage animation. It is deliberately much
// faster than the database poll: the spinner is what tells you the dashboard is
// alive between refreshes.
const frameInterval = 250 * time.Millisecond

type view int

const (
	viewList view = iota
	viewDetail
	viewLog
	viewHelp
)

// Model is the bubbletea model for the whole dashboard.
type Model struct {
	reader   Reader
	opts     Options
	notifier *notifier

	snap    store.Snapshot
	runs    []store.Run // filtered, sorted, active then recent
	nActive int
	labels  map[string]string
	slots   map[string]string
	stalls  map[string]stallKind

	cursor       int
	sort         sortMode
	view         view
	detailScroll int
	logScroll    int
	frame        int

	// filterParked hides everything that is not waiting on a decision.
	filterParked bool
	// filterRepo, when set, limits the list to one repository path.
	filterRepo string

	logRunID string
	logName  string
	logLines []string
	logErr   error

	width  int
	height int

	compact    bool // one-shot render: no key hints
	err        error
	status     string
	statusTime time.Time
	lastOK     time.Time
}

// New builds the dashboard model.
func New(reader Reader, opts Options) Model {
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}
	if opts.RecentWindow <= 0 {
		opts.RecentWindow = 6 * time.Hour
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return Model{
		reader:   reader,
		opts:     opts,
		notifier: newNotifier(opts.Notify),
		width:    100,
		height:   30,
		labels:   map[string]string{},
		slots:    map[string]string{},
		stalls:   map[string]stallKind{},
	}
}

type snapshotMsg struct {
	snap store.Snapshot
	err  error
}

type logMsg struct {
	runID string
	name  string
	lines []string
	err   error
}

type slotsMsg map[string]string

type tickMsg time.Time
type frameMsg time.Time

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.refresh(), tickEvery(m.opts.Interval), frameEvery())
}

func (m Model) refresh() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		snap, err := m.reader.Read(ctx, m.opts.Now(), m.opts.RecentWindow, m.opts.RecentLimit)
		return snapshotMsg{snap: snap, err: err}
	}
}

// loadLog reads a run's current log off the UI goroutine. Logs are re-read on
// every refresh rather than followed, because a step hands off to the next one
// and the interesting file changes underneath you.
func loadLog(run store.Run, width int) tea.Cmd {
	step, path, ok := run.LatestLog()
	if !ok {
		return func() tea.Msg { return logMsg{runID: run.ID} }
	}
	return func() tea.Msg {
		raw, err := readTail(path, logTailBytes)
		if err != nil {
			return logMsg{runID: run.ID, name: step.Name, err: err}
		}
		return logMsg{runID: run.ID, name: step.Name, lines: renderLog(raw, width)}
	}
}

// loadSlots asks treehouse which worktree is driving each run.
func loadSlots(runs []store.Run) tea.Cmd {
	if len(runs) == 0 {
		return nil
	}
	dirs := map[string]bool{}
	for _, run := range runs {
		if run.RepoPath != "" {
			dirs[run.RepoPath] = true
		}
	}
	snapshot := append([]store.Run(nil), runs...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		var leases []lease
		for dir := range dirs {
			leases = append(leases, readLeases(ctx, dir)...)
		}
		out := slotsMsg{}
		for _, run := range snapshot {
			if slot := slotFor(run, leases); slot != "" {
				out[run.ID] = slot
			}
		}
		return out
	}
}

func tickEvery(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func frameEvery() tea.Cmd {
	return tea.Tick(frameInterval, func(t time.Time) tea.Msg { return frameMsg(t) })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case frameMsg:
		m.frame++
		return m, frameEvery()

	case tickMsg:
		return m, tea.Batch(m.refresh(), tickEvery(m.opts.Interval))

	case snapshotMsg:
		return m.applySnapshotCmd(msg)

	case logMsg:
		if msg.runID == m.logRunID {
			m.logLines, m.logName, m.logErr = msg.lines, msg.name, msg.err
		}
		return m, nil

	case slotsMsg:
		m.slots = msg
		return m, nil

	case openedMsg:
		if msg.err != nil {
			m.setStatus("open failed: " + msg.err.Error())
		} else {
			m.setStatus("opened " + msg.url)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// applySnapshotCmd stores a snapshot and returns the follow-up work it implies:
// notifications for what changed, a log read for the selected run, and a
// treehouse lookup.
func (m Model) applySnapshotCmd(msg snapshotMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.err = msg.err
		return m, nil
	}
	m.stalls = m.detectStalls(msg.snap)
	events := m.notifier.events(msg.snap, m.stalls)
	m = m.applySnapshot(msg)

	cmds := []tea.Cmd{notify(events)}
	if run, ok := m.selected(); ok {
		m.logRunID = run.ID
		cmds = append(cmds, loadLog(run, m.width-6))
	}
	if m.opts.Treehouse {
		cmds = append(cmds, loadSlots(append(append([]store.Run{}, msg.snap.Active...), msg.snap.Recent...)))
	}
	return m, tea.Batch(cmds...)
}

// detectStalls looks for runs whose agent has gone quiet or gone missing. It
// runs once per poll rather than per frame: it costs a syscall per run, and the
// answer cannot change between animation frames anyway.
func (m Model) detectStalls(snap store.Snapshot) map[string]stallKind {
	check := m.stallCheck()
	out := map[string]stallKind{}
	for _, run := range snap.Active {
		if kind, _ := check.classify(run); kind != stallNone {
			out[run.ID] = kind
		}
	}
	return out
}

func (m Model) stallCheck() stallCheck {
	return newStallCheck(m.opts.StallAfter, m.opts.Now())
}

// applySnapshot keeps the cursor on the same run across refreshes. Runs come
// and go every few seconds, so a positional cursor would wander on its own.
func (m Model) applySnapshot(msg snapshotMsg) Model {
	if msg.err != nil {
		m.err = msg.err
		return m
	}
	m.err = nil
	m.lastOK = m.opts.Now()
	m.snap = msg.snap
	return m.rebuild()
}

// rebuild recomputes the display list from the last snapshot. Filters and sort
// go through here, so a keypress reorders the table immediately instead of
// waiting for the next database poll.
func (m Model) rebuild() Model {
	selectedID := m.selectedID()

	active := m.filter(m.snap.Active)
	recent := m.filter(m.snap.Recent)
	sortRuns(active, m.sort, m.snap.Baselines, m.opts.Now())
	sortRuns(recent, sortAge, m.snap.Baselines, m.opts.Now())

	m.nActive = len(active)
	m.runs = append(active, recent...)
	m.labels = repoLabels(m.snap.Active, m.snap.Recent)
	m.cursor = clamp(indexOfRun(m.runs, selectedID), 0, maxInt(len(m.runs)-1, 0))
	return m
}

// filter applies the active view filters, copying so the snapshot stays intact.
func (m Model) filter(runs []store.Run) []store.Run {
	out := make([]store.Run, 0, len(runs))
	for _, run := range runs {
		if m.filterParked && !run.Parked() {
			continue
		}
		if m.filterRepo != "" && run.RepoPath != m.filterRepo {
			continue
		}
		out = append(out, run)
	}
	return out
}

func (m Model) filtering() bool { return m.filterParked || m.filterRepo != "" }

func indexOfRun(runs []store.Run, id string) int {
	if id == "" {
		return 0
	}
	for i, run := range runs {
		if run.ID == id {
			return i
		}
	}
	return 0
}

func (m Model) selectedID() string {
	if run, ok := m.selected(); ok {
		return run.ID
	}
	return ""
}

func (m Model) selected() (store.Run, bool) {
	if m.cursor < 0 || m.cursor >= len(m.runs) {
		return store.Run{}, false
	}
	return m.runs[m.cursor], true
}

func (m *Model) setStatus(msg string) {
	m.status = msg
	m.statusTime = m.opts.Now()
}

// selectionChanged reloads the log when the cursor lands on a different run.
func (m Model) selectionChanged() (Model, tea.Cmd) {
	run, ok := m.selected()
	if !ok || run.ID == m.logRunID {
		return m, nil
	}
	m.logRunID = run.ID
	m.logLines, m.logName, m.logErr = nil, "", nil
	m.logScroll = 0
	return m, loadLog(run, m.width-6)
}

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.view != viewList {
			m.view = viewList
			m.detailScroll, m.logScroll = 0, 0
			return m, nil
		}
		return m, tea.Quit

	case "?":
		if m.view == viewHelp {
			m.view = viewList
		} else {
			m.view = viewHelp
		}
		return m, nil

	case "enter":
		if m.view == viewList && len(m.runs) > 0 {
			m.view = viewDetail
			m.detailScroll = 0
		} else if m.view == viewDetail {
			m.view = viewList
		}
		return m, nil

	case "d":
		run, ok := m.selected()
		if !ok {
			return m, nil
		}
		if _, _, has := run.LatestLog(); !has {
			m.setStatus("no log written for this run yet")
			return m, nil
		}
		m.view = viewLog
		m.logScroll = 1 << 20 // a log opens at the end, where the news is
		m.logRunID = run.ID
		return m, loadLog(run, m.width-6)

	case "p":
		m.filterParked = !m.filterParked
		m = m.rebuild()
		m.setStatus(filterStatus(m))
		return m.selectionChanged()

	case "f":
		if m.filterRepo != "" {
			m.filterRepo = ""
			m = m.rebuild()
			m.setStatus(filterStatus(m))
			return m.selectionChanged()
		}
		run, ok := m.selected()
		if !ok {
			m.setStatus("select a run to filter by its repo")
			return m, nil
		}
		m.filterRepo = run.RepoPath
		m = m.rebuild()
		m.setStatus(filterStatus(m))
		return m.selectionChanged()

	case "up", "k":
		return m.move(-1).selectionChanged()

	case "down", "j":
		return m.move(1).selectionChanged()

	case "pgup":
		return m.move(-10).selectionChanged()

	case "pgdown":
		return m.move(10).selectionChanged()

	case "g", "home":
		switch m.view {
		case viewDetail:
			m.detailScroll = 0
		case viewLog:
			m.logScroll = 0
		default:
			m.cursor = 0
			return m.selectionChanged()
		}
		return m, nil

	case "G", "end":
		switch m.view {
		case viewDetail:
			m.detailScroll = 1 << 20
		case viewLog:
			m.logScroll = 1 << 20
		default:
			m.cursor = maxInt(len(m.runs)-1, 0)
			return m.selectionChanged()
		}
		return m, nil

	case "s":
		m.sort = m.sort.next()
		m.setStatus("sort: " + m.sort.String())
		return m.rebuild(), nil

	case "r":
		m.setStatus("refreshing")
		return m, m.refresh()

	case "o":
		run, ok := m.selected()
		if !ok || run.PRURL == "" {
			m.setStatus("no PR on this run yet")
			return m, nil
		}
		m.setStatus("opening " + prLabel(run))
		return m, openURL(run.PRURL)
	}
	return m, nil
}

func filterStatus(m Model) string {
	var parts []string
	if m.filterParked {
		parts = append(parts, "parked only")
	}
	if m.filterRepo != "" {
		label := m.labels[m.filterRepo]
		if label == "" {
			label = m.filterRepo
		}
		parts = append(parts, "repo "+label)
	}
	if len(parts) == 0 {
		return "filter cleared"
	}
	return "filter: " + strings.Join(parts, " + ")
}

// move steps the cursor in the list, or scrolls whichever page is open.
func (m Model) move(delta int) Model {
	switch m.view {
	case viewDetail:
		m.detailScroll = maxInt(m.detailScroll+delta, 0)
		return m
	case viewLog:
		m.logScroll = maxInt(m.logScroll+delta, 0)
		return m
	}
	if len(m.runs) == 0 {
		return m
	}
	m.cursor = clamp(m.cursor+delta, 0, len(m.runs)-1)
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Model) View() string {
	switch m.view {
	case viewDetail:
		return m.renderDetail()
	case viewLog:
		return m.renderLogView()
	case viewHelp:
		return m.renderHelp()
	default:
		return m.renderList()
	}
}

func (m Model) rowContext() rowContext {
	return rowContext{
		cols: layout(m.width, maxStepCount(m.snap.Active, m.snap.Recent),
			longestLabel(m.labels), len(m.slots) > 0),
		labels:    m.labels,
		slots:     m.slots,
		stalls:    m.stalls,
		baselines: m.snap.Baselines,
		frame:     m.frame,
		now:       m.opts.Now(),
	}
}

func (m Model) renderList() string {
	var b strings.Builder
	b.WriteString(m.renderHeader() + "\n\n")

	if m.err != nil {
		b.WriteString(sRed.Render("  "+m.err.Error()) + "\n")
		b.WriteString(m.renderFooter())
		return b.String()
	}

	if len(m.runs) == 0 {
		if m.filtering() {
			b.WriteString(sDim.Render("  no runs match this filter") + "\n")
			b.WriteString(sDim.Render("  press p or f to clear it") + "\n")
		} else {
			b.WriteString(sDim.Render("  nothing running, nothing finished recently") + "\n")
			b.WriteString(sDim.Render("  push a branch through a gate to start a pipeline") + "\n")
		}
		b.WriteString(m.renderFooter())
		return b.String()
	}

	ctx := m.rowContext()
	b.WriteString(headerRow(ctx.cols) + "\n")

	for i, run := range m.runs {
		if i == 0 && m.nActive > 0 {
			b.WriteString(sSection.Render("  active") + "\n")
		}
		if i == m.nActive {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(sSection.Render("  recent") + "\n")
		}
		b.WriteString(renderRow(run, ctx, i == m.cursor) + "\n")
	}

	b.WriteString(m.renderFooter())
	return b.String()
}

func (m Model) renderHeader() string {
	parked := 0
	for _, run := range m.snap.Active {
		if run.Parked() {
			parked++
		}
	}
	left := sBold.Render("no-mistakes") + sDim.Render("  ·  ") +
		fmt.Sprintf("%d active", len(m.snap.Active))
	if parked > 0 {
		left += sDim.Render("  ·  ") + sYellow.Render(fmt.Sprintf("%d parked", parked))
	}
	if len(m.stalls) > 0 {
		left += sDim.Render("  ·  ") + sRed.Render(fmt.Sprintf("%d stuck", len(m.stalls)))
	}
	if len(m.snap.Recent) > 0 {
		left += sDim.Render(fmt.Sprintf("  ·  %d recent", len(m.snap.Recent)))
	}
	if m.filtering() {
		left += sDim.Render("  ·  ") + sAccent.Render(filterStatus(m))
	}
	return "  " + left
}

func (m Model) renderFooter() string {
	if m.compact {
		return ""
	}
	var parts []string
	if !m.lastOK.IsZero() {
		parts = append(parts, "updated "+age(m.opts.Now().Sub(m.lastOK))+" ago")
	}
	if m.sort != sortUrgency {
		parts = append(parts, "sort "+m.sort.String())
	}
	if m.status != "" && m.opts.Now().Sub(m.statusTime) < 4*time.Second {
		parts = append(parts, m.status)
	}
	line := "\n  " + sDim.Render("↑↓ move   enter detail   d log   o PR   p parked   f repo   s sort   ? help   q quit")
	if len(parts) > 0 {
		line += "\n  " + sDim.Render(strings.Join(parts, "   ·   "))
	}
	return line
}

// page renders a scrollable body, clamping the offset so the end of the content
// is the furthest you can scroll.
func page(lines []string, scroll, height int) (visible []string, start, below int) {
	body := maxInt(height, 3)
	start = clamp(scroll, 0, maxInt(len(lines)-body, 0))
	end := minInt(start+body, len(lines))
	return lines[start:end], start, len(lines) - end
}

func (m Model) renderDetail() string {
	run, ok := m.selected()
	if !ok {
		return m.renderList()
	}
	lines := detailLines(run, detailContext{
		frame:     m.frame,
		now:       m.opts.Now(),
		width:     m.width - 4,
		baselines: m.snap.Baselines,
		slot:      m.slots[run.ID],
		stall:     m.stalls[run.ID],
		stallIdle: m.stallIdle(run),
		logLines:  m.logLines,
		logName:   m.logName,
	})

	visible, start, below := page(lines, m.detailScroll, m.height-6)

	var b strings.Builder
	b.WriteString("  " + sDim.Render("detail") + "\n\n")
	for _, line := range visible {
		b.WriteString("  " + line + "\n")
	}
	if below > 0 {
		b.WriteString("\n  " + sDim.Render(fmt.Sprintf("%d more lines below", below)))
	} else if start > 0 {
		b.WriteString("\n  " + sDim.Render("end"))
	}
	b.WriteString("\n  " + sDim.Render("↑↓ scroll   d log   o PR   esc back   q quit"))
	return b.String()
}

// stallIdle is how long the selected run has been quiet, for the detail page.
func (m Model) stallIdle(run store.Run) time.Duration {
	_, idle := m.stallCheck().classify(run)
	return idle
}

func (m Model) renderLogView() string {
	run, ok := m.selected()
	if !ok {
		return m.renderList()
	}

	title := "  " + sBold.Render(run.RepoName()) + sDim.Render("  ·  ") + sAccent.Render(shortBranch(run.Branch))
	if m.logName != "" {
		title += sDim.Render("  ·  " + m.logName + ".log")
	}

	var b strings.Builder
	b.WriteString(title + "\n\n")

	switch {
	case m.logErr != nil:
		b.WriteString("  " + sRed.Render(m.logErr.Error()) + "\n")
	case len(m.logLines) == 0:
		b.WriteString("  " + sDim.Render("nothing logged yet") + "\n")
	default:
		visible, _, below := page(m.logLines, m.logScroll, m.height-6)
		for _, line := range visible {
			b.WriteString("  " + line + "\n")
		}
		if below > 0 {
			b.WriteString("\n  " + sDim.Render(fmt.Sprintf("%d more lines below", below)))
		}
	}

	b.WriteString("\n  " + sDim.Render("↑↓ scroll   g top   G end   esc back   q quit"))
	return b.String()
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"↑ / k, ↓ / j", "move between runs"},
		{"pgup / pgdown", "move ten at a time"},
		{"g / G", "jump to first / last"},
		{"enter", "open the selected run's detail"},
		{"d", "read the run's log, formatted"},
		{"o", "open the run's pull request in a browser"},
		{"p", "show only runs waiting on a decision"},
		{"f", "show only the selected run's repo"},
		{"s", "cycle sort: urgency, finishing next, age, repo"},
		{"r", "refresh now"},
		{"esc", "back to the list"},
		{"q", "quit"},
	}
	legend := [][2]string{
		{stageStyle("completed").Render(glyphDone), "done"},
		{stageStyle("running").Render(runningFrames[0]), "running"},
		{stageStyle("fixing").Render(glyphFixing), "fixing"},
		{stageStyle("awaiting_approval").Render(glyphParked), "parked, waiting on a decision"},
		{stageStyle("failed").Render(glyphFailed), "failed"},
		{stageStyle("skipped").Render(glyphSkipped), "skipped"},
		{sDim.Render(glyphPending), "not started"},
	}

	var b strings.Builder
	b.WriteString("  " + sBold.Render("keys") + "\n\n")
	for _, row := range rows {
		b.WriteString("  " + sAccent.Render(pad(row[0], 16)) + sText.Render(row[1]) + "\n")
	}
	b.WriteString("\n  " + sBold.Render("stages") + "\n\n")
	for _, row := range legend {
		b.WriteString("  " + row[0] + "  " + sText.Render(row[1]) + "\n")
	}
	b.WriteString("\n  " + sBold.Render("the LEFT column") + "\n\n")
	b.WriteString("  " + sText.Render("an estimate from this machine's own finished runs: what the") + "\n")
	b.WriteString("  " + sText.Render("current step still owes, plus the usual cost of the rest") + "\n")
	b.WriteString("\n  " + sDim.Render("reading "+m.opts.DBPath+" read-only, every "+m.opts.Interval.String()))
	if m.opts.Notify {
		b.WriteString("\n  " + sDim.Render("desktop notifications on: a run parking or failing"))
	}
	b.WriteString("\n  " + sDim.Render("esc or ? to go back"))
	return b.String()
}

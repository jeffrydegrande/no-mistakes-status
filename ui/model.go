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
	viewHelp
)

// Model is the bubbletea model for the whole dashboard.
type Model struct {
	reader Reader
	opts   Options

	snap    store.Snapshot
	runs    []store.Run // active then recent, in display order
	nActive int

	cursor       int
	sort         sortMode
	view         view
	detailScroll int
	frame        int

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
	return Model{reader: reader, opts: opts, width: 100, height: 30}
}

type snapshotMsg struct {
	snap store.Snapshot
	err  error
}

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
		return m.applySnapshot(msg), nil

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

// applySnapshot keeps the cursor on the same run across refreshes. Runs come
// and go every few seconds, so a positional cursor would wander on its own.
func (m Model) applySnapshot(msg snapshotMsg) Model {
	if msg.err != nil {
		m.err = msg.err
		return m
	}
	selectedID := m.selectedID()
	m.err = nil
	m.lastOK = m.opts.Now()
	m.snap = msg.snap

	active := append([]store.Run(nil), msg.snap.Active...)
	recent := append([]store.Run(nil), msg.snap.Recent...)
	sortRuns(active, m.sort)
	sortRuns(recent, sortAge)

	m.nActive = len(active)
	m.runs = append(active, recent...)
	m.cursor = indexOfRun(m.runs, selectedID)
	if m.cursor >= len(m.runs) {
		m.cursor = len(m.runs) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	return m
}

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

func (m Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit

	case "esc":
		if m.view != viewList {
			m.view = viewList
			m.detailScroll = 0
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

	case "up", "k":
		return m.move(-1), nil

	case "down", "j":
		return m.move(1), nil

	case "pgup":
		return m.move(-10), nil

	case "pgdown":
		return m.move(10), nil

	case "g", "home":
		if m.view == viewDetail {
			m.detailScroll = 0
			return m, nil
		}
		m.cursor = 0
		return m, nil

	case "G", "end":
		if m.view == viewDetail {
			m.detailScroll = 1 << 20
			return m, nil
		}
		m.cursor = maxInt(len(m.runs)-1, 0)
		return m, nil

	case "s":
		m.sort = m.sort.next()
		m.setStatus("sort: " + m.sort.String())
		return m.applySnapshot(snapshotMsg{snap: m.snap}), nil

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

// move steps the cursor in the list, or scrolls the detail page.
func (m Model) move(delta int) Model {
	if m.view == viewDetail {
		m.detailScroll = maxInt(m.detailScroll+delta, 0)
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

func (m Model) View() string {
	switch m.view {
	case viewDetail:
		return m.renderDetail()
	case viewHelp:
		return m.renderHelp()
	default:
		return m.renderList()
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

	cols := layout(m.width, maxStepCount(m.snap.Active, m.snap.Recent), longestRepoName(m.snap.Active, m.snap.Recent))
	now := m.opts.Now()

	if len(m.runs) == 0 {
		b.WriteString(sDim.Render("  nothing running, nothing finished recently") + "\n")
		b.WriteString(sDim.Render("  push a branch through a gate to start a pipeline") + "\n")
		b.WriteString(m.renderFooter())
		return b.String()
	}

	b.WriteString(headerRow(cols) + "\n")

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
		b.WriteString(renderRow(run, cols, m.frame, now, i == m.cursor) + "\n")
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
	if len(m.snap.Recent) > 0 {
		left += sDim.Render(fmt.Sprintf("  ·  %d recent", len(m.snap.Recent)))
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
	line := "\n  " + sDim.Render("↑↓ move   enter detail   o open PR   s sort   r refresh   ? help   q quit")
	if len(parts) > 0 {
		line += "\n  " + sDim.Render(strings.Join(parts, "   ·   "))
	}
	return line
}

func (m Model) renderDetail() string {
	run, ok := m.selected()
	if !ok {
		return m.renderList()
	}
	lines := detailLines(run, m.frame, m.opts.Now(), m.width-4)

	// Two lines of chrome at the top, three at the bottom.
	body := maxInt(m.height-6, 5)
	if m.detailScroll > maxInt(len(lines)-body, 0) {
		m.detailScroll = maxInt(len(lines)-body, 0)
	}
	end := minInt(m.detailScroll+body, len(lines))

	var b strings.Builder
	b.WriteString("  " + sDim.Render("detail") + "\n\n")
	for _, line := range lines[m.detailScroll:end] {
		b.WriteString("  " + line + "\n")
	}
	more := len(lines) - end
	if more > 0 {
		b.WriteString("\n  " + sDim.Render(fmt.Sprintf("%d more lines below", more)))
	}
	b.WriteString("\n  " + sDim.Render("↑↓ scroll   o open PR   esc back   q quit"))
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m Model) renderHelp() string {
	rows := [][2]string{
		{"↑ / k, ↓ / j", "move between runs"},
		{"pgup / pgdown", "move ten at a time"},
		{"g / G", "jump to first / last"},
		{"enter", "open the selected run's detail"},
		{"o", "open the run's pull request in a browser"},
		{"s", "cycle sort: urgency, age, repo"},
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
	b.WriteString("\n  " + sDim.Render("reading "+m.opts.DBPath+" read-only, every "+m.opts.Interval.String()))
	b.WriteString("\n  " + sDim.Render("esc or ? to go back"))
	return b.String()
}

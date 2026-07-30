package ui

import (
	"os/exec"
	"runtime"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// Notification is one thing worth interrupting you for.
type Notification struct {
	RunID string
	Title string
	Body  string
}

// runSignal is the part of a run's state that can trigger a notification.
type runSignal struct {
	parked  bool
	stalled bool
	status  string
}

// notifier turns successive snapshots into notifications. It only ever fires on
// a transition, so a run that stays parked for an hour interrupts you once.
type notifier struct {
	enabled bool
	seen    map[string]runSignal
	primed  bool
}

func newNotifier(enabled bool) *notifier {
	return &notifier{enabled: enabled, seen: map[string]runSignal{}}
}

// events diffs a snapshot against the last one. The first snapshot only records
// state: starting the dashboard with six runs in flight must not fire six
// notifications for things you already knew about.
func (n *notifier) events(snap store.Snapshot, stalls map[string]stallKind) []Notification {
	current := map[string]runSignal{}
	var out []Notification

	for _, run := range append(append([]store.Run{}, snap.Active...), snap.Recent...) {
		signal := runSignal{parked: run.Parked(), stalled: stalls[run.ID] != stallNone, status: run.Status}
		current[run.ID] = signal
		if !n.primed {
			continue
		}
		previous, known := n.seen[run.ID]
		if known && previous == signal {
			continue
		}
		if signal.parked && !previous.parked {
			out = append(out, Notification{
				RunID: run.ID,
				Title: run.RepoName() + " is waiting on you",
				Body:  stepLabel(run) + " · " + run.Branch,
			})
			continue
		}
		if signal.status == "failed" && previous.status != "failed" {
			out = append(out, Notification{
				RunID: run.ID,
				Title: run.RepoName() + " pipeline failed",
				Body:  stepLabel(run) + " · " + run.Branch,
			})
			continue
		}
		// A wedged agent is the one case where nothing at all will happen
		// until you look, so it is worth the same interruption.
		if signal.stalled && !previous.stalled {
			out = append(out, Notification{
				RunID: run.ID,
				Title: run.RepoName() + " looks stuck",
				Body:  stepLabel(run) + " " + stalls[run.ID].String() + " · " + run.Branch,
			})
		}
	}

	// Replace rather than merge, so runs that age out of the window stop
	// consuming memory for the life of the process.
	n.seen = current
	n.primed = true
	if !n.enabled {
		return nil
	}
	return out
}

// notifyCommand builds the platform's desktop-notification command. It is a
// variable so tests can capture the invocation instead of popping a toast.
var notifyCommand = func(title, body string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		script := "display notification " + osaQuote(body) + " with title " + osaQuote(title)
		return exec.Command("osascript", "-e", script)
	case "linux":
		return exec.Command("notify-send", "--app-name=nms", title, body)
	default:
		return nil
	}
}

// osaQuote wraps a string as an AppleScript literal. Findings and branch names
// are arbitrary text, and an unescaped quote would turn a notification into a
// syntax error at best.
func osaQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return `"` + s + `"`
}

// NotificationsAvailable reports whether desktop notifications work here.
func NotificationsAvailable() bool { return notificationsAvailable() }

// notificationsAvailable reports whether this machine can show desktop
// notifications at all. When it cannot, the feature turns itself off rather
// than reporting an error on every state change.
func notificationsAvailable() bool {
	cmd := notifyCommand("probe", "probe")
	if cmd == nil {
		return false
	}
	_, err := exec.LookPath(cmd.Path)
	if err == nil {
		return true
	}
	_, err = exec.LookPath(cmd.Args[0])
	return err == nil
}

// notify sends notifications in the background. A notification is a courtesy:
// if the system tool fails, the dashboard says nothing and carries on.
func notify(events []Notification) tea.Cmd {
	if len(events) == 0 {
		return nil
	}
	return func() tea.Msg {
		for _, event := range events {
			cmd := notifyCommand(event.Title, event.Body)
			if cmd == nil {
				return nil
			}
			if err := cmd.Start(); err != nil {
				continue
			}
			go func(c *exec.Cmd) { _ = c.Wait() }(cmd)
		}
		return nil
	}
}

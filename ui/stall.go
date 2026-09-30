package ui

import (
	"os"
	"runtime"
	"syscall"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// A pipeline step runs an agent in a worktree with no terminal attached. When
// that agent stops to ask for permission, nobody is there to answer: the step
// stays "running", the process stays alive, and nothing is written to the log
// ever again. From the daemon's point of view it is working. From yours it is
// dead, and you find out an hour later.
//
// Two shapes of the same problem are detectable from what is already recorded:
//
//   - silent: the step is running but has reported nothing for a long time.
//   - gone: the step recorded an agent pid, and that process no longer exists.
//
// Neither is proven trouble. A test suite can be genuinely slow and quiet. So
// this drives a warning and a notification, never an action.

// DefaultStallAfter is how long a running step may say nothing before it is
// called out. Long enough to sit through a slow build, short enough that a
// permission prompt does not eat your afternoon.
const DefaultStallAfter = 8 * time.Minute

// stallKind is why a run is being flagged.
type stallKind int

const (
	stallNone stallKind = iota
	stallSilent
	stallGone
)

func (s stallKind) String() string {
	switch s {
	case stallSilent:
		return "silent"
	case stallGone:
		return "agent gone"
	default:
		return ""
	}
}

// stallCheck evaluates runs against a threshold.
type stallCheck struct {
	after time.Duration
	now   time.Time
	// alive reports whether a process id is still running. Injectable so the
	// test suite does not depend on what happens to be running on the machine.
	alive func(pid int) bool
}

func newStallCheck(after time.Duration, now time.Time) stallCheck {
	if after <= 0 {
		after = DefaultStallAfter
	}
	return stallCheck{after: after, now: now, alive: processAlive}
}

// classify reports whether a run looks wedged, and why.
func (s stallCheck) classify(run store.Run) (stallKind, time.Duration) {
	if run.Terminal() || run.Parked() {
		return stallNone, 0
	}
	step, ok := run.Current()
	if !ok || step.Status != "running" {
		return stallNone, 0
	}

	// A recorded pid that no longer exists is not a slow step, it is a step
	// whose agent died or was killed out from under the daemon.
	if step.AgentPID > 0 && !s.alive(step.AgentPID) {
		return stallGone, s.idle(step)
	}

	idle := s.idle(step)
	if idle > s.after {
		return stallSilent, idle
	}
	return stallNone, 0
}

// idle is how long the current step has said nothing. A step that has never
// reported falls back to how long it has been running, so an agent that wedges
// before its first output is still caught.
func (s stallCheck) idle(step store.Step) time.Duration {
	switch {
	case !step.LastActivityAt.IsZero():
		return s.now.Sub(step.LastActivityAt)
	case !step.StartedAt.IsZero():
		return s.now.Sub(step.StartedAt)
	default:
		return 0
	}
}

// processAlive reports whether a process id is still running. Signal 0 performs
// the permission and existence checks without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if runtime.GOOS == "windows" {
		// No cheap equivalent here, and claiming an agent is gone when it is
		// not would be worse than staying quiet.
		return true
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

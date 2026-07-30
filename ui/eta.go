package ui

import (
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// estimate answers "how much longer", built from this machine's own history:
// the time the current step still owes against its usual duration, plus the
// usual duration of every step after it.
//
// It is an estimate, not a promise. A step with no history contributes nothing,
// so an estimate is a floor rather than a guess, and a run with no history at
// all reports nothing instead of a made-up number.
func estimate(run store.Run, baselines store.Baselines, now time.Time) (time.Duration, bool) {
	if run.Terminal() || len(run.Steps) == 0 {
		return 0, false
	}

	current, ok := run.Current()
	if !ok {
		return 0, false
	}

	var (
		total    time.Duration
		grounded bool
		passed   bool
	)
	for _, step := range run.Steps {
		if step.Order == current.Order {
			passed = true
			typical, known := baselines.Lookup(run.RepoPath, step.Name)
			if !known {
				continue
			}
			grounded = true
			total += remaining(step, typical, now)
			continue
		}
		if !passed {
			continue
		}
		if step.Status != "pending" {
			continue
		}
		if typical, known := baselines.Lookup(run.RepoPath, step.Name); known {
			grounded = true
			total += typical
		}
	}
	if !grounded {
		return 0, false
	}
	return total, true
}

// remaining is what a running step still owes. A step already past its usual
// duration owes nothing more we can predict, so it floors at zero rather than
// going negative and pulling the whole estimate down.
func remaining(step store.Step, typical time.Duration, now time.Time) time.Duration {
	if step.StartedAt.IsZero() {
		return typical
	}
	elapsed := now.Sub(step.StartedAt)
	if elapsed >= typical {
		return 0
	}
	return typical - elapsed
}

// etaLabel renders an estimate for the table. A parked run is not waiting on
// time, it is waiting on you, so it never shows a countdown.
func etaLabel(run store.Run, baselines store.Baselines, now time.Time) string {
	if run.Parked() {
		return "you"
	}
	d, ok := estimate(run, baselines, now)
	if !ok {
		return "-"
	}
	if d == 0 {
		return "any time"
	}
	return "~" + age(d)
}

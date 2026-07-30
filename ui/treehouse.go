package ui

import (
	"context"
	"os/exec"
	"strings"
	"time"

	"github.com/jeffrydegrande/no-mistakes-status/store"
)

// Treehouse keeps a pool of pre-warmed git worktrees so several agents can work
// on one repo at once, and leases each worktree to a named task. A pipeline run
// does not record which worktree drove it, but the lease name and the branch
// name both come from the same task, so the two can be matched.
//
// This is best-effort decoration: when treehouse is absent, or the lease names
// do not resemble the branch names, the column simply stays empty.

// lease is one worktree in the pool.
type lease struct {
	slot   string
	holder string
}

// treehouseInterval is how often to re-read the pool. Leases change when an
// agent picks up or finishes a task, which is minutes apart, and the status
// command walks every worktree's process tree.
const treehouseInterval = 30 * time.Second

// treehouseCommand runs the pool status for one repository. It is a variable so
// tests do not need treehouse installed.
var treehouseCommand = func(ctx context.Context, dir string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "treehouse", "status")
	cmd.Dir = dir
	return cmd
}

// TreehouseAvailable reports whether the worktree pool manager is installed.
func TreehouseAvailable() bool { return treehouseAvailable() }

// treehouseAvailable reports whether the pool manager is installed.
func treehouseAvailable() bool {
	_, err := exec.LookPath("treehouse")
	return err == nil
}

// readLeases returns the leased worktrees for one repository, keyed by nothing
// in particular: callers match them against branches themselves.
func readLeases(ctx context.Context, dir string) []lease {
	out, err := treehouseCommand(ctx, dir).Output()
	if err != nil {
		return nil
	}
	return parseLeases(string(out))
}

// parseLeases reads `treehouse status` output. Lines look like:
//
//	5     leased       ~/.treehouse/nova-go-95b98f/5/nova-go  (held by NGO-167)
//
// with indented process lists underneath, which carry no slot and are skipped.
func parseLeases(out string) []lease {
	var leases []lease
	for _, line := range strings.Split(out, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "leased" {
			continue
		}
		holder := ""
		if i := strings.Index(line, "(held by "); i >= 0 {
			holder = strings.TrimSuffix(strings.TrimSpace(line[i+len("(held by "):]), ")")
		}
		if holder == "" {
			continue
		}
		leases = append(leases, lease{slot: fields[0], holder: holder})
	}
	return leases
}

// slotFor matches a run to the worktree slot that is driving it, by looking for
// the lease holder's name inside the branch name. Holders are task keys like
// "NGO-167" and branches are "jeffrydegrande/ngo-167-some-title", so the match
// is case-insensitive and bounded by a non-alphanumeric character to keep
// "NGO-16" from matching "ngo-167".
func slotFor(run store.Run, leases []lease) string {
	branch := strings.ToLower(run.Branch)
	for _, l := range leases {
		holder := strings.ToLower(strings.TrimSpace(l.holder))
		if holder == "" {
			continue
		}
		if matchesToken(branch, holder) {
			return l.slot
		}
	}
	return ""
}

func matchesToken(haystack, needle string) bool {
	for i := 0; ; {
		j := strings.Index(haystack[i:], needle)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(needle)
		if end >= len(haystack) || !isAlphanumeric(haystack[end]) {
			return true
		}
		i = end
	}
}

func isAlphanumeric(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

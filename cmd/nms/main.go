// Command nms is a read-only dashboard for every no-mistakes pipeline run on
// this machine.
//
// no-mistakes itself is repo-scoped: `status` and `runs` only ever show the
// repository you are standing in. One daemon drives every repo, so this reads
// its state database directly and shows all of them at once.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jeffrydegrande/no-mistakes-status/store"
	"github.com/jeffrydegrande/no-mistakes-status/ui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dbPath   = flag.String("db", "", "path to state.sqlite (default: $NM_HOME/state.sqlite or ~/.no-mistakes/state.sqlite)")
		interval = flag.Duration("interval", 5*time.Second, "how often to re-read the database")
		recent   = flag.Duration("recent", 6*time.Hour, "how far back to list finished runs")
		limit    = flag.Int("recent-limit", 10, "maximum finished runs to list (0 for no limit)")
		once     = flag.Bool("once", false, "print the table once and exit, for scripts and non-interactive shells")
	)
	flag.Parse()

	path := *dbPath
	if path == "" {
		resolved, err := store.DefaultPath()
		if err != nil {
			return err
		}
		path = resolved
	}

	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := st.Probe(ctx); err != nil {
		return err
	}

	opts := ui.Options{
		Interval:     *interval,
		RecentWindow: *recent,
		RecentLimit:  *limit,
		DBPath:       path,
	}

	if *once {
		return ui.PrintOnce(ctx, os.Stdout, st, opts)
	}

	model := ui.New(st, opts)
	program := tea.NewProgram(model, tea.WithAltScreen())
	_, err = program.Run()
	return err
}

package ui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// defaultWidth is used when nothing tells us how wide the terminal is, which is
// the normal case when output is piped.
const defaultWidth = 120

// PrintOnce renders the table a single time and returns. Lipgloss strips color
// automatically when the writer is not a terminal, so this is pipe-friendly.
func PrintOnce(ctx context.Context, w io.Writer, reader Reader, opts Options) error {
	model := New(reader, opts)
	model.width = detectWidth()
	model.compact = true

	snap, err := reader.Read(ctx, model.opts.Now(), model.opts.RecentWindow, model.opts.RecentLimit)
	if err != nil {
		return err
	}
	model = model.applySnapshot(snapshotMsg{snap: snap})
	model.lastOK = time.Time{} // "updated 0s ago" is noise in a one-shot dump.
	_, err = fmt.Fprintln(w, model.renderList())
	return err
}

func detectWidth() int {
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 40 {
			return n
		}
	}
	return defaultWidth
}

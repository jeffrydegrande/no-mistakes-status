package ui

import (
	"fmt"
	"os/exec"
	"runtime"

	tea "github.com/charmbracelet/bubbletea"
)

type openedMsg struct {
	url string
	err error
}

// openCommand is the platform's "open this in the default app" command. It is a
// variable so tests can swap it for something harmless.
var openCommand = func(url string) *exec.Cmd {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url)
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return exec.Command("xdg-open", url)
	}
}

// openURL opens a URL in the user's browser without blocking the UI. The child
// is released rather than waited on: a browser can outlive this dashboard.
func openURL(url string) tea.Cmd {
	return func() tea.Msg {
		// The URL comes out of a database this program does not own, so check
		// it is an http(s) URL before handing it to a system opener.
		if err := plainURL(url); err != nil {
			return openedMsg{url: url, err: err}
		}
		cmd := openCommand(url)
		if err := cmd.Start(); err != nil {
			return openedMsg{url: url, err: err}
		}
		go func() { _ = cmd.Wait() }()
		return openedMsg{url: url}
	}
}

// plainURL is a guard against handing the shell something that is not a URL.
func plainURL(url string) error {
	if len(url) < 8 || (url[:7] != "http://" && url[:8] != "https://") {
		return fmt.Errorf("not an http url: %q", url)
	}
	return nil
}

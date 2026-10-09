//go:build unix

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// terminalWidth is the column count of the terminal behind f, or 0 when f is
// not a terminal.
func terminalWidth(f *os.File) int {
	ws, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0
	}
	return int(ws.Col)
}

// isTerminal reports whether f is a terminal at all, whatever its size.
func isTerminal(f *os.File) bool {
	_, err := unix.IoctlGetWinsize(int(f.Fd()), unix.TIOCGWINSZ)
	return err == nil
}

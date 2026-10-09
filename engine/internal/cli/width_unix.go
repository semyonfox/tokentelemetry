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

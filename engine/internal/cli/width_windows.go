//go:build windows

package cli

import (
	"os"

	"golang.org/x/sys/windows"
)

// terminalWidth is the column count of the console behind f, or 0 when f is
// not a console.
func terminalWidth(f *os.File) int {
	var info windows.ConsoleScreenBufferInfo
	if err := windows.GetConsoleScreenBufferInfo(windows.Handle(f.Fd()), &info); err != nil {
		return 0
	}
	return int(info.Window.Right-info.Window.Left) + 1
}

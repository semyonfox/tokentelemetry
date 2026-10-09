package cli

import (
	"io"
	"os"
	"strconv"
)

// outputWidth is how many terminal cells a line written to w may use, or 0
// when that is unknown. Output going to a pipe or file is never fitted.
// COLUMNS wins when set, so a user or a test can force a width; otherwise the
// terminal is asked directly, since shells keep COLUMNS to themselves rather
// than exporting it.
func outputWidth(w io.Writer) int {
	if n, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && n > 0 {
		return n
	}
	if f, ok := w.(*os.File); ok {
		return terminalWidth(f)
	}
	return 0
}

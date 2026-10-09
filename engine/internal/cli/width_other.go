//go:build !unix && !windows

package cli

import "os"

func terminalWidth(*os.File) int { return 0 }

func isTerminal(*os.File) bool { return false }

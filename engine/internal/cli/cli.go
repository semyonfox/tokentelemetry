// Package cli implements the TokenTelemetry command line.
package cli

import (
	"fmt"
	"os"
	"strings"
)

// Version is stamped at build time with -ldflags.
var Version = "dev"

// Main runs the CLI and returns a process exit code.
func Main(args []string) int {
	if len(args) < 2 {
		return cmdReport("summary", nil)
	}
	name, rest := args[1], args[2:]
	switch name {
	case "help", "--help", "-h":
		if len(rest) == 0 {
			printHelp("")
			return 0
		}
		if len(rest) != 1 {
			return commandError("help", fmt.Errorf("help accepts one command name"))
		}
		if rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
			printHelp("")
			return 0
		}
		command, ok := findCommand(rest[0])
		if !ok {
			return commandError("help", fmt.Errorf("unknown command %q", rest[0]))
		}
		printHelp(command.name)
		return 0
	}
	command, ok := findCommand(name)
	if !ok {
		if strings.HasPrefix(name, "-") {
			return cmdReport("summary", args[1:])
		}
		return commandError("", fmt.Errorf("unknown command %q", name))
	}
	if command.report {
		return cmdReport(command.name, rest)
	}
	switch command.name {
	case "agents":
		return cmdAgents(rest)
	case "price":
		return cmdPrice(rest)
	case "version":
		if len(rest) == 1 && (rest[0] == "--help" || rest[0] == "-h") {
			printHelp("version")
			return 0
		}
		if len(rest) != 0 {
			return commandError("version", fmt.Errorf("version accepts no arguments"))
		}
		fmt.Printf("tokentelemetry %s\n", Version)
		return 0
	}
	return commandError("", fmt.Errorf("unknown command %q", name))
}

func commandError(command string, err error) int {
	fmt.Fprintf(os.Stderr, "tokentelemetry: %v\n", err)
	hint := "tt help"
	if command != "" && command != "help" {
		hint += " " + command
	}
	fmt.Fprintf(os.Stderr, "Run `%s` for usage.\n", hint)
	return 2
}

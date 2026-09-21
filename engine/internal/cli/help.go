package cli

import (
	"fmt"
	"strings"
)

type commandSpec struct {
	name, description string
	aliases           []string
	report            bool
}

var commands = []commandSpec{
	{name: "summary", description: "usage overview; default is the last 30 days", report: true},
	{name: "daily", description: "usage by day", report: true},
	{name: "weekly", description: "usage by ISO week", report: true},
	{name: "monthly", description: "usage by month", report: true},
	{name: "session", aliases: []string{"sessions"}, description: "usage by session", report: true},
	{name: "model", aliases: []string{"models"}, description: "usage by model", report: true},
	{name: "project", aliases: []string{"projects"}, description: "usage by project", report: true},
	{name: "agents", aliases: []string{"providers"}, description: "provider coverage and detected source paths"},
	{name: "price", description: "effective-dated rates for one or more models"},
	{name: "version", aliases: []string{"--version", "-v"}, description: "build version"},
}

func findCommand(name string) (commandSpec, bool) {
	for _, command := range commands {
		if name == command.name {
			return command, true
		}
		for _, alias := range command.aliases {
			if name == alias {
				return command, true
			}
		}
	}
	return commandSpec{}, false
}

func printHelp(name string) {
	if name == "" {
		fmt.Print(`TokenTelemetry - local usage and cost reports for AI coding agents

USAGE
  tt [command] [flags]
  tokentelemetry [command] [flags]

REPORTS
`)
		for _, command := range commands {
			if command.report {
				fmt.Printf("  %-10s %s\n", command.name, command.description)
			}
		}
		fmt.Print("\nLOOKUP\n")
		for _, command := range commands {
			if !command.report && command.name != "version" {
				fmt.Printf("  %-10s %s\n", command.name, command.description)
			}
		}
		fmt.Print(`
HELP
  help [command]       focused command help; also COMMAND --help
  version              build version; also -v or --version

COMMON REPORT FLAGS
  --all-time, -a        include all available history
  --agent NAME         select agents; repeatable or comma-separated
  --json               machine-readable output
  --plain              omit summary charts and the startup spinner

EXAMPLES
  tt -a
  tt daily --agent claude,codex --since 2026-09-01
  tt agents --installed
  tt agents cursor
  tt help summary

Aliases: sessions, models, projects, providers.
Run tt help COMMAND for all flags, date defaults and examples.
`)
		return
	}
	command, _ := findCommand(name)
	fmt.Printf("TokenTelemetry %s - %s\n\n", name, command.description)
	if command.report {
		fmt.Printf("USAGE\n  tt %s [flags]\n", name)
		if len(command.aliases) > 0 {
			fmt.Printf("  Aliases: %s\n", strings.Join(command.aliases, ", "))
		}
		if name == "summary" {
			fmt.Print("\nDefaults to the last 30 local calendar days. Use -a for all history.\nFlags without a command, such as tt -a, run summary.\n")
		} else {
			fmt.Print("\nIncludes all available history unless date filters are supplied.\n")
		}
		switch name {
		case "daily", "weekly", "monthly", "session":
			fmt.Print("Includes a model breakdown by default; --group-by none gives flat rows.\n")
		case "project":
			fmt.Print("Projects are flat by default; --group-by model adds model detail.\n")
		case "summary":
			fmt.Print("Charts stay flat; --group-by adds breakdowns to JSON output.\n")
		}
		fmt.Print(reportFlagsHelp)
		if name == "summary" {
			fmt.Print("  --limit N           chart rows; default 7, including when N is 0\n\nSummary JSON includes all rows regardless of the chart limit.\n")
		} else {
			fmt.Print("  --limit N           maximum displayed rows; 0 means all\n")
		}
		fmt.Printf("\nEXAMPLE\n  tt %s --all-time --agent claude,codex\n", name)
		return
	}
	switch name {
	case "agents":
		fmt.Print(`USAGE
  tt agents [NAME ...] [flags]
  tt providers [NAME ...] [flags]

Shows the full catalog by default. Names are case-insensitive identifiers.
This checks source paths without scanning usage. A detected path does not prove
that the source contains measured usage or complete history.

FLAGS
  --installed         show only entries with detected source paths
  --json              machine-readable catalog
  --help, -h          show this help

EXAMPLES
  tt agents --installed
  tt agents cursor copilot
  tt providers --json kimi kimicode
`)
	case "price":
		fmt.Print(`USAGE
  tt price MODEL [MODEL ...] [flags]

Shows API list rates per million tokens and their effective dates.
This looks up pricing without scanning usage logs.

FLAGS
  --no-color          disable colour; also honours NO_COLOR
  --help, -h          show this help

EXAMPLE
  tt price gpt-5.6-luna --no-color
`)
	case "version":
		fmt.Print("USAGE\n  tt version\n  tt --version\n  tt -v\n")
	}
}

const reportFlagsHelp = `
FILTERS
  --all-time, -a      include all history, including undated records
  --since, --from DATE include usage on or after DATE (YYYY-MM-DD)
  --until, --to DATE  include usage on or before DATE
                      date bounds cannot be combined with --all-time
  --agent NAME        repeatable or comma-separated; use tt agents for names
  --model NAME        repeatable or comma-separated model filter
  --project NAME      repeatable full path or trailing folder name
  --subagents MODE    include (default), only, or exclude

GROUPING
  --group-by, --by DIMS  comma-separated day, week, month, agent, model,
                        provider, project, session; none for flat output
  --breakdown           shorthand for a model breakdown

OUTPUT
  --json              machine-readable output
  --plain             omit summary charts and the startup spinner
  --compact           abbreviate counts in detailed tables
  --no-color          disable colour; also honours NO_COLOR
  --help, -h          show this command's help

SCANNING
  --no-cache          read source logs without reading or updating the cache
  --verbose           show scan, cache and duplicate diagnostics

ROWS
`

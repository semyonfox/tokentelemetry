package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
)

func cmdAgents(args []string) int {
	fs := flag.NewFlagSet("agents", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "")
	installed := fs.Bool("installed", false, "")
	names, err := parseFlags(fs, args)
	if err == flag.ErrHelp {
		printHelp("agents")
		return 0
	}
	if err != nil {
		return commandError("agents", err)
	}
	for _, name := range names {
		if !ingest.KnownAgent(name) {
			return commandError("agents", fmt.Errorf("unknown agent %q; use `tt agents` to list supported identifiers", name))
		}
	}
	rows := filterAgents(ingest.Catalog(), names, *installed)
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintf(os.Stderr, "tokentelemetry: %v\n", err)
			return 1
		}
		return 0
	}
	if len(rows) == 0 {
		fmt.Println("No matching agent sources found.")
		return 0
	}
	for _, r := range rows {
		mark := "—"
		if r.Installed {
			mark = "✓"
		}
		fmt.Printf(" %s  %-16s %-14s %s\n", mark, r.Agent, r.Status, strings.Join(r.Roots, ", "))
		if r.Coverage != "" {
			fmt.Printf("      %s\n", r.Coverage)
		}
		if r.ExplicitOnly {
			fmt.Printf("      Requires --agent %s; may overlap other usage sources\n", r.Agent)
		}
	}
	return 0
}

func filterAgents(rows []ingest.ProviderInfo, names []string, installed bool) []ingest.ProviderInfo {
	filtered := make([]ingest.ProviderInfo, 0, len(rows))
	for _, row := range rows {
		if installed && !row.Installed {
			continue
		}
		matched := len(names) == 0
		for _, name := range names {
			matched = matched || strings.EqualFold(name, row.Agent)
		}
		if matched {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

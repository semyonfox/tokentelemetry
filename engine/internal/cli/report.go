package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
	"github.com/semyonfox/tokentelemetry/engine/internal/pricing"
	"github.com/semyonfox/tokentelemetry/engine/internal/projectmeta"
	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func cmdReport(cmd string, args []string) int {
	opts, err := parseReportOptions(cmd, args)
	if err == flag.ErrHelp {
		printHelp(cmd)
		return 0
	}
	if err != nil {
		return commandError(cmd, err)
	}
	f, agents := opts.filter, opts.filter.Agents

	progress := startProgress(os.Stderr, progressEnabled(opts.asJSON || opts.plainOutput), "Checking for updated prices...")
	defer progress.Stop()
	pricing.Refresh()
	tbl, err := pricing.Load()
	if err != nil {
		progress.Stop()
		fmt.Fprintf(os.Stderr, "tokentelemetry: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	progress.Update("Finding agent logs...")
	scanners := ingest.Available(agents...)
	if len(scanners) == 0 {
		progress.Stop()
		fmt.Fprintln(os.Stderr, "tokentelemetry: no agent logs found on this machine (try `tt agents --installed`)")
		return 1
	}
	progress.Update(fmt.Sprintf("Scanning logs from %d agents...", len(scanners)))
	cacheDir := ""
	if !opts.noCache {
		if dir, err := os.UserCacheDir(); err == nil {
			cacheDir = filepath.Join(dir, "tokentelemetry", "scans")
		}
	}
	res, err := ingest.RunCached(ctx, scanners, cacheDir)
	if err != nil {
		progress.Stop()
		fmt.Fprintf(os.Stderr, "tokentelemetry: %v\n", err)
		return 1
	}
	lineage, lineageErr := projectmeta.LoadT3ProjectLineage(ctx)

	g := report.Daily
	switch cmd {
	case "weekly":
		g = report.Weekly
	case "monthly":
		g = report.Monthly
	}
	progress.Update("Calculating usage and costs...")
	rep := report.BuildWithProjectLineage(res.Turns, tbl, f, g, res.Duplicates, opts.dimensions, lineage)
	progress.Stop()
	if lineageErr != nil {
		fmt.Fprintf(os.Stderr, "tokentelemetry: warning: %v\n", lineageErr)
	}
	for _, e := range res.Errors {
		fmt.Fprintf(os.Stderr, "tokentelemetry: warning: %v\n", e)
	}
	if opts.verbose && cacheDir != "" {
		fmt.Fprintf(os.Stderr, "tokentelemetry: scan cache: %d hit(s), %d miss(es)\n", res.CacheHits, res.CacheMisses)
	}

	if opts.asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(view(cmd, rep)); err != nil {
			fmt.Fprintf(os.Stderr, "tokentelemetry: %v\n", err)
			return 1
		}
		return 0
	}
	if cmd == "summary" {
		renderOverview(os.Stdout, rep, opts.limit, colorEnabled(opts.noColor), opts.verbose, !opts.plainOutput, agents)
		return 0
	}
	render(os.Stdout, cmd, rep, opts.limit, colorEnabled(opts.noColor), opts.verbose, opts.compact, agents)
	return 0
}

// view narrows the report to the rows the chosen command is about, so `--json`
// output does not carry four aggregates the caller did not ask for.
func view(cmd string, rep *report.Report) any {
	if cmd == "summary" {
		return rep
	}
	type out struct {
		Command string          `json:"command"`
		Rows    []report.Bucket `json:"rows"`
		*report.Report
	}
	o := out{Command: cmd, Report: rep}
	switch cmd {
	case "session":
		o.Rows = rep.Sessions
	case "model":
		o.Rows = rep.ByModel
	case "project":
		o.Rows = rep.ByProject
	default:
		o.Rows = rep.Series
	}
	// Rows are the answer; the per-dimension aggregates stay available but are
	// not duplicated into Rows.
	o.Report.Series = nil
	if cmd != "model" {
		o.Report.ByModel = nil
	}
	if cmd != "project" {
		o.Report.ByProject = nil
	}
	if cmd != "session" {
		o.Report.Sessions = nil
	}
	return o
}

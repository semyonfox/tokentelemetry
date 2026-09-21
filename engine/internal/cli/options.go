package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

type reportOptions struct {
	filter      report.Filter
	dimensions  []report.Dimension
	limit       int
	asJSON      bool
	plainOutput bool
	noColor     bool
	verbose     bool
	noCache     bool
	compact     bool
}

func parseReportOptions(cmd string, args []string) (reportOptions, error) {
	var opts reportOptions
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var agents, models, projects stringList
	var allTime bool
	fs.BoolVar(&allTime, "all-time", false, "")
	fs.BoolVar(&allTime, "a", false, "")
	since := fs.String("since", "", "")
	from := fs.String("from", "", "")
	until := fs.String("until", "", "")
	to := fs.String("to", "", "")
	subagents := fs.String("subagents", "include", "")
	breakdown := fs.Bool("breakdown", false, "")
	groupBy := fs.String("group-by", "", "")
	by := fs.String("by", "", "")
	fs.BoolVar(&opts.plainOutput, "plain", false, "")
	fs.BoolVar(&opts.asJSON, "json", false, "")
	fs.IntVar(&opts.limit, "limit", 0, "")
	fs.BoolVar(&opts.noColor, "no-color", false, "")
	fs.BoolVar(&opts.verbose, "verbose", false, "")
	fs.BoolVar(&opts.noCache, "no-cache", false, "")
	fs.BoolVar(&opts.compact, "compact", false, "")
	fs.Var(&agents, "agent", "")
	fs.Var(&models, "model", "")
	fs.Var(&projects, "project", "")
	positional, err := parseFlags(fs, args)
	if err != nil {
		return opts, err
	}
	if len(positional) > 0 {
		return opts, fmt.Errorf("unexpected report arguments: %s", strings.Join(positional, " "))
	}
	if opts.limit < 0 {
		return opts, fmt.Errorf("--limit must not be negative")
	}
	var cursorIDE, cursorSDK bool
	for _, agent := range agents {
		if !ingest.KnownAgent(agent) {
			return opts, fmt.Errorf("unknown agent %q; use `tt agents` to list supported identifiers", agent)
		}
		if reason := ingest.SourceLimitation(agent); reason != "" {
			return opts, fmt.Errorf("%s: %s", agent, reason)
		}
		cursorIDE = cursorIDE || strings.EqualFold(agent, "cursor")
		cursorSDK = cursorSDK || strings.EqualFold(agent, "cursor-agent")
	}
	if cursorIDE && cursorSDK {
		return opts, fmt.Errorf("Cursor history and SDK results may contain the same usage without shared request IDs; select --agent cursor or --agent cursor-agent separately")
	}
	opts.filter = report.Filter{
		From: firstNonEmpty(*since, *from), To: firstNonEmpty(*until, *to),
		Agents: agents, Models: models, Projects: projects, Subagents: *subagents,
	}
	f := &opts.filter
	if allTime && (f.From != "" || f.To != "") {
		return opts, fmt.Errorf("--all-time cannot be combined with --since, --from, --until or --to")
	}
	if cmd == "summary" && !allTime && f.From == "" && f.To == "" {
		now := time.Now()
		f.From = now.AddDate(0, 0, -29).Format("2006-01-02")
		f.To = now.Format("2006-01-02")
	}
	if f.From != "" && f.To != "" && f.From > f.To {
		return opts, fmt.Errorf("--since must not be after --until")
	}
	for _, date := range []string{f.From, f.To} {
		if date != "" {
			if _, err := time.Parse("2006-01-02", date); err != nil {
				return opts, fmt.Errorf("bad date %q, expected YYYY-MM-DD", date)
			}
		}
	}
	switch f.Subagents {
	case "include", "only", "exclude":
	default:
		return opts, fmt.Errorf("--subagents must be include, only or exclude")
	}
	spec := firstNonEmpty(*groupBy, *by)
	dims, err := report.ParseDimensions(spec)
	if err != nil {
		return opts, err
	}
	opts.dimensions = defaultDimensions(cmd, spec, *breakdown, dims)
	return opts, nil
}

// parseFlags accepts flags before or after positional lookup arguments, while
// leaving flag validation and value parsing to the standard library. A -- ends
// option processing, including for identifiers beginning with a dash.
func parseFlags(fs *flag.FlagSet, args []string) ([]string, error) {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name, _, inlineValue := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-"), "=")
		if inlineValue {
			continue
		}
		f := fs.Lookup(name)
		if f == nil {
			continue
		}
		if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return positional, fs.Parse(options)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	// Accept both repeated flags and a single comma-separated value, because
	// both spellings are what people reach for.
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			*s = append(*s, part)
		}
	}
	return nil
}

func defaultDimensions(cmd, spec string, breakdown bool, dims []report.Dimension) []report.Dimension {
	if len(dims) == 0 && breakdown {
		return []report.Dimension{report.DimModel}
	}
	if spec == "" && !breakdown && cmd != "model" && cmd != "summary" && cmd != "project" {
		return []report.Dimension{report.DimModel}
	}
	return dims
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

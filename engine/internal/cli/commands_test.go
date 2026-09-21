package cli

import (
	"flag"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
)

func TestFocusedCommandHelp(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		want, absent string
	}{
		{[]string{"help"}, "REPORTS", "--subagents"},
		{[]string{"help", "summary"}, "last 30 local calendar days", "tt price MODEL"},
		{[]string{"daily", "--help"}, "Includes all available history", "last 30 local calendar days"},
		{[]string{"help", "sessions"}, "tt session [flags]", "tt agents [NAME"},
		{[]string{"agents", "--help"}, "--installed", "--group-by"},
		{[]string{"providers", "-h"}, "tt providers [NAME", "--since"},
		{[]string{"price", "--help"}, "tt price MODEL", "--agent"},
		{[]string{"price", "fixture-model", "-h"}, "tt price MODEL", "no rate on file"},
		{[]string{"help", "version"}, "tt --version", "--all-time"},
	} {
		code, out, err := runCapturedCLI(t, tc.args...)
		if code != 0 || err != "" || !strings.Contains(out, tc.want) || strings.Contains(out, tc.absent) {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", tc.args, code, out, err)
		}
	}
}

func TestCommandValidationBeforeLookupOrScan(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"agents", "--jsno"}, "flag provided but not defined"},
		{[]string{"agents", "claude", "--jsno"}, "flag provided but not defined"},
		{[]string{"agents", "claudee"}, "unknown agent"},
		{[]string{"price", "fixture-model", "--jsno"}, "flag provided but not defined"},
		{[]string{"price"}, "needs a model id"},
		{[]string{"version", "extra"}, "accepts no arguments"},
		{[]string{"help", "unknown"}, "unknown command"},
		{[]string{"help", "daily", "weekly"}, "one command name"},
		{[]string{"summary", "--agent", "claudee"}, "unknown agent"},
		{[]string{"summary", "--agent", "GROKBOT"}, "no measured usage"},
		{[]string{"daily", "extra"}, "unexpected report arguments"},
		{[]string{"daily", "--limit=-1"}, "must not be negative"},
	} {
		code, out, err := runCapturedCLI(t, tc.args...)
		if code != 2 || out != "" || !strings.Contains(err, tc.want) || !strings.Contains(err, "Run `tt help") {
			t.Fatalf("%v: exit=%d stdout=%q stderr=%q", tc.args, code, out, err)
		}
	}
}

func TestInterspersedFlagsKeepValuesAndEndMarker(t *testing.T) {
	fs := flag.NewFlagSet("lookup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noColor := fs.Bool("no-color", false, "")
	agent := fs.String("agent", "", "")
	limit := fs.Int("limit", 0, "")
	args, err := parseFlags(fs, []string{"first", "--no-color", "second", "--agent", "-name", "--limit=-2", "--", "--literal", "-h"})
	if err != nil || !*noColor || *agent != "-name" || *limit != -2 || !reflect.DeepEqual(args, []string{"first", "second", "--literal", "-h"}) {
		t.Fatalf("args=%v noColor=%v agent=%q limit=%d err=%v", args, *noColor, *agent, *limit, err)
	}
}

func TestAgentFiltersPreserveCatalogRows(t *testing.T) {
	rows := []ingest.ProviderInfo{
		{Agent: "claude", Installed: true, Status: "local", Roots: []string{"fixture"}},
		{Agent: "cursor-agent", Installed: true, Status: "local", ExplicitOnly: true},
		{Agent: "grokbot", Status: "source-limited", Coverage: "no measured usage"},
	}
	for _, tc := range []struct {
		names     []string
		installed bool
		want      []ingest.ProviderInfo
	}{
		{nil, false, rows},
		{nil, true, rows[:2]},
		{[]string{"CLAUDE", "GROKBOT"}, false, []ingest.ProviderInfo{rows[0], rows[2]}},
		{[]string{"GROKBOT"}, true, []ingest.ProviderInfo{}},
	} {
		got := filterAgents(rows, tc.names, tc.installed)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("names=%v installed=%v: got %+v want %+v", tc.names, tc.installed, got, tc.want)
		}
	}
}

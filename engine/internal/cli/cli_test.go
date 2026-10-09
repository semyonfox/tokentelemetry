package cli

import (
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

func TestDefaultDimensions(t *testing.T) {
	model := []report.Dimension{report.DimModel}
	for _, tc := range []struct {
		name      string
		cmd       string
		spec      string
		breakdown bool
		dims      []report.Dimension
		want      []report.Dimension
	}{
		{name: "daily defaults to model", cmd: "daily", want: model},
		{name: "session defaults to model", cmd: "session", want: model},
		{name: "project stays flat", cmd: "project", want: nil},
		{name: "project explicit model", cmd: "project", spec: "model", dims: model, want: model},
		{name: "project breakdown", cmd: "project", breakdown: true, want: model},
		{name: "explicit flat remains flat", cmd: "daily", spec: "none", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := defaultDimensions(tc.cmd, tc.spec, tc.breakdown, tc.dims)
			if len(got) != len(tc.want) {
				t.Fatalf("dimensions = %v, want %v", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("dimensions = %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// --limit is one knob: a cap on displayed rows, lifted by 0. Only the summary
// charts fill in a default when it is omitted.
func TestLimitDefaultsPerCommand(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		args []string
		want int
	}{
		{"summary", nil, 7},
		{"summary", []string{"--limit", "0"}, 0},
		{"summary", []string{"--limit", "3"}, 3},
		{"daily", nil, 0},
		{"daily", []string{"--limit", "3"}, 3},
	} {
		opts, err := parseReportOptions(tc.cmd, tc.args)
		if err != nil || opts.limit != tc.want {
			t.Errorf("%s %v: limit=%d err=%v, want %d", tc.cmd, tc.args, opts.limit, err, tc.want)
		}
	}
}

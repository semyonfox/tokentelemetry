package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"unicode"

	"github.com/VasiHemanth/tokentelemetry/engine/internal/report"
)

// renderOverview uses the same aggregates as detailed reports. Limits affect
// chart rows only; the headline and JSON always include all matching usage.
func renderOverview(w io.Writer, rep *report.Report, limit int, color, verbose, bars bool, agents []string) {
	th := theme{on: color}
	if rep.MatchedTurns == 0 {
		fmt.Fprintln(w, "No usage matched those filters.")
		return
	}
	section(w, th, "TokenTelemetry · "+rep.WindowFrom+" to "+rep.WindowTo)
	summary(w, th, rep, 0, verbose, agents)
	if limit == 0 {
		limit = 7
	}
	width := 20
	if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns < 72 {
		width = 0
	}
	if !bars {
		width = 0
	}
	daily := rep.Series
	if len(daily) > limit {
		daily = daily[len(daily)-limit:]
	}
	overviewChart(w, th, fmt.Sprintf("Daily tokens · latest %d of %d recorded days", len(daily), len(rep.Series)), daily, width, false)
	models := rep.ByModel
	if len(models) > limit {
		models = models[:limit]
	}
	overviewChart(w, th, fmt.Sprintf("Models by list cost · top %d of %d", len(models), len(rep.ByModel)), models, width, true)
	agentsRows := rep.ByAgent
	if len(agentsRows) > limit {
		agentsRows = agentsRows[:limit]
	}
	overviewChart(w, th, fmt.Sprintf("Agents by list cost · top %d of %d", len(agentsRows), len(rep.ByAgent)), agentsRows, width, true)
	if width > 0 {
		fmt.Fprintln(w, "  Bars scale to the largest row in each chart.")
	}
	fmt.Fprintln(w, "  Use daily/model for detailed tables.")
}

func overviewChart(w io.Writer, th theme, title string, rows []report.Bucket, width int, byCost bool) {
	section(w, th, title)
	maxValue := 0.0
	value := func(b report.Bucket) float64 {
		if byCost {
			return b.Cost
		}
		return float64(b.Usage.Total())
	}
	for _, b := range rows {
		maxValue = math.Max(maxValue, value(b))
	}
	for _, b := range rows {
		label := chartLabel(b.Key)
		fmt.Fprintf(w, "  %-28s", label)
		if width > 0 {
			n := 0
			if maxValue > 0 && value(b) > 0 {
				n = max(1, int(math.Round(value(b)/maxValue*float64(width))))
			}
			fmt.Fprintf(w, " %s", th.label(strings.Repeat("#", n)+strings.Repeat(" ", width-n)))
		}
		if byCost {
			fmt.Fprintf(w, " %10s", money(b.Cost))
		} else {
			fmt.Fprintf(w, " %10s", tokens(b.Usage.Total()))
		}
		if b.Unpriced > 0 {
			fmt.Fprint(w, " *")
		}
		fmt.Fprintln(w)
	}
	for _, b := range rows {
		if b.Unpriced > 0 {
			fmt.Fprintln(w, "  * Includes unpriced usage; its cost is excluded.")
			break
		}
	}
}

// Log-supplied names must not inject terminal control sequences or extra rows.
func chartLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	runes := []rune(s)
	if len(runes) > 28 {
		return string(runes[:25]) + "..."
	}
	return s
}

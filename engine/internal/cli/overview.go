package cli

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/semyonfox/tokentelemetry/engine/internal/report"
)

// renderOverview uses the same aggregates as detailed reports. The limit caps
// chart rows only, 0 meaning all of them; the headline and JSON always include
// all matching usage.
func renderOverview(w io.Writer, rep *report.Report, limit int, color, verbose, bars bool, agents []string) {
	th := theme{on: color}
	if rep.MatchedTurns == 0 {
		noMatch(w, th)
		return
	}
	section(w, th, "TokenTelemetry · "+rep.WindowFrom+" to "+rep.WindowTo)
	summary(w, th, rep, 0, verbose, agents)
	renderPlanWindows(w, th, rep.PlanWindows, time.Now())
	// Bars need 64 cells; on anything narrower than 72 the charts drop them
	// rather than wrap.
	width := 20
	if columns := outputWidth(w); !bars || (columns > 0 && columns < 72) {
		width = 0
	}
	palette := newAgentPalette(rep.ByAgent)
	daily := rep.Series
	if limit > 0 && len(daily) > limit {
		daily = daily[len(daily)-limit:]
	}
	dailyChart(w, th, fmt.Sprintf("Daily list cost by agent · latest %d of %d recorded days", len(daily), len(rep.Series)), daily, width, palette, rep.ByAgent)
	models := rep.ByModel
	if limit > 0 && len(models) > limit {
		models = models[:limit]
	}
	rankedChart(w, th, fmt.Sprintf("Models by list cost · top %d of %d", len(models), len(rep.ByModel)), models, width, func(b report.Bucket) string {
		return palette.color(dominantAgent(b.CostByAgent))
	})
	agentsRows := rep.ByAgent
	if limit > 0 && len(agentsRows) > limit {
		agentsRows = agentsRows[:limit]
	}
	rankedChart(w, th, fmt.Sprintf("Agents by list cost · top %d of %d", len(agentsRows), len(rep.ByAgent)), agentsRows, width, func(b report.Bucket) string {
		return palette.color(b.Key)
	})
	if width > 0 {
		note := "Bars scale to the largest row in each chart."
		if th.on {
			note = "Bars scale to the largest row in each chart; a model takes its main agent's colour."
		}
		fmt.Fprintln(w, "  "+note)
	}
	fmt.Fprintln(w, "  Use daily/model for detailed tables.")
}

// barFill is the one glyph every bar is drawn with. Agents are told apart by
// colour alone; without colour the bars are simply plain.
const barFill = "#"

// brandColor is an agent's brand-nearest colour: a 256-colour shade, so two
// blue or two purple brands still differ, and the nearest basic colour for a
// terminal without 256 colours.
type brandColor struct{ rich, basic string }

// brandColors lists the agents with a recognisable brand colour, so a chart
// reads the way its user already pictures these tools. The rest draw from the
// free palette.
func brandColors() map[string]brandColor {
	return map[string]brandColor{
		"codex":        {ansi256(245), ansiGrey},          // openai: black and white
		"claude":       {ansi256(208), ansiYellow},        // anthropic orange
		"gemini":       {ansi256(33), ansiBlue},           // google blue
		"ibm-bob":      {ansi256(25), ansiBrightBlue},     // ibm's deeper blue
		"copilot":      {ansi256(99), ansiBrightMagenta},  // github copilot's indigo
		"kiro":         {ansi256(129), ansiMagenta},       // aws kiro's purple
		"qwen":         {ansi256(141), ansiBrightMagenta}, // alibaba's lighter purple
		"mistral-vibe": {ansi256(220), ansiYellow},        // mistral's yellow-orange
		"cursor":       {ansi256(253), ansiWhite},         // black and white
		"grok":         {ansi256(231), ansiBrightWhite},   // black and white
	}
}

// freeColors is handed out, in order, to agents without a brand colour and to
// any agent whose brand colour an earlier, costlier agent already took, so no
// two agents in one report share a colour until the palette runs out.
var freeColors = []string{ansiCyan, ansiGreen, ansiMagenta, ansiBlue, ansiRed, ansiYellow, ansiBrightCyan, ansiBrightGreen, ansiBrightBlue, ansiBrightRed}

// agentPalette maps each agent in the report to its colour, assigned in cost
// order so the legend reads the same way as the agents chart.
type agentPalette map[string]string

func newAgentPalette(byAgent []report.Bucket) agentPalette {
	brands, rich := brandColors(), richColor()
	p := agentPalette{}
	used := map[string]bool{}
	next := 0
	for _, b := range byAgent {
		brand, ok := brands[b.Key]
		c := brand.rich
		if !rich {
			c = brand.basic
		}
		if !ok || used[c] {
			for next < len(freeColors) && used[freeColors[next]] {
				next++
			}
			if next < len(freeColors) {
				c = freeColors[next]
			} else {
				c = freeColors[len(p)%len(freeColors)]
			}
		}
		p[b.Key] = c
		used[c] = true
	}
	return p
}

func (p agentPalette) color(agent string) string {
	if c, ok := p[agent]; ok {
		return c
	}
	return freeColors[0]
}

// dominantAgent is the agent that paid most of a bucket's cost, or "" when
// nothing in it was priced.
func dominantAgent(costByAgent map[string]float64) string {
	agents := sortedAgents(costByAgent)
	if len(agents) == 0 {
		return ""
	}
	return agents[0]
}

// sortedAgents orders agents by cost, highest first, then by name so equal
// shares draw the same way every run.
func sortedAgents(costByAgent map[string]float64) []string {
	agents := make([]string, 0, len(costByAgent))
	for agent := range costByAgent {
		agents = append(agents, agent)
	}
	sort.Slice(agents, func(i, j int) bool {
		if costByAgent[agents[i]] != costByAgent[agents[j]] {
			return costByAgent[agents[i]] > costByAgent[agents[j]]
		}
		return agents[i] < agents[j]
	})
	return agents
}

// dailyChart draws each day's list cost as one bar stacked by agent, with the
// day's tokens alongside for scale and, in colour, a legend naming each agent.
func dailyChart(w io.Writer, th theme, title string, rows []report.Bucket, width int, palette agentPalette, byAgent []report.Bucket) {
	section(w, th, title)
	maxCost := 0.0
	for _, b := range rows {
		maxCost = math.Max(maxCost, b.Cost)
	}
	for _, b := range rows {
		fmt.Fprintf(w, "  %s", chartLabel(b.Key))
		if width > 0 {
			fmt.Fprintf(w, " %s", stackedBar(th, b, maxCost, width, palette))
		}
		fmt.Fprintf(w, " %10s %s", money(b.Cost), th.dim(fmt.Sprintf("%7s", tokens(b.Usage.Total()))))
		if b.Unpriced > 0 {
			fmt.Fprint(w, " *")
		}
		fmt.Fprintln(w)
	}
	if width > 0 && th.on {
		legend(w, th, byAgent, palette)
	}
	unpricedNote(w, rows)
}

// stackedBar places each agent's segment end on its cumulative share of the
// day, so rounding never changes the bar's total length, and a day that is
// priced but tiny still shows one cell.
func stackedBar(th theme, b report.Bucket, maxCost float64, width int, palette agentPalette) string {
	if maxCost <= 0 || b.Cost <= 0 {
		return strings.Repeat(" ", width)
	}
	agents := sortedAgents(b.CostByAgent)
	if len(agents) == 0 {
		agents = []string{""}
		b.CostByAgent = map[string]float64{"": b.Cost}
	}
	var sb strings.Builder
	cum, pos := 0.0, 0
	for _, agent := range agents {
		cum += b.CostByAgent[agent]
		end := int(math.Round(cum / maxCost * float64(width)))
		if end > pos {
			sb.WriteString(th.wrap(palette.color(agent), strings.Repeat(barFill, end-pos)))
			pos = end
		}
	}
	if pos == 0 {
		sb.WriteString(th.wrap(palette.color(agents[0]), barFill))
		pos = 1
	}
	sb.WriteString(strings.Repeat(" ", width-pos))
	return sb.String()
}

// rankedChart draws one bar per row in the colour colorOf picks for it.
func rankedChart(w io.Writer, th theme, title string, rows []report.Bucket, width int, colorOf func(report.Bucket) string) {
	section(w, th, title)
	maxCost := 0.0
	for _, b := range rows {
		maxCost = math.Max(maxCost, b.Cost)
	}
	for _, b := range rows {
		fmt.Fprintf(w, "  %s", chartLabel(b.Key))
		if width > 0 {
			n := 0
			if maxCost > 0 && b.Cost > 0 {
				n = max(1, int(math.Round(b.Cost/maxCost*float64(width))))
			}
			fmt.Fprintf(w, " %s%s", th.wrap(colorOf(b), strings.Repeat(barFill, n)), strings.Repeat(" ", width-n))
		}
		fmt.Fprintf(w, " %10s", money(b.Cost))
		if b.Unpriced > 0 {
			fmt.Fprint(w, " *")
		}
		fmt.Fprintln(w)
	}
	unpricedNote(w, rows)
}

func unpricedNote(w io.Writer, rows []report.Bucket) {
	for _, b := range rows {
		if b.Unpriced > 0 {
			fmt.Fprintln(w, "  * Includes unpriced usage; its cost is excluded.")
			break
		}
	}
}

// legend names each agent's colour once, in cost order, under the daily chart.
func legend(w io.Writer, th theme, byAgent []report.Bucket, palette agentPalette) {
	if len(byAgent) == 0 {
		return
	}
	parts := make([]string, 0, len(byAgent))
	for _, b := range byAgent {
		parts = append(parts, th.wrap(palette.color(b.Key), strings.Repeat(barFill, 2))+" "+strings.TrimSpace(chartLabel(b.Key)))
	}
	fmt.Fprintf(w, "  %s\n", strings.Join(parts, "   "))
}

// chartLabelWidth is the fixed label column every overview chart shares.
const chartLabelWidth = 28

// chartLabel fits a log-supplied name into the label column. Control
// characters are blanked so a name cannot inject escape sequences or rows, and
// the width is measured in terminal cells so wide runes still align.
func chartLabel(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	return pad(truncate(s, chartLabelWidth), chartLabelWidth, false)
}

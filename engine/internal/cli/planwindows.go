package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/ingest"
	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// renderPlanWindows shows each agent's own subscription windows under the
// summary. The percentages are the agent's, observed at some point in the
// past, so every row says when. An expired window is left out: its old
// percentage says nothing about the current one.
func renderPlanWindows(w io.Writer, th theme, windows []model.PlanWindow, now time.Time) {
	live := ingest.LivePlanWindows(windows, now)
	showPlan := false
	for _, pw := range live {
		showPlan = showPlan || pw.Plan != ""
	}
	if len(live) == 0 {
		return
	}
	section(w, th, "Plan windows · as each agent last reported them")
	cols := []column{
		{title: "AGENT", align: alignLeft},
		{title: "WINDOW", align: alignLeft},
		{title: "USED", align: alignRight},
		{title: "RESETS", align: alignLeft},
		{title: "SEEN", align: alignLeft},
	}
	if showPlan {
		cols = append(cols, column{title: "PLAN", align: alignLeft})
	}
	t := newTable(th, cols...)
	for _, pw := range live {
		used := fmt.Sprintf("%.0f%%", pw.UsedPercent)
		usedCell := plain(used)
		if pw.UsedPercent >= 90 {
			usedCell = styled(used, th.warn)
		}
		cells := []cell{
			plain(string(pw.Agent)),
			plain(pw.Window),
			usedCell,
			plain(resetLabel(pw.ResetsAt, now)),
			styled(sinceLabel(now.Sub(pw.ObservedAt)), th.dim),
		}
		if showPlan {
			cells = append(cells, plain(pw.Plan))
		}
		t.add(cells...)
	}
	t.render(w)
}

// resetLabel keeps the reset time as short as the distance allows: a clock
// time today, a weekday within the week, a date beyond it.
func resetLabel(at, now time.Time) string {
	if at.IsZero() {
		return "unknown"
	}
	at, now = at.Local(), now.Local()
	y1, m1, d1 := now.Date()
	y2, m2, d2 := at.Date()
	switch {
	case y1 == y2 && m1 == m2 && d1 == d2:
		return at.Format("15:04")
	case at.Sub(now) < 7*24*time.Hour:
		return at.Format("Mon 15:04")
	default:
		return at.Format("Jan 2 15:04")
	}
}

func sinceLabel(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

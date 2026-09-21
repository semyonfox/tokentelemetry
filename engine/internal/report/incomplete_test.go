package report

import (
	"reflect"
	"testing"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

func TestIncompleteUsageKeepsModelAndTokensButExcludesCost(t *testing.T) {
	a := turn("a", "dear", "/p", local(2026, 8, 1, 12, 0), 1_000_000)
	a.Usage.Unclassified = 2_000_000
	a.UnpricedReason = "source has no cache split"
	b := turn("b", "cheap", "/p", local(2026, 8, 1, 12, 0), 1_000_000)
	r := Build([]model.Turn{a, b}, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Cost != 1 || r.Totals.Usage.Total() != 4_000_000 || r.Totals.UnpricedTurns != 1 || len(r.Totals.UnpricedReasons) != 1 || r.Totals.UnpricedModels[0] != "dear" {
		t.Fatalf("incomplete usage was priced or lost: %+v", r.Totals)
	}
	// Missing split itself excludes pricing even when a reader omits a reason.
	a.UnpricedReason = ""
	r = Build([]model.Turn{a}, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Cost != 0 || r.Totals.UnpricedTurns != 1 {
		t.Fatalf("unsplit input priced: %+v", r.Totals)
	}
}

func TestCreditsStaySeparateByProviderAndRespectFilters(t *testing.T) {
	creditA, creditB := 1.25, 9.5
	a := turn("a", "dear", "/p", local(2026, 8, 1, 12, 0), 0)
	a.Agent = model.Agent("kiro")
	a.Credits = &creditA
	b := a
	b.Key = "b"
	b.Agent = model.Agent("codebuff")
	b.Credits = &creditB
	r := Build([]model.Turn{a, b}, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Cost != 0 || r.Totals.Usage.Total() != 0 || r.Totals.UnpricedTurns != 2 || r.Totals.CreditsByAgent["kiro"] != 1.25 || r.Totals.CreditsByAgent["codebuff"] != 9.5 {
		t.Fatalf("credits converted or lost: %+v", r.Totals)
	}
	r = Build([]model.Turn{a, b}, tbl(), Filter{Agents: []string{"kiro"}}, Daily, 0, nil)
	if len(r.Totals.CreditsByAgent) != 1 || r.Totals.CreditsByAgent["kiro"] != 1.25 {
		t.Fatalf("credit filter ignored: %+v", r.Totals)
	}
}

func TestExcludedRuntimeAggregateNeverContributesToTotals(t *testing.T) {
	a := turn("native", "cheap", "/p", local(2026, 8, 1, 12, 0), 1_000_000)
	b := a
	b.Key, b.Agent, b.Model = "wrapper", model.AgentHermes, "dear"
	b.Usage.Input = 2_000_000
	b.ExcludedReason = "explicitly linked runtime aggregate, mixed history cannot be reconciled"
	r := Build([]model.Turn{a, b}, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Cost != 1 || r.Totals.Usage.Total() != 1_000_000 || r.MatchedTurns != 1 || len(r.ExcludedOverlaps) != 1 || r.ExcludedOverlaps[0].Usage.Input != 2_000_000 {
		t.Fatalf("excluded usage counted/lost: %+v", r)
	}
	r = Build([]model.Turn{a, b}, tbl(), Filter{Models: []string{"cheap"}}, Daily, 0, nil)
	if len(r.ExcludedOverlaps) != 0 {
		t.Fatal("excluded evidence ignored report filters")
	}
}

func linkedRuntimeTurns() []model.Turn {
	native := turn("native", "cheap", "/native", local(2026, 8, 1, 12, 0), 1_000_000)
	native.Agent = model.AgentCodex
	native.SessionID = "codex-thread"

	wrapper := turn("wrapper", "dear", "/wrapper", local(2026, 8, 2, 12, 0), 2_000_000)
	wrapper.Agent = model.AgentHermes
	wrapper.SessionID = "hermes-session"
	wrapper.Subagent = true
	wrapper.RuntimeAgent = model.AgentCodex
	wrapper.RuntimeSessionID = native.SessionID
	wrapper.ExcludedReason = runtimeOverlapReason(wrapper)
	return []model.Turn{native, wrapper}
}

func TestRuntimeOverlapCountsMatchingNativeOnceWithoutMutatingInput(t *testing.T) {
	turns := linkedRuntimeTurns()
	before := append([]model.Turn(nil), turns...)
	r := Build(turns, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Turns != 1 || r.Totals.Usage.Output != 1_000_000 || len(r.ExcludedOverlaps) != 1 {
		t.Fatalf("matching native and wrapper were not reconciled: totals=%+v excluded=%+v", r.Totals, r.ExcludedOverlaps)
	}
	if !reflect.DeepEqual(turns, before) {
		t.Fatalf("Build mutated input turns:\n got  %+v\n want %+v", turns, before)
	}

	r = Build(turns[1:], tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Turns != 1 || r.Totals.Usage.Output != 2_000_000 || len(r.ExcludedOverlaps) != 0 {
		t.Fatalf("wrapper-only report retained stale exclusion: totals=%+v excluded=%+v", r.Totals, r.ExcludedOverlaps)
	}
	if !reflect.DeepEqual(turns, before) {
		t.Fatalf("wrapper-only Build mutated input turns:\n got  %+v\n want %+v", turns, before)
	}

	unrelated := turns[1]
	unrelated.ExcludedReason = "independent accounting exclusion"
	r = Build([]model.Turn{unrelated}, tbl(), Filter{}, Daily, 0, nil)
	if r.Totals.Turns != 0 || len(r.ExcludedOverlaps) != 1 || r.ExcludedOverlaps[0].ExcludedReason != unrelated.ExcludedReason {
		t.Fatalf("unrelated exclusion was reclassified: totals=%+v excluded=%+v", r.Totals, r.ExcludedOverlaps)
	}
}

func TestRuntimeOverlapUsesNativeRecordsMatchingReportFilter(t *testing.T) {
	turns := linkedRuntimeTurns()
	for _, tc := range []struct {
		name   string
		filter Filter
	}{
		{name: "agent", filter: Filter{Agents: []string{"hermes"}}},
		{name: "model", filter: Filter{Models: []string{"dear"}}},
		{name: "project", filter: Filter{Projects: []string{"/wrapper"}}},
		{name: "date", filter: Filter{From: "2026-08-02", To: "2026-08-02"}},
		{name: "subagent", filter: Filter{Subagents: "only"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := Build(turns, tbl(), tc.filter, Daily, 0, nil)
			if r.Totals.Turns != 1 || r.Totals.Usage.Output != 2_000_000 || len(r.ExcludedOverlaps) != 0 {
				t.Fatalf("filtered-out native suppressed wrapper: totals=%+v excluded=%+v", r.Totals, r.ExcludedOverlaps)
			}
		})
	}
}

func TestRuntimeOverlapRespectsVerifiedProjectLineage(t *testing.T) {
	turns := linkedRuntimeTurns()
	lineage := model.ProjectLineage{SessionRoots: map[model.ProjectSession]string{}}
	for _, turn := range turns {
		lineage.SessionRoots[model.ProjectSession{Agent: turn.Agent, SessionID: turn.SessionID}] = "/original-project"
	}
	r := BuildWithProjectLineage(turns, tbl(), Filter{Projects: []string{"/original-project"}}, Daily, 0, nil, lineage)
	if r.Totals.Turns != 1 || r.Totals.Usage.Output != 1_000_000 || len(r.ExcludedOverlaps) != 1 {
		t.Fatalf("project lineage bypassed overlap reconciliation: totals=%+v excluded=%+v", r.Totals, r.ExcludedOverlaps)
	}
}

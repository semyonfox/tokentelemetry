package ingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// CursorAgent reads the Cursor SDK's native local store. An explicit
// TT_CURSOR_AGENT_DIR retains support for caller-saved SDK RunResult objects.
// Legacy CLI transcripts do not expose this accounting contract and are not
// estimated.
type CursorAgent struct {
	root        string
	sdkProjects string
}

func NewCursorAgent() *CursorAgent {
	if root := os.Getenv("TT_CURSOR_AGENT_DIR"); root != "" {
		return &CursorAgent{root: root}
	}
	if home := homeDir(); home != "" {
		return &CursorAgent{sdkProjects: filepath.Join(home, ".cursor", "projects")}
	}
	return &CursorAgent{}
}

func newCursorAgentSDKAt(projects string) *CursorAgent {
	return &CursorAgent{sdkProjects: projects}
}

func (*CursorAgent) Agent() model.Agent { return model.Agent("cursor-agent") }
func (*CursorAgent) ExplicitOnly() bool { return true }
func (c *CursorAgent) Roots() []string {
	if c.root != "" {
		if existingDir(c.root) != "" {
			return []string{c.root}
		}
		return nil
	}
	if c.sdkProjects == "" {
		return nil
	}
	paths, _ := cursorSDKStorePaths(context.Background(), c.sdkProjects)
	return paths
}

type cursorRun struct {
	ID      string `json:"id"`
	AgentID string `json:"agentId"`
	Status  string `json:"status"`
	Created int64  `json:"createdAt"`
	Model   struct {
		ID string `json:"id"`
	} `json:"model"`
	Usage *struct {
		Input     int64 `json:"inputTokens"`
		Output    int64 `json:"outputTokens"`
		Read      int64 `json:"cacheReadTokens"`
		Write     int64 `json:"cacheWriteTokens"`
		Total     int64 `json:"totalTokens"`
		Reasoning int64 `json:"reasoningTokens"`
	} `json:"usage"`
}

func (c *CursorAgent) Scan(ctx context.Context, emit func(model.Turn)) error {
	if c.root != "" {
		if existingDir(c.root) == "" {
			return nil
		}
		return c.scanSavedRunResults(ctx, emit)
	}
	if c.sdkProjects == "" {
		return nil
	}
	paths, err := cursorSDKStorePaths(ctx, c.sdkProjects)
	return errors.Join(err, scanCursorAgentSDKStores(ctx, paths, emit))
}

func (c *CursorAgent) scanSavedRunResults(ctx context.Context, emit func(model.Turn)) error {
	var errs []error
	err := filepath.WalkDir(c.root, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		if !d.Type().IsRegular() || filepath.Ext(path) != ".json" {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			errs = append(errs, e)
			return nil
		}
		var run cursorRun
		e = json.NewDecoder(f).Decode(&run)
		f.Close()
		if e != nil {
			errs = append(errs, fmt.Errorf("cursor-agent: invalid SDK result %s", path))
			return nil
		}
		if run.ID == "" || run.Usage == nil {
			return nil
		}
		switch run.Status {
		case "finished", "error", "cancelled":
		default:
			return nil
		}
		r := run.Usage
		for _, count := range []int64{r.Input, r.Output, r.Read, r.Write, r.Reasoning} {
			if count < 0 || count > math.MaxInt64/8 {
				errs = append(errs, fmt.Errorf("cursor-agent: invalid token count in %s", path))
				return nil
			}
		}
		u := model.Usage{Input: r.Input, Output: r.Output, CacheRead: r.Read, CacheWrite: r.Write, Reasoning: r.Reasoning}
		if u.Total() != r.Total || r.Reasoning > r.Output {
			errs = append(errs, fmt.Errorf("cursor-agent: SDK token total does not reconcile in %s", path))
			return nil
		}
		if u.IsZero() {
			return nil
		}
		id := run.Model.ID
		reason := ""
		if id == "" {
			id = "unknown"
			reason = "Cursor SDK result has no resolved model"
		}
		var ts time.Time
		if run.Created > 0 {
			ts = time.UnixMilli(run.Created)
		} else {
			reason = "Cursor SDK RunResult omits wall-clock time; usage is undated and historical pricing is unavailable"
		}
		emit(model.Turn{Key: identityKey("cursor-agent", run.ID), SessionID: run.AgentID, Agent: c.Agent(), Timestamp: ts, Model: id, Provider: "cursor", Usage: u, Aggregate: true, UnpricedReason: reason})
		return nil
	})
	return errors.Join(append(errs, err)...)
}

package ingest

import (
	"context"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

const rooCodeExtensionID = "rooveterinaryinc.roo-cline"

// RooCode scans Roo Code's VS Code task store. Roo inherited Cline's task
// layout, but current records mark apiProtocol and store gross tokensIn;
// records predating that marker retain Cline's disjoint input buckets.
type RooCode struct {
	roots []string
}

func NewRooCode() *RooCode {
	return &RooCode{roots: existingTaskRoots(vscodeGlobalStorageRoots(rooCodeExtensionID))}
}

func newRooCodeAt(roots ...string) *RooCode { return &RooCode{roots: roots} }

func (r *RooCode) Agent() model.Agent { return model.Agent("roo-code") }

func (r *RooCode) Roots() []string { return append([]string(nil), r.roots...) }

func (r *RooCode) Scan(ctx context.Context, emit func(model.Turn)) error {
	return scanLegacyClineTasks(ctx, r.roots, model.Agent("roo-code"), true, emit)
}

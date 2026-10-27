package ingest

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"strings"
	"sync"

	"github.com/semyonfox/tokentelemetry/engine/internal/model"
)

// parserFiles is this package's own code, embedded so the scan cache can key
// on what actually parses usage instead of on the binary. A rebuild or
// reinstall of the same parsers keeps the cache, and any parser change drops it.
//
//go:embed *.go
var parserFiles embed.FS

var parserVersion = sync.OnceValue(func() string {
	h := sha256.New()
	h.Write(model.Source)
	// embed returns names sorted, so the hash is stable
	entries, _ := fs.ReadDir(parserFiles, ".")
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, _ := parserFiles.ReadFile(name)
		h.Write([]byte(name + "\x00"))
		h.Write(raw)
	}
	return hex.EncodeToString(h.Sum(nil))
})

package pricing

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const publishedPrices = "https://raw.githubusercontent.com/semyonfox/tokentelemetry/main/engine/internal/pricing/data/pricing.json"

func cachePath() string {
	dir, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "tokentelemetry", "pricing.json")
}

// Refresh checks once per day. Failure never prevents a local report.
// TT_OFFLINE=1 disables the check while retaining cached prices.
func Refresh() {
	if os.Getenv("TT_OFFLINE") == "1" {
		return
	}
	refresh(cachePath(), publishedPrices, &http.Client{Timeout: 3 * time.Second})
}

func refresh(path, url string, client *http.Client) {
	if path == "" {
		return
	}
	if info, err := os.Stat(path + ".checked"); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return
	}
	// Throttle failed checks too, so an offline machine does not wait every run.
	_ = os.WriteFile(path+".checked", nil, 0600)
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil || !validDataset(raw) {
		return
	}
	old, _ := datasetAt(dataJSON, path)
	next, _ := parseDataset(raw)
	if next.Updated.Before(old.Updated) {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), "prices-*")
	if err != nil {
		return
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	closeErr := f.Close()
	if err == nil && closeErr == nil {
		_ = os.Rename(f.Name(), path)
	}
}

func validDataset(raw []byte) bool {
	_, ok := parseDataset(raw)
	return ok
}

// parseDataset decodes and checks a dataset in one pass. The file is several
// megabytes, so decoding it more than once per run is most of a short report.
func parseDataset(raw []byte) (fileFormat, bool) {
	var f fileFormat
	if json.Unmarshal(raw, &f) != nil || f.Schema != SchemaVersion || f.Updated.IsZero() || len(f.Models) == 0 {
		return fileFormat{}, false
	}
	for _, models := range []map[string]*Model{f.Models, f.ByProvider} {
		for _, m := range models {
			if m == nil || len(m.Rates) == 0 {
				return fileFormat{}, false
			}
			for _, r := range m.Rates {
				if r.From.IsZero() {
					return fileFormat{}, false
				}
				for _, v := range []float64{r.In, r.Out, r.CacheRead, r.CacheWrite, r.CacheWrite1h, r.TierIn, r.TierOut, r.TierCacheRead, r.TierCacheWrite} {
					if v < 0 {
						return fileFormat{}, false
					}
				}
			}
		}
	}
	return f, true
}

// datasetAt returns the refreshed dataset at path when it is valid and not
// older than the bundled one, otherwise the bundled dataset. Each candidate is
// decoded once, and the bundled copy only in full when it is the one used.
func datasetAt(bundled []byte, path string) (fileFormat, error) {
	if path != "" {
		if raw, err := os.ReadFile(path); err == nil {
			var header struct {
				Updated Date `json:"updated"`
			}
			if f, ok := parseDataset(raw); ok && json.Unmarshal(bundled, &header) == nil && !f.Updated.Before(header.Updated) {
				return f, nil
			}
		}
	}
	var f fileFormat
	if err := json.Unmarshal(bundled, &f); err != nil {
		return f, fmt.Errorf("embedded pricing data is corrupt: %w", err)
	}
	return f, nil
}

package pricing

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestLoadAutomaticallyRefreshesPrices(t *testing.T) {
	const cached = `{"schema":2,"updated":"2099-01-01","models":{"fixture-model":{"rates":[{"from":"2099-01-01","in":2,"out":10}]}}}`
	const fresh = `{"schema":2,"updated":"2099-01-02","models":{"fixture-model":{"rates":[{"from":"2099-01-01","in":2,"out":10},{"from":"2099-01-02","in":1,"out":5}]},"fixture-new":{"rates":[{"from":"2099-01-02","in":1,"out":4}]}},"aliases":{"fixture-new-latest":"fixture-new"}}`
	for _, tc := range []struct {
		name     string
		body     string
		status   int
		offline  bool
		recent   bool
		override bool
		wantNew  bool
		wantOut  float64
	}{
		{name: "new models and changed rates", body: fresh, status: http.StatusOK, wantNew: true, wantOut: 5},
		{name: "daily cache", recent: true, wantOut: 10},
		{name: "offline cache", offline: true, wantOut: 10},
		{name: "unavailable feed", status: http.StatusServiceUnavailable, wantOut: 10},
		{name: "invalid feed", body: `{`, status: http.StatusOK, wantOut: 10},
		{name: "older feed", body: strings.ReplaceAll(cached, "2099-01-01", "2098-01-01"), status: http.StatusOK, wantOut: 10},
		{name: "user override", body: fresh, status: http.StatusOK, override: true, wantNew: true, wantOut: 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("XDG_CACHE_HOME", dir)
			t.Setenv("LOCALAPPDATA", dir)
			t.Setenv("HOME", dir)
			t.Setenv("TT_OFFLINE", "")
			if tc.offline {
				t.Setenv("TT_OFFLINE", "1")
			}
			overridePath := filepath.Join(dir, "override.json")
			t.Setenv("TT_PRICING_FILE", overridePath)
			if tc.override {
				if err := os.WriteFile(overridePath, []byte(`{"models":{"fixture-model":{"rates":[{"from":"2099-01-01","out":99}]}}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := cachePath()
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(cached), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path+".checked", nil, 0600); err != nil {
				t.Fatal(err)
			}
			if !tc.recent {
				old := time.Now().Add(-25 * time.Hour)
				if err := os.Chtimes(path+".checked", old, old); err != nil {
					t.Fatal(err)
				}
			}

			reset := func() {
				once = sync.Once{}
				loaded, loadErr = nil, nil
			}
			reset()
			t.Cleanup(reset)
			transport := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = transport })
			calls := 0
			http.DefaultTransport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodGet || r.URL.String() != publishedPrices {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})

			for range 2 {
				tbl, err := Load()
				if err != nil {
					t.Fatal(err)
				}
				rate, _, ok := tbl.Lookup("fixture-model", "", day("2099-01-02"))
				if !ok || rate.Out != tc.wantOut {
					t.Fatalf("output rate = %v, priced = %v; want %v", rate.Out, ok, tc.wantOut)
				}
				_, confidence, priced := tbl.Lookup("fixture-new-latest", "", day("2099-01-02"))
				if priced != tc.wantNew || !priced && confidence != ConfidenceUnpriced {
					t.Fatalf("new model priced = %v, confidence = %v; want priced = %v", priced, confidence, tc.wantNew)
				}
				reset()
			}
			wantCalls := 1
			if tc.offline || tc.recent {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("got %d requests across two loads, want %d", calls, wantCalls)
			}
		})
	}
}

func TestCachedDataFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pricing.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	bundled := []byte(`{"schema":2,"updated":"2026-09-01","models":{"x":{"rates":[{"from":"2026-09-01","in":1,"out":2}]}}}`)
	for _, bad := range []string{`{`, `{"schema":99}`, `{"schema":2,"updated":"2026-08-01","models":{"x":{"rates":[{"from":"2026-08-01","in":1,"out":2}]}}}`, `{"schema":2,"updated":"2026-09-02","models":{"x":null}}`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if string(cachedDataAt(bundled, path)) != string(bundled) {
			t.Fatalf("accepted invalid or older cache: %s", bad)
		}
	}
	newer := []byte(`{"schema":2,"updated":"2026-09-02","models":{"x":{"rates":[{"from":"2026-09-02","in":2,"out":3}]}}}`)
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	if string(cachedDataAt(bundled, path)) != string(newer) {
		t.Fatal("newer cache ignored")
	}
	t.Setenv("TT_OFFLINE", "1")
	Refresh()
	if _, err := os.Stat(path + ".checked"); !os.IsNotExist(err) {
		t.Fatal("offline refresh attempted a check")
	}
}

func TestRefreshCachesAndThrottles(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_, _ = w.Write(dataJSON)
	}))
	defer server.Close()
	path := filepath.Join(t.TempDir(), "prices.json")
	refresh(path, server.URL, server.Client())
	raw, err := os.ReadFile(path)
	if err != nil || !validDataset(raw) {
		t.Fatalf("no valid cache: %v", err)
	}
	refresh(path, server.URL, server.Client())
	if calls != 1 {
		t.Fatalf("got %d requests, want 1", calls)
	}
}

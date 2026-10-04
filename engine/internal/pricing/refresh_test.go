package pricing

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

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

func TestRefreshKeepsNewerDataAtRequestedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.json")
	newer := []byte(`{"schema":2,"updated":"9999-12-31","models":{"x":{"rates":[{"from":"9999-12-31","in":1,"out":2}]}}}`)
	if err := os.WriteFile(path, newer, 0600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(dataJSON)
	}))
	defer server.Close()

	refresh(path, server.URL, server.Client())
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newer) {
		t.Fatal("refresh replaced a newer dataset at the requested path")
	}
}

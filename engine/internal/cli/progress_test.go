package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestProgressClearsBeforeReport(t *testing.T) {
	var out bytes.Buffer
	p := startProgress(&out, true, "Checking prices...")
	p.Update("Scanning logs...")
	p.Stop()
	p.Stop()
	out.WriteString("report\n")
	if !strings.Contains(out.String(), "Checking prices...") || !strings.Contains(out.String(), "Scanning logs...") || !strings.HasSuffix(out.String(), "\r\x1b[2Kreport\n") {
		t.Fatalf("unexpected progress output: %q", out.String())
	}
	out.Reset()
	p = startProgress(&out, false, "quiet")
	p.Update("quiet")
	p.Stop()
	if out.Len() != 0 {
		t.Fatal("quiet progress wrote output")
	}
}

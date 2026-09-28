package api

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// A refusal quotes values anybody can make as long as a URL; the log line
// that carries them must stay short whatever was sent.
func TestARefusalReasonIsCutShortForTheLog(t *testing.T) {
	long := strings.Repeat("é", 500)
	got := truncateForLog(long, 200)
	if len(got) > 203 || !strings.HasSuffix(got, "...") || !utf8.ValidString(got) {
		t.Fatalf("truncateForLog gave %d bytes (valid UTF-8: %v)", len(got), utf8.ValidString(got))
	}
	if truncateForLog("short", 200) != "short" {
		t.Fatal("a short reason should be left alone")
	}
}

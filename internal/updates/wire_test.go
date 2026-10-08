package updates

import (
	"strings"
	"testing"
	"time"
)

const documentedRequest = `{ "v": 1, "id": "upd_k3fqz2mx7abcd", "tag": "v1.3.5",
  "requested_by": "user:alice", "requested_at": "2026-10-08T09:00:00Z" }`

func TestParseRequestAcceptsTheDocumentedShape(t *testing.T) {
	got, err := ParseRequest([]byte(documentedRequest))
	if err != nil {
		t.Fatalf("ParseRequest: %v", err)
	}
	want := Request{
		V: 1, ID: "upd_k3fqz2mx7abcd", Tag: "v1.3.5", RequestedBy: "user:alice",
		RequestedAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
	}
	if got.V != want.V || got.ID != want.ID || got.Tag != want.Tag || got.RequestedBy != want.RequestedBy || !got.RequestedAt.Equal(want.RequestedAt) {
		t.Fatalf("ParseRequest = %+v, want %+v", got, want)
	}
}

// The request is the one thing the less privileged side hands to root, so a
// field the helper does not know is a field somebody hoped it would act on. A
// URL, a path or a flag has to be refused loudly, not ignored.
func TestParseRequestRefusesAnUnknownField(t *testing.T) {
	for _, field := range []string{`"url": "https://example.com/zoomies"`, `"flags": ["--yes"]`, `"path": "/tmp/x"`} {
		doc := `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z",` + field + `}`
		if _, err := ParseRequest([]byte(doc)); err == nil {
			t.Errorf("a request carrying %s was accepted", field)
		}
	}
}

func TestParseRequestRefusesAnythingAfterTheDocument(t *testing.T) {
	if _, err := ParseRequest([]byte(documentedRequest + `{"v":1}`)); err == nil {
		t.Error("a second document after the first was accepted")
	}
	if _, err := ParseRequest([]byte(documentedRequest + "\n")); err != nil {
		t.Errorf("a trailing newline was refused: %v", err)
	}
}

func TestParseRequestRefusesAWireVersionItDoesNotKnow(t *testing.T) {
	for _, v := range []string{`"v": 2,`, `"v": 0,`, ``} {
		doc := `{` + v + `"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`
		_, err := ParseRequest([]byte(doc))
		if err == nil {
			t.Errorf("version field %q was accepted", v)
		}
	}
}

func TestParseRequestRefusesABadTag(t *testing.T) {
	for _, tag := range []string{
		"", "1.3.5", "v1.3", "v1.3.5.1", "V1.3.5", "v1.3.5-rc1", "v1.3.5+build", "latest", "dev",
		"v1.3.5\n", " v1.3.5", "v1.3.5 --yes", "../v1.3.5", "v1.3.5/../../x", "v1.3.x",
	} {
		doc := `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":` + quote(tag) + `,"requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`
		if _, err := ParseRequest([]byte(doc)); err == nil {
			t.Errorf("tag %q was accepted", tag)
		}
	}
}

func TestParseRequestRefusesABodyOverFourKilobytes(t *testing.T) {
	// Leading spaces keep the document valid JSON, so the size is the only
	// thing wrong with it.
	pad := func(n int) []byte {
		return []byte(strings.Repeat(" ", n-len(documentedRequest)) + documentedRequest)
	}
	if _, err := ParseRequest(pad(MaxRequestBytes)); err != nil {
		t.Errorf("a body of exactly %d bytes was refused: %v", MaxRequestBytes, err)
	}
	body := pad(MaxRequestBytes + 1)
	if len(body) != 4097 {
		t.Fatalf("test body is %d bytes, want 4097", len(body))
	}
	if _, err := ParseRequest(body); err == nil {
		t.Error("a body of 4097 bytes was accepted")
	}
}

func TestParseRequestRefusesAnEmptyOrOverlongID(t *testing.T) {
	tooLong := "upd_" + strings.Repeat("a", 61) // 65 characters
	if len(tooLong) != 65 {
		t.Fatalf("test id is %d characters, want 65", len(tooLong))
	}
	for _, id := range []string{"", tooLong, "upd_", "k3fqz2mx7abcd", "UPD_k3fqz2mx7abcd", "upd_K3FQ", "upd_k3 fq", "upd_../x", "upd_k3fq\n"} {
		doc := `{"v":1,"id":` + quote(id) + `,"tag":"v1.3.5","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`
		if _, err := ParseRequest([]byte(doc)); err == nil {
			t.Errorf("id %q was accepted", id)
		}
	}
	longest := "upd_" + strings.Repeat("a", 60) // 64 characters
	doc := `{"v":1,"id":"` + longest + `","tag":"v1.3.5","requested_by":"user:alice","requested_at":"2026-10-08T09:00:00Z"}`
	if _, err := ParseRequest([]byte(doc)); err != nil {
		t.Errorf("an id of 64 characters was refused: %v", err)
	}
}

// requested_by is never acted on, but the helper writes it into a log root
// owns. A newline in it would let the less privileged side write a line of
// root's, and an escape sequence would let it repaint the terminal reading it.
func TestParseRequestRefusesARequestedByThatCouldForgeALogLine(t *testing.T) {
	for _, by := range []string{
		strings.Repeat("a", 129), "user:alice\n", "user:alice\rforged", "user:\x00alice",
		"user:\x1b[31malice", "user:alice\x7f", "user:\talice", "user:alice\u0085",
	} {
		doc := `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":` + quote(by) + `,"requested_at":"2026-10-08T09:00:00Z"}`
		if _, err := ParseRequest([]byte(doc)); err == nil {
			t.Errorf("requested_by %q was accepted", by)
		}
	}
	for _, by := range []string{"", "user:alice", "user:zoë", strings.Repeat("a", 128)} {
		doc := `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":` + quote(by) + `,"requested_at":"2026-10-08T09:00:00Z"}`
		if _, err := ParseRequest([]byte(doc)); err != nil {
			t.Errorf("requested_by %q was refused: %v", by, err)
		}
	}
}

// A request with no time cannot be placed in the helper's log or against the
// attempt that made it, and encoding/json yields the zero time for a missing
// field without complaint.
func TestParseRequestRefusesARequestWithNoRequestedAt(t *testing.T) {
	for _, at := range []string{``, `,"requested_at":"0001-01-01T00:00:00Z"`, `,"requested_at":null`} {
		doc := `{"v":1,"id":"upd_k3fqz2mx7abcd","tag":"v1.3.5","requested_by":"user:alice"` + at + `}`
		if _, err := ParseRequest([]byte(doc)); err == nil {
			t.Errorf("requested_at %q was accepted", at)
		}
	}
}

func TestParseRequestRefusesWhatIsNotJSON(t *testing.T) {
	for _, body := range []string{"", "null", "[]", `"v1.3.5"`, "{", "not json"} {
		if _, err := ParseRequest([]byte(body)); err == nil {
			t.Errorf("%q was accepted", body)
		}
	}
}

// quote writes s as a JSON string without the encoder, so a test table can hold
// a newline or a quote and still say exactly what reached the parser.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`, "\t", `\t`, "\x00", `\u0000`, "\x1b", `\u001b`)
	return `"` + r.Replace(s) + `"`
}

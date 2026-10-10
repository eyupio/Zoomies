package prrepair

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type replies struct {
	values []string
	seen   []Message
}

func (m *replies) Answer(_ context.Context, messages []Message) (string, error) {
	m.seen = append([]Message(nil), messages...)
	if len(m.values) == 0 {
		return "", fmt.Errorf("no reply")
	}
	v := m.values[0]
	m.values = m.values[1:]
	return v, nil
}

type files map[string]File

func (f files) ReadFile(_ context.Context, name string) (File, error) { return f[name], nil }
func TestRepairPathsProtectCredentialsAndWorkflowPermissions(t *testing.T) {
	for _, p := range []string{".Git/config", ".SSH/id_rsa", ".AWS/config", "secrets/token", ".GITHUB/workflows/ci.yml", "../a", "a/../b", "/tmp/a", `a\b`, ".git/config", ".env", "dir/.env.local", "key.pem", "credentials.json", "secrets.yaml", ".ssh/id_rsa", ".github/workflows/test.yml"} {
		if AllowedPath(p, false) {
			t.Errorf("allowed %q", p)
		}
	}
	if !AllowedPath(".github/workflows/test.yml", true) || !AllowedPath("src/main.go", false) {
		t.Fatal("ordinary source or opted-in workflow blocked")
	}
}
func TestRepairReadsPinnedSourceAndPreservesExecutableMode(t *testing.T) {
	m := &replies{values: []string{`{"read":["bin/run.sh"]}`, `{"summary":"Fix exit code","files":[{"path":"bin/run.sh","content":"exit 0\n"}]}`}}
	p, e := Generate(context.Background(), m, files{"bin/run.sh": {Path: "bin/run.sh", Content: "exit 1\n", Mode: "100755"}}, Snapshot{}, "fix failure", false)
	if e != nil || len(p.Files) != 1 || p.Files[0].Mode != "100755" {
		t.Fatalf("plan %+v: %v", p, e)
	}
	if !strings.Contains(m.seen[len(m.seen)-1].Content, "exit 1") {
		t.Fatal("model did not receive requested source")
	}
}
func TestRepairRefusesUnreadPathsAndIncompleteOrSecretPlans(t *testing.T) {
	for _, reply := range []string{
		`{"summary":"fix","files":[{"path":"unread.go","content":"fixed"}]}`,
		`{"summary":"fix","files":[{"path":"main.go","content":"old"}]}`,
		`{"summary":"fix","files":[{"path":"main.go","content":"ghp_abcdefghijklmnop"}]}`,
		`{"summary":"fix","files":[{"path":"main.go","content":"fixed","mode":"120000"}]}`,
		`{"summary":"fix","files":[{"path":"main.go","content":"fixed"}]} {}`,
		`{"read":[".env"]}`, `{"cannot_fix":"infrastructure failure"}`, `{"read":["main.go"],"files":[{"path":"main.go","content":"fixed"}]}`,
	} {
		t.Run(reply, func(t *testing.T) {
			_, e := Generate(context.Background(), &replies{values: []string{reply}}, files{}, Snapshot{Files: []File{{Path: "main.go", Content: "old", Mode: "100644"}}}, "", false)
			if e == nil {
				t.Fatal("unsafe plan accepted")
			}
		})
	}
}
func TestRepairNeverSendsProtectedInitialFilesAndRedactsTokens(t *testing.T) {
	m := &replies{values: []string{`{"cannot_fix":"no evidence"}`}}
	_, _ = Generate(context.Background(), m, files{}, Snapshot{Files: []File{{Path: ".env", Content: "private-value"}, {Path: "main.go", Content: "ghp_abcdefghijklmnop"}}}, "sk-proj-abcdefghijklmnop", false)
	if len(m.seen) != 1 {
		t.Fatal("no request")
	}
	s := m.seen[0].Content
	if strings.Contains(s, "private-value") || strings.Contains(s, "ghp_") || strings.Contains(s, "sk-proj-") {
		t.Fatalf("private data sent: %s", s)
	}
}
func TestRepairStopsAfterItsStepBudget(t *testing.T) {
	m := &replies{}
	for i := 0; i < MaxSteps; i++ {
		m.values = append(m.values, `{"read":["a"]}`)
	}
	_, e := Generate(context.Background(), m, files{"a": {Path: "a"}}, Snapshot{}, "", false)
	if e == nil || !strings.Contains(e.Error(), "step limit") {
		t.Fatalf("%v", e)
	}
}

func TestRepairRedactionRemovesTheWholePrivateKeyRatherThanJustItsHeader(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nPRIVATE_CONTENT\n-----END RSA PRIVATE KEY-----"
	got := Redact("before\n" + key + "\nafter")
	if strings.Contains(got, "PRIVATE_CONTENT") || strings.Contains(got, "PRIVATE KEY") || !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Fatalf("incomplete redaction: %q", got)
	}
	if strings.Contains(Redact("-----BEGIN PRIVATE KEY-----\nPRIVATE_CONTENT"), "PRIVATE_CONTENT") {
		t.Fatal("unterminated key leaked")
	}
}

package auth

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/events"
	"github.com/eyupio/zoomies/internal/store"
)

// secretful is a struct shaped like the ones the API actually audits: a couple
// of harmless fields, a secret at the top level, a secret one level down, and a
// map whose keys carry the secret names.
type secretful struct {
	Name     string            `json:"name"`
	TokenID  string            `json:"token_id"`
	Password string            `json:"password"`
	Count    int               `json:"count"`
	Nested   nested            `json:"nested"`
	Env      map[string]string `json:"env"`
	Peers    []nested          `json:"peers"`
	Hidden   string            `json:"-"`
}

type nested struct {
	Endpoint     string `json:"endpoint"`
	ClientSecret string `json:"client_secret"`
	PrivateKey   string `json:"private_key"`
	Empty        string `json:"webhook_secret"`
}

func TestRedactBlanksNestedSecrets(t *testing.T) {
	in := secretful{
		Name:     "acme",
		TokenID:  "tok_123",
		Password: "hunter2",
		Count:    3,
		Nested:   nested{Endpoint: "https://example.com", ClientSecret: "sh", PrivateKey: "-----BEGIN"},
		Env:      map[string]string{"HOME": "/home/runner", "GITHUB_TOKEN": "ghs_1", "region": "eu"},
		Peers:    []nested{{Endpoint: "https://b.example.com", ClientSecret: "shh"}},
		Hidden:   "never rendered",
	}

	out, ok := Redact(in).(map[string]any)
	if !ok {
		t.Fatalf("Redact returned %T; want a map", Redact(in))
	}

	if out["name"] != "acme" || out["count"] != 3 {
		t.Errorf("harmless fields were changed: %#v", out)
	}
	// An ID names a credential, it is not one; blanking it would leave an
	// audit entry that says something happened to some token.
	if out["token_id"] != "tok_123" {
		t.Errorf("token_id = %v; want it left alone", out["token_id"])
	}
	if out["password"] != redactedMarker {
		t.Errorf("password = %v; want it redacted", out["password"])
	}
	if _, present := out["Hidden"]; present {
		t.Error(`a json:"-" field reached the audit payload`)
	}

	nestedOut := out["nested"].(map[string]any)
	if nestedOut["endpoint"] != "https://example.com" {
		t.Errorf("nested endpoint = %v; want it left alone", nestedOut["endpoint"])
	}
	if nestedOut["client_secret"] != redactedMarker || nestedOut["private_key"] != redactedMarker {
		t.Errorf("nested secrets survived: %#v", nestedOut)
	}
	// An empty secret stays empty: "[redacted]" would claim a secret is set.
	if nestedOut["webhook_secret"] != "" {
		t.Errorf("empty secret = %v; want it left empty", nestedOut["webhook_secret"])
	}

	env := out["env"].(map[string]any)
	if env["HOME"] != "/home/runner" || env["region"] != "eu" {
		t.Errorf("harmless environment entries were changed: %#v", env)
	}
	if env["GITHUB_TOKEN"] != redactedMarker {
		t.Errorf("GITHUB_TOKEN = %v; want it redacted", env["GITHUB_TOKEN"])
	}

	peers := out["peers"].([]any)
	if len(peers) != 1 || peers[0].(map[string]any)["client_secret"] != redactedMarker {
		t.Errorf("secrets inside a slice survived: %#v", peers)
	}
}

func TestRedactHandlesDomainTypes(t *testing.T) {
	now := time.Date(2025, 3, 4, 10, 0, 0, 0, time.UTC)
	u := &store.User{
		ID: "usr_1", Username: "alice", Role: store.RoleAdmin,
		PasswordHash: "$argon2id$...", CreatedAt: now,
	}
	out := Redact(u).(map[string]any)
	if out["username"] != "alice" || out["role"] != store.RoleAdmin {
		t.Errorf("user fields were mangled: %#v", out)
	}
	if _, present := out["password_hash"]; present {
		t.Error("the password hash reached the audit payload")
	}
	if got, ok := out["created_at"].(time.Time); !ok || !got.Equal(now) {
		t.Errorf("created_at = %#v; want the time preserved so it renders as RFC 3339", out["created_at"])
	}

	// A sealed blob is reduced to its length: the ciphertext is of no use in an
	// audit entry and it is still key material.
	inst := &store.Installation{ID: "ins_1", Target: "acme", PrivateKeyEnc: []byte("ciphertext")}
	instOut := Redact(inst).(map[string]any)
	if instOut["target"] != "acme" {
		t.Errorf("target = %v", instOut["target"])
	}
	for k, v := range instOut {
		if s, ok := v.(string); ok && s == "ciphertext" {
			t.Errorf("field %q leaked sealed key material", k)
		}
	}

	// A Duration keeps its readable form rather than a nanosecond count.
	p := &store.Pool{Name: "linux", IdleTimeout: store.Duration(5 * time.Minute)}
	b, err := json.Marshal(Redact(p))
	if err != nil {
		t.Fatal(err)
	}
	if want := `"idle_timeout":"5m0s"`; !strings.Contains(string(b), want) {
		t.Errorf("marshalled pool = %s; want it to contain %s", b, want)
	}
}

func TestRedactKeepsFlagsAndSurvivesNil(t *testing.T) {
	// store.Setting.Secret is a flag whose name matches the sensitive list.
	// Blanking a bool makes the entry unreadable and protects nothing.
	out := Redact(store.Setting{Key: "oidc.client_secret", Value: "", Secret: true}).(map[string]any)
	if out["secret"] != true {
		t.Errorf("secret flag = %v; want true", out["secret"])
	}
	if Redact(nil) != nil {
		t.Error("Redact(nil) should be nil")
	}
	var p *secretful
	if Redact(p) != nil {
		t.Error("Redact of a nil pointer should be nil")
	}
}

func TestIsSensitiveName(t *testing.T) {
	sensitive := []string{"password", "PasswordHash", "new_password", "token", "TokenHash",
		"api_token", "client_secret", "WebhookSecretEnc", "private_key", "jit_config",
		"encryption_key", "GITHUB_TOKEN"}
	for _, n := range sensitive {
		if !isSensitiveName(n) {
			t.Errorf("%q should be treated as a secret", n)
		}
	}
	harmless := []string{"name", "token_id", "TokenPrefix", "token_count", "created_at",
		"encryption_key_file", "secret_name", "installation_id", "labels"}
	for _, n := range harmless {
		if isSensitiveName(n) {
			t.Errorf("%q should not be treated as a secret", n)
		}
	}
}

func TestDiffKeepsOnlyWhatChanged(t *testing.T) {
	before := map[string]any{
		"name":        "linux-x64",
		"max_runners": 4,
		"ephemeral":   true,
		"resources":   map[string]any{"cpus": 2, "memory_mb": 4096},
	}
	after := map[string]any{
		"name":        "linux-x64",
		"max_runners": 8,
		"ephemeral":   true,
		"resources":   map[string]any{"cpus": 2, "memory_mb": 8192},
	}

	b, a := Diff(before, after)
	bm, am := b.(map[string]any), a.(map[string]any)
	wantBefore := map[string]any{"max_runners": 4, "resources": map[string]any{"memory_mb": 4096}}
	wantAfter := map[string]any{"max_runners": 8, "resources": map[string]any{"memory_mb": 8192}}
	if !reflect.DeepEqual(bm, wantBefore) {
		t.Errorf("before diff = %#v; want %#v", bm, wantBefore)
	}
	if !reflect.DeepEqual(am, wantAfter) {
		t.Errorf("after diff = %#v; want %#v", am, wantAfter)
	}
}

func TestDiffOnEqualValuesIsEmpty(t *testing.T) {
	p := store.Pool{Name: "linux", MaxRunners: 4}
	if b, a := Diff(p, p); b != nil || a != nil {
		t.Errorf("Diff of identical values = %v, %v; want nil, nil", b, a)
	}
	if b, a := Diff("one", "two"); b != "one" || a != "two" {
		t.Errorf("Diff of scalars = %v, %v; want one, two", b, a)
	}
	if b, a := Diff(nil, store.Pool{Name: "linux"}); b != nil || a == nil {
		t.Errorf("Diff(nil, pool) = %v, %v; want nil and the whole pool", b, a)
	}
}

func TestDiffTracksAddedAndRemovedKeys(t *testing.T) {
	b, a := Diff(map[string]any{"gone": 1}, map[string]any{"added": 2})
	bm, am := b.(map[string]any), a.(map[string]any)
	if _, ok := bm["gone"]; !ok {
		t.Errorf("before diff = %#v; want the removed key", bm)
	}
	if _, ok := am["added"]; !ok {
		t.Errorf("after diff = %#v; want the added key", am)
	}
}

func TestAuditorRecordsAndPublishes(t *testing.T) {
	c := newClock()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:", Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.New()
	s := New(st, config.Default(), bus, WithClock(c.Now))
	// The subscription is closed by cancelling the test's context rather than
	// by a cleanup: events.Subscription.Close is not safe to call twice
	// concurrently, and the context path is the one the API server uses.
	sub := bus.Subscribe(t.Context(), events.SubscribeOptions{Kinds: []events.Kind{events.KindAudit}})

	id := &Identity{Kind: KindUser, ID: "usr_1", Name: "alice", Role: store.RoleAdmin, IP: "10.0.0.1"}
	before := store.Pool{Name: "linux", MaxRunners: 4, Env: store.StringMap{"GITHUB_TOKEN": "ghs_1"}}
	after := store.Pool{Name: "linux", MaxRunners: 8, Env: store.StringMap{"GITHUB_TOKEN": "ghs_1"}}
	s.Auditor().Updated(t.Context(), id, "pool", "pool_1", before, after)

	rows, total, err := st.ListAudit(t.Context(), store.AuditFilter{}, store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("audit rows = %d; want 1", total)
	}
	row := rows[0]
	if row.Action != "pool.update" || row.ActorName != "alice" || row.ActorKind != KindUser || row.IP != "10.0.0.1" {
		t.Errorf("audit row = %+v", row)
	}
	if !strings.Contains(row.After, "max_runners") || strings.Contains(row.After, "ghs_1") {
		t.Errorf("after payload = %s; want only the changed field, with no secret", row.After)
	}
	if strings.Contains(row.Before, "name") {
		t.Errorf("before payload = %s; want only the fields that changed", row.Before)
	}

	select {
	case ev := <-sub.C:
		if ev.Kind != events.KindAudit {
			t.Errorf("event kind = %s", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no audit event was published to the bus")
	}

	// An update that changes nothing writes no row: an audit log full of
	// no-ops is one nobody reads.
	s.Auditor().Updated(t.Context(), id, "pool", "pool_1", after, after)
	if _, total, err = st.ListAudit(t.Context(), store.AuditFilter{}, store.Page{}); err != nil || total != 1 {
		t.Errorf("audit rows after a no-op update = %d, %v; want 1", total, err)
	}
}

func TestAuditorHandlesAMissingIdentity(t *testing.T) {
	c := newClock()
	st, err := store.Open(t.Context(), store.Options{Path: ":memory:", Now: c.Now})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// A nil bus must not panic: the CLI and the installer use the auditor
	// without a UI attached.
	a := NewAuditor(st, nil, nil)
	if err := a.Record(t.Context(), nil, "runner.create", "runner", "run_1", nil, nil); err != nil {
		t.Fatalf("Record: %v", err)
	}
	rows, _, err := st.ListAudit(t.Context(), store.AuditFilter{}, store.Page{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAudit = %v, %v", rows, err)
	}
	if rows[0].ActorKind != KindSystem {
		t.Errorf("actor kind = %q; want %q", rows[0].ActorKind, KindSystem)
	}
}

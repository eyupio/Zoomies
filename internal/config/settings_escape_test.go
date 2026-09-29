package config

import (
	"reflect"
	"testing"
)

// A comma inside a list item or a label value is ordinary: NO_PROXY is a comma
// separated list, and an LDAP-style group name is `CN=Admins,OU=Groups`. The
// stored text joins items with commas, so a value that carried one used to be
// split on the next restart -- the whole runners.env row was refused and the
// fleet silently lost its proxy, or a group name became two groups.
func TestAValueContainingACommaSurvivesItsStoredText(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value any
		text  string
	}{
		{"runners.env", map[string]any{"HTTP_PROXY": "http://proxy:3128", "NO_PROXY": "localhost,127.0.0.1"},
			`HTTP_PROXY=http://proxy:3128,NO_PROXY=localhost\,127.0.0.1`},
		{"runners.env", map[string]any{"FOO": "a,b=c"}, `FOO=a\,b=c`},
		{"oidc.admin_groups", []any{"CN=Admins,OU=Groups", "plain"}, `CN=Admins\,OU=Groups,plain`},
		{"agent.labels", map[string]any{"path": `C:\tools\,x`}, `path=C:\\tools\\\,x`},
		{"oidc.scopes", []any{`ends-with-backslash\`, "next"}, `ends-with-backslash\\,next`},
	} {
		s, ok := LookupSetting(tc.key)
		if !ok {
			t.Fatalf("%s: not a setting", tc.key)
		}
		c := Default()
		if _, err := c.SetValue(tc.key, tc.value); err != nil {
			t.Fatalf("%s: %v", tc.key, err)
		}
		before, _ := c.Value(tc.key)
		text := Text(s, before)
		if text != tc.text {
			t.Errorf("%s stored as %q, want %q", tc.key, text, tc.text)
		}
		d := Default()
		if _, err := d.SetValueString(tc.key, text); err != nil {
			t.Errorf("%s: %q does not parse back: %v", tc.key, text, err)
			continue
		}
		after, _ := d.Value(tc.key)
		if !reflect.DeepEqual(before, after) {
			t.Errorf("%s: %#v stored as %q and came back as %#v", tc.key, before, text, after)
		}
	}
}

// Rows written before escaping existed, and environment variables typed by
// hand, hold no backslash-comma or double backslash. They must read exactly as
// they always did, so no migration is needed.
func TestAStoredRowWithoutEscapesReadsAsItAlwaysDid(t *testing.T) {
	c := Default()
	if _, err := c.SetValueString("agent.labels", `path=C:\tools, tier=build`); err != nil {
		t.Fatal(err)
	}
	got, _ := c.Value("agent.labels")
	want := map[string]string{"path": `C:\tools`, "tier": "build"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

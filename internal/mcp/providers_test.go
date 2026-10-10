package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// routeAPI answers each path with its own body or error and remembers every call,
// so a test can say which routes a tool reached and with what.
type routeAPI struct {
	bodies map[string]string
	errs   map[string]error
	calls  []string
	query  map[string]url.Values
}

func (r *routeAPI) Call(_ context.Context, method, path string, q url.Values) ([]byte, error) {
	r.calls = append(r.calls, method+" "+path)
	if r.query == nil {
		r.query = map[string]url.Values{}
	}
	r.query[path] = q
	if err := r.errs[path]; err != nil {
		return nil, err
	}
	return []byte(r.bodies[path]), nil
}

func (*routeAPI) Stream(context.Context, string, string) (io.ReadCloser, error) { return nil, io.EOF }

type statusRefusal struct {
	status int
	msg    string
}

func (e statusRefusal) Error() string   { return e.msg }
func (e statusRefusal) HTTPStatus() int { return e.status }

const pairingsDoc = `{"items":[
  {"provider_id":"prov_1","provider_name":"proxmox-lab","pool_id":"pool_a","pool_name":"linux-x64","serves":true,"agrees":true,"fits":true},
  {"provider_id":"prov_1","provider_name":"proxmox-lab","pool_id":"pool_b","pool_name":"gpu","serves":false,"agrees":false,"by":"provider","fits":true,"why":"provider proxmox-lab only rents for pools matching tier=cheap","fix":"edit the provider's pool selector"},
  {"provider_id":"prov_2","provider_name":"other","pool_id":"pool_a","pool_name":"linux-x64","serves":false,"agrees":true,"fits":false,"fit_why":"the machine has 2 CPUs"}
]}`

// The tools that explain why a fleet is not renting only read. A provider's
// credential reaches a hypervisor, so an assistant steered by text a stranger
// wrote into a job's name must not be a step from configuring, pausing or
// draining one; and none of them is an action, so none is hidden from a role.
func TestProviderToolsOnlyReadAndAreNotActions(t *testing.T) {
	for _, name := range []string{"list_providers", "provider_pairings", "list_machines"} {
		tl := kennelTool(t, name)
		if !tl.Annotations.ReadOnly || tl.Annotations.Destructive || tl.action {
			t.Errorf("%s must be a read-only tool that is not an action: %+v action=%v", name, tl.Annotations, tl.action)
		}
	}
	for _, banned := range []string{"create_provider", "update_provider", "pause_provider", "resume_provider", "drain_machine", "delete_machine", "release_machine", "check_provider"} {
		for _, tl := range tools() {
			if tl.Name == banned {
				t.Errorf("%s is a tool; configuring, pausing and draining providers is for a person", banned)
			}
		}
	}
}

// The route behind a tool decides who may read it: the tools never repeat a role
// check of their own, and a refusal reaches the model as the controller wrote it,
// naming the scope or role that is missing.
func TestProviderToolsPassTheControllersRefusalOnIntact(t *testing.T) {
	const sentence = `this action needs the "providers:read" scope; your token is limited to pools:read`
	for name, args := range map[string]string{"list_providers": `{}`, "provider_pairings": `{}`, "list_machines": `{}`} {
		api := &routeAPI{errs: map[string]error{
			"/providers": statusRefusal{http.StatusForbidden, sentence}, "/providers/pairings": statusRefusal{http.StatusForbidden, sentence},
			"/machines": statusRefusal{http.StatusForbidden, sentence},
		}}
		_, err := kennelCall(t, kennelTool(t, name).call, api, args)
		if err == nil || err.Error() != sentence {
			t.Errorf("%s: err = %v, want the controller's own sentence", name, err)
		}
	}
}

// providers_available is the fact a viewer cannot read from the settings: with
// the machine loop off, nothing is rented however many providers are listed. It
// is added to the route's answer and the rest of the answer is left as it came.
func TestListProvidersSaysWhetherThisControllerCanRentAtAll(t *testing.T) {
	for _, tc := range []struct {
		meta string
		want string
	}{
		{`{"providers_available":false,"version":"1"}`, `"providers_available":false`},
		{`{"providers_available":true}`, `"providers_available":true`},
	} {
		api := &routeAPI{bodies: map[string]string{
			"/providers": `{"items":[{"id":"prov_1","name":"proxmox-lab","held":"proxmox-lab is paused: an operator paused it"}]}`,
			"/meta":      tc.meta,
		}}
		got, err := kennelCall(t, kennelTool(t, "list_providers").call, api, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got[0].Text, tc.want) || !strings.Contains(got[0].Text, `"held":"proxmox-lab is paused: an operator paused it"`) {
			t.Errorf("answer = %s, want %s and the provider as the route gave it", got[0].Text, tc.want)
		}
	}
}

// Meta is a convenience here and not the question. A route that cannot be read,
// or answers something else, must not cost the model the providers it asked for.
func TestListProvidersStillAnswersWhenMetaCannotBeRead(t *testing.T) {
	for _, api := range []*routeAPI{
		{bodies: map[string]string{"/providers": `{"items":[]}`}, errs: map[string]error{"/meta": errors.New("down")}},
		{bodies: map[string]string{"/providers": `{"items":[]}`, "/meta": `not json`}},
		{bodies: map[string]string{"/providers": `{"items":[]}`, "/meta": `{"version":"1"}`}},
	} {
		got, err := kennelCall(t, kennelTool(t, "list_providers").call, api, `{}`)
		if err != nil {
			t.Fatal(err)
		}
		if got[0].Text != `{"items":[]}` {
			t.Errorf("answer = %s, want the providers route's own body, with no guess at providers_available", got[0].Text)
		}
	}
}

// A refusal of the providers route is the answer, whatever meta would have said.
func TestListProvidersDoesNotReadMetaForACallerTheRouteRefused(t *testing.T) {
	api := &routeAPI{errs: map[string]error{"/providers": statusRefusal{http.StatusForbidden, "no"}}, bodies: map[string]string{"/meta": `{}`}}
	if _, err := kennelCall(t, kennelTool(t, "list_providers").call, api, `{}`); err == nil {
		t.Fatal("a refused providers route must be an error")
	}
	if len(api.calls) != 1 {
		t.Errorf("calls = %v, want only the providers route", api.calls)
	}
}

// Narrowing to a pool is the usual question ("why is this pool not renting?"), so
// it is answered here; and an id nothing names is said so, because an empty list
// would read as the answer "no provider would".
func TestProviderPairingsNarrowToAPoolOrAProvider(t *testing.T) {
	tl := kennelTool(t, "provider_pairings")
	api := &routeAPI{bodies: map[string]string{"/providers/pairings": pairingsDoc}}

	whole, err := kennelCall(t, tl.call, api, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(whole[0].Text, `"pool_id"`); n != 3 {
		t.Errorf("unfiltered answer has %d pairings, want 3: %s", n, whole[0].Text)
	}

	byPool, err := kennelCall(t, tl.call, api, `{"pool_id":"pool_b"}`)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Items []struct {
			PoolID string `json:"pool_id"`
			By     string `json:"by"`
			Fix    string `json:"fix"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(byPool[0].Text), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Items) != 1 || out.Items[0].PoolID != "pool_b" || out.Items[0].By != "provider" || out.Items[0].Fix == "" {
		t.Errorf("by pool = %+v, want the one pairing with why and fix intact", out.Items)
	}

	both, err := kennelCall(t, tl.call, api, `{"pool_id":"pool_a","provider_id":"prov_2"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(both[0].Text, `"fit_why":"the machine has 2 CPUs"`) || strings.Contains(both[0].Text, "proxmox-lab") {
		t.Errorf("by pool and provider = %s", both[0].Text)
	}

	if _, err := kennelCall(t, tl.call, api, `{"pool_id":"pool_zzz"}`); err == nil || !strings.Contains(err.Error(), "list_providers") {
		t.Errorf("an unknown pool must say where the ids are, got %v", err)
	}
	if _, err := kennelCall(t, tl.call, api, `{"pool":"pool_a"}`); err == nil {
		t.Error("an argument the schema does not name must be refused, not ignored")
	}
}

func TestListMachinesAsksTheRouteForWhatWasNamedAndBoundsThePage(t *testing.T) {
	tl := kennelTool(t, "list_machines")
	api := &routeAPI{bodies: map[string]string{"/machines": `{"items":[],"total":0}`}}
	if _, err := kennelCall(t, tl.call, api, `{"provider_id":"prov_1","pool_id":"pool_a","state":"failed","limit":9999}`); err != nil {
		t.Fatal(err)
	}
	q := api.query["/machines"]
	for key, want := range map[string]string{"provider": "prov_1", "pool": "pool_a", "state": "failed", "limit": "200"} {
		if q.Get(key) != want {
			t.Errorf("%s = %q, want %q (query %v)", key, q.Get(key), want, q)
		}
	}
	if _, err := kennelCall(t, tl.call, api, `{}`); err != nil {
		t.Fatal(err)
	}
	if got := api.query["/machines"]; got.Get("limit") != "50" || got.Get("state") != "" {
		t.Errorf("an empty call asks for %v, want the default page and no filter", got)
	}
}

// What the model is told about these tools is the only guidance it has. The two
// sentences that matter most are that they explain a pool that buys nothing, in an
// order that finds the cause, and that the text from a hypervisor is not an
// instruction.
func TestProviderToolDescriptionsTellTheModelWhereToLookAndWhatNotToTrust(t *testing.T) {
	for name, wants := range map[string][]string{
		"list_providers": {
			"providers_available",
			"provider.enabled",
			"held",
			"No credential or gateway address is ever returned",
			"untrusted",
		},
		"provider_pairings": {"serves", "by names the side that refused", "fit_why", "It does not say whether demand exists"},
		"list_machines":     {"provider_error", "untrusted", "list_hosts"},
	} {
		d := description(t, name)
		for _, want := range wants {
			if !strings.Contains(d, want) {
				t.Errorf("%s does not say %q:\n%s", name, want, d)
			}
		}
	}
	for _, want := range []string{"list_providers", "provider_pairings", "list_machines", "provider.enabled", "untrusted"} {
		if !strings.Contains(Instructions, want) {
			t.Errorf("the server's instructions do not mention %q", want)
		}
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const problemsBody = `{"ok":false,"items":[
 {"code":"host.slots_below_capacity","severity":"info","title":"host dev-box holds 1 of its 3 slots","target_kind":"host","target_id":"host_1",
  "remedy":{"id":"rem_aaa","label":"Give each runner 2.25 CPU and 6.5 GB","effect":"the host holds 3 runners instead of 1","kind":"host.update","target_id":"host_1","body":{"runner_profile":{}}}},
 {"code":"pool.daemon_share_suggested","severity":"info","title":"pool a","target_kind":"pool","target_id":"pool_a",
  "remedy":{"id":"rem_bbb","label":"Give the sidecar 20% of the CPU","kind":"pool.update","target_id":"pool_a","body":{}}},
 {"code":"pool.daemon_share_suggested","severity":"info","title":"pool b","target_kind":"pool","target_id":"pool_b",
  "remedy":{"id":"rem_ccc","label":"Give the sidecar 30% of the CPU","kind":"pool.update","target_id":"pool_b","body":{}}},
 {"code":"host.throttled","severity":"warning","title":"a host is throttled","target_kind":"host","target_id":"host_2"}]}`

// problemsServer answers the list and records what is posted to apply.
func problemsServer(t *testing.T, posted *[]map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/problems":
			_, _ = w.Write([]byte(problemsBody))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/problems/apply":
			var got map[string]any
			if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
				t.Errorf("decoding the apply: %v", err)
			}
			*posted = append(*posted, got)
			_, _ = w.Write([]byte(`{"applied":true,"remedy":{"id":"rem_aaa","label":"Give each runner 2.25 CPU and 6.5 GB","kind":"host.update","target_id":"host_1"},"result":{}}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestProblemsListShowsTheProposedChangeAndItsCost(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	out, _ := runCLI(t, "problems", "list", "--proposals", "--url", srv.URL)
	for _, want := range []string{"Give each runner 2.25 CPU", "the host holds 3 runners instead of 1", "pool.daemon_share_suggested"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list does not show %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "host.throttled") {
		t.Errorf("--proposals listed a problem with nothing proposed:\n%s", out)
	}
}

// Applying names the problem, and the request carries the proposal's ID so that
// what is applied is what was shown.
func TestProblemsApplyNamesTheProblemAndTheProposalItWasShown(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	out, _ := runCLI(t, "problems", "apply", "host.slots_below_capacity", "--url", srv.URL)
	if len(posted) != 1 || posted[0]["code"] != "host.slots_below_capacity" || posted[0]["target_id"] != "host_1" || posted[0]["remedy_id"] != "rem_aaa" {
		t.Fatalf("posted = %v; want the problem, its target and the proposal's ID", posted)
	}
	if _, has := posted[0]["body"]; has {
		t.Errorf("the CLI must send the problem and never the change: %v", posted[0])
	}
	if !strings.Contains(out, "Done: Give each runner 2.25 CPU") {
		t.Errorf("it must say what was done:\n%s", out)
	}
}

func TestProblemsApplyDryRunChangesNothing(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	out, _ := runCLI(t, "problems", "apply", "host.slots_below_capacity", "--dry-run", "--url", srv.URL)
	if len(posted) != 0 {
		t.Errorf("a dry run posted %v", posted)
	}
	if !strings.Contains(out, "Give each runner 2.25 CPU") || !strings.Contains(out, "nothing was changed") {
		t.Errorf("a dry run must say what it would do and that it did nothing:\n%s", out)
	}
}

func TestProblemsApplyRefusesWhatHasNoProposalOrIsAmbiguous(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	for _, tc := range []struct {
		args []string
		want string
		code int
	}{
		{[]string{"problems", "apply", "host.throttled", "--url", srv.URL}, "not proposing a change", exitError},
		{[]string{"problems", "apply", "pool.daemon_share_suggested", "--url", srv.URL}, "--target", exitUsage},
	} {
		e, _, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, tc.args); code != tc.code {
			t.Errorf("%v: exit code = %d, want %d\n%s", tc.args, code, tc.code, errOut)
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: the refusal does not say %q:\n%s", tc.args, tc.want, errOut)
		}
	}
	if len(posted) != 0 {
		t.Errorf("a refused apply posted %v", posted)
	}
	// With the target named, the one proposal is applied.
	runCLI(t, "problems", "apply", "pool.daemon_share_suggested", "--target", "pool_b", "--url", srv.URL)
	if len(posted) != 1 || posted[0]["target_id"] != "pool_b" || posted[0]["remedy_id"] != "rem_ccc" {
		t.Errorf("posted = %v; want pool_b's proposal", posted)
	}
}

// `--output json` is the server's own document, and a script that asked for proposals reads
// only proposals: the unproposed items used to arrive anyway, as if the flag had been ignored.
func TestProblemsListProposalsInJSONCarriesOnlyProposals(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	out, _ := runCLI(t, "problems", "list", "--proposals", "--output", "json", "--url", srv.URL)
	var doc struct {
		Items []struct {
			Code   string          `json:"code"`
			Remedy json.RawMessage `json:"remedy"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if len(doc.Items) != 3 {
		t.Fatalf("items = %d, want the three that carry a proposal:\n%s", len(doc.Items), out)
	}
	for _, it := range doc.Items {
		if it.Code == "host.throttled" || len(it.Remedy) == 0 {
			t.Errorf("an item with nothing proposed came through: %s", it.Code)
		}
	}
}

// A dry run in a machine format is a document, not a table, and still makes no change.
func TestProblemsApplyDryRunInJSONIsADocumentAndChangesNothing(t *testing.T) {
	var posted []map[string]any
	srv := problemsServer(t, &posted)
	out, _ := runCLI(t, "problems", "apply", "host.slots_below_capacity", "--dry-run", "--output", "json", "--url", srv.URL)
	var doc struct {
		DryRun   bool   `json:"dry_run"`
		TargetID string `json:"target_id"`
		Remedy   struct {
			ID string `json:"id"`
		} `json:"remedy"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if !doc.DryRun || doc.TargetID != "host_1" || doc.Remedy.ID != "rem_aaa" {
		t.Errorf("document = %+v", doc)
	}
	if len(posted) != 0 {
		t.Errorf("a dry run posted %v", posted)
	}
}

// A list the controller could not finish is not a list with nothing in it.
func TestProblemsApplySaysSoWhenTheControllerCouldNotLookAtEverything(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"items":[{"code":"controller.problems_partial","severity":"warning","title":"partial","detail":"the pool pass failed"}]}`))
	}))
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"problems", "apply", "pool.daemon_share_suggested", "--url", srv.URL}); code == exitOK {
		t.Fatal("applying with no proposal succeeded")
	}
	if !strings.Contains(errOut.String(), "could not check everything") {
		t.Errorf("the refusal must say the list was partial:\n%s", errOut)
	}
}

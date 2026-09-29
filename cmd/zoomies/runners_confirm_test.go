package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// The controller refuses to drain a busy runner unless the caller says it
// understands the job is stopped after five minutes, and the help promises
// --yes as the way to say so. Without it the terminal had no way to finish a
// graceful drain: the only exit was `runners delete --force`, which is the
// harsher thing the operator was trying to avoid.
func TestYesReachesTheAPIAsConfirmOnEveryDrainAndDeletePath(t *testing.T) {
	type seen struct {
		path    string
		confirm string // "" when the query carries none
		body    map[string]any
	}
	cases := []struct {
		name string
		args []string
		want bool
	}{
		{"one runner drained with --yes", []string{"runners", "drain", "run_1", "--yes"}, true},
		{"one runner drained without --yes", []string{"runners", "drain", "run_1"}, false},
		{"one runner deleted with --yes", []string{"runners", "delete", "run_1", "--yes"}, true},
		{"one runner deleted without --yes", []string{"runners", "delete", "run_1"}, false},
		{"several runners drained with --yes", []string{"runners", "drain", "run_1", "run_2", "--yes"}, true},
		{"several runners drained without --yes", []string{"runners", "drain", "run_1", "run_2"}, false},
		{"several runners deleted with --yes", []string{"runners", "delete", "run_1", "run_2", "--yes"}, true},
		{"a host drained with --yes", []string{"hosts", "drain", "hst_a", "--yes"}, true},
		{"a host drained without --yes", []string{"hosts", "drain", "hst_a"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var got []seen
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case strings.HasSuffix(r.URL.Path, "/cordon"):
					_, _ = w.Write([]byte(`{"id":"hst_a","name":"vm-1","cordoned":true}`))
				case r.URL.Path == "/api/v1/runners":
					_, _ = w.Write([]byte(`{"items":[{"id":"run_1"}],"total":1}`))
				case r.URL.Path == "/api/v1/runners/bulk":
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					mu.Lock()
					got = append(got, seen{path: r.URL.Path, body: body})
					mu.Unlock()
					_, _ = w.Write([]byte(`{"results":[{"id":"run_1","ok":true}]}`))
				default:
					mu.Lock()
					got = append(got, seen{path: r.URL.Path, confirm: r.URL.Query().Get("confirm")})
					mu.Unlock()
					_, _ = w.Write([]byte(`{"id":"run_1","name":"r"}`))
				}
			}))
			defer srv.Close()

			e, _, errOut := newTestEnv(t)
			if code := dispatch(context.Background(), e, append(tc.args, "--url", srv.URL)); code != exitOK {
				t.Fatalf("exit code = %d\n%s", code, errOut)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(got) != 1 {
				t.Fatalf("requests = %+v, want exactly one drain or delete", got)
			}
			confirmed := got[0].confirm == "true"
			if c, ok := got[0].body["confirm"]; ok {
				confirmed = c == true
				if !tc.want {
					t.Errorf("the bulk body carried confirm=%v without --yes; the default has to stay refuse-if-busy", c)
				}
			}
			if confirmed != tc.want {
				t.Errorf("confirm reached the API = %v, want %v (%+v)", confirmed, tc.want, got[0])
			}
		})
	}
}

// A 409 that says "send it again with confirm=true" is no use to someone
// whose CLI has no such option, so a refused busy runner names the flag.
func TestABusyRunnerRefusedForWantOfYesSaysToRerunWithIt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"id":"run_1","ok":false,"error":"runner run_1 is busy; send it again with confirm=true"}]}`))
	}))
	defer srv.Close()

	e, out, _ := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"runners", "drain", "run_1", "run_2", "--url", srv.URL})
	if code == exitOK {
		t.Fatal("a refused drain exited 0")
	}
	if !strings.Contains(out.String(), "--yes") {
		t.Errorf("the refusal does not name --yes:\n%s", out)
	}
}

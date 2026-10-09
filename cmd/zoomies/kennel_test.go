package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files from the command's output")

const fixtures = "../../internal/kennel/offline/testdata"

// The JSON a script or a skill reads must not move under it: the golden files
// are the contract, byte for byte, and a change to them is a change to review.
func TestKennelCheckPrintsTheGoldenReportForEachFixtureTree(t *testing.T) {
	for _, name := range []string{"clean", "findings", "hostile"} {
		t.Run(name, func(t *testing.T) {
			// The root is passed with forward slashes on every platform, so the
			// report, which echoes it, reads the same on Windows and the golden
			// file is one file.
			root := path.Join(fixtures, name)
			out, errOut, _ := runCLIFailing(t, "kennel", "check", "--output", "json", "--prompts", root)
			if errOut != "" && !strings.Contains(errOut, "finding") {
				t.Errorf("stderr: %s", errOut)
			}
			// The root is where the command was pointed, which is a path on this
			// machine; the golden file holds the fixture's name instead, and
			// every other byte is the command's own, in its own order.
			rootLine := `  "root": "` + root + `",`
			if !strings.Contains(out, rootLine+"\n") {
				t.Fatalf("the output does not carry the root as printed:\n%s", out)
			}
			got := []byte(strings.Replace(out, rootLine, `  "root": "`+name+`",`, 1))
			golden := filepath.Join("testdata", "kennel", name+".golden.json")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("output differs from %s:\n%s", golden, got)
			}
			if name == "hostile" && strings.Contains(out, "IGNORE") {
				t.Error("the hostile file name is in the output")
			}
		})
	}
}

func TestKennelCheckExitCodesFollowTheFindingsAndTheSeverityAsked(t *testing.T) {
	for _, tt := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{"no workflows", []string{filepath.Join(fixtures, "empty")}, 1, "no .github/workflows"},
		{"clean", []string{filepath.Join(fixtures, "clean")}, 0, "Nothing found"},
		{"findings", []string{filepath.Join(fixtures, "findings")}, 4, "ci.no_timeout"},
		{"only errors asked", []string{"--severity", "error", filepath.Join(fixtures, "findings")}, 0, "Nothing found at error"},
		{"one code asked", []string{"--code", "ci.no_timeout", filepath.Join(fixtures, "findings")}, 4, "ci.no_timeout"},
		{"default is here", nil, 1, "no .github/workflows"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if tt.args == nil {
				t.Chdir(t.TempDir())
			}
			out, errOut, code := runCLIFailing(t, append([]string{"kennel", "check"}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit = %d, want %d\n%s%s", code, tt.code, out, errOut)
			}
			if !strings.Contains(out+errOut, tt.want) {
				t.Errorf("output lacks %q:\n%s%s", tt.want, out, errOut)
			}
			if tt.name == "one code asked" && strings.Contains(out, "exposure.target_checkout_pr_head") {
				t.Errorf("a code not asked for is in the output:\n%s", out)
			}
		})
	}
}

// Nothing leaves the machine: a controller is consulted only when asked.
func TestKennelCheckNeverTalksToAControllerUnlessAsked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("the check made a request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	e, out, errOut := newTestEnv(t)
	if code := dispatch(t.Context(), e, []string{"kennel", "check", "--url", srv.URL, "--token", "zt_test", filepath.Join(fixtures, "findings")}); code != 4 {
		t.Errorf("exit = %d\n%s%s", code, out, errOut)
	}
}

const kennelRepoDoc = `{"id":"kcr_1","name":"acme/api","visibility":"private","state":"attention","evaluated_at":"2026-10-09T08:00:00Z","next_due_at":null,
  "counts":{"error":0,"warning":1,"info":0,"waived":0},"complete":true,
  "coverage":[{"source":"fleet","state":"ok","reason":""},{"source":"metadata","state":"ok","reason":""}],
  "findings":[{"code":"capacity.unserved_label","severity":"warning","subject":"","title":"Jobs waited for a label no pool serves","detail":"2 jobs waited.","fix":"Add a pool.","evidence":[{"kind":"pool","ref":"pool_1","label":"zoomies-x"}],"prompt":"Fix the Kennel Club finding ` + "`capacity.unserved_label`" + ` in this repository.\n"},
   {"code":"ci.no_timeout","severity":"warning","subject":"2d6d5c6c4664","title":"Jobs have no explicit timeout","detail":"1 job.","fix":"Set it.","evidence":[{"kind":"file","ref":"2d6d5c6c46647250349ea137c8d8f61f59fd85b4","job_index":0,"line":5}],"prompt":"Fix the Kennel Club finding ` + "`ci.no_timeout`" + ` in this repository.\n"}],
  "waived":[],"lapsed":[],"skipped":[],"disabled":[],"files":[{"sha":"2d6d5c6c46647250349ea137c8d8f61f59fd85b4","path":".github/workflows/ci.yml"}],
  "tracking":{"tracked":true}}`

// Every other verb is a thin reader of a route: it asks exactly what the route
// takes, lays the answer out, and with --output json hands the document over
// unchanged, so the CLI and the page cannot disagree.
func TestKennelVerbsAreThinReadersOfTheRoutes(t *testing.T) {
	overview := `{"enabled":true,"scope":"everything","repositories":3,"not_tracked":1,"states":{"pending":0,"partial":1,"attention":1,"best_in_show":1},
	  "counts":{"error":2,"warning":1,"info":0,"waived":1},"checks":[],"attention":[{"id":"kcr_1","name":"acme/api","visibility":"public","state":"attention","counts":{"error":2,"warning":0,"info":0,"waived":0}}],
	  "coverage":[],"unavailable":[],"oldest_evaluation":null,"disabled_checks":["capacity"]}`
	list := `{"items":[` + kennelRepoDoc + `],"total":7,"limit":1,"offset":0}`
	checks := `[{"code":"ci.no_timeout","area":"ci","severity":"warning","detects":"A job has no timeout.","fix":"Set it.","verify":"Recheck.","docs":"kennel-club.md#ci-no_timeout","needs":[],"disabled":false},
	  {"code":"capacity.unserved_label","area":"capacity","severity":"warning","detects":"Jobs waited.","fix":"Add a pool.","verify":"Recheck.","docs":"kennel-club.md#capacity-unserved_label","needs":[],"disabled":true}]`
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/kennel":
			_, _ = w.Write([]byte(overview))
		case "/api/v1/kennel/repositories":
			_, _ = w.Write([]byte(list))
		case "/api/v1/kennel/repositories/kcr_1":
			_, _ = w.Write([]byte(kennelRepoDoc))
		case "/api/v1/kennel/checks":
			_, _ = w.Write([]byte(checks))
		case "/api/v1/kennel/repositories/kcr_1/recheck":
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(kennelRepoDoc))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	for _, tt := range []struct {
		name  string
		args  []string
		path  string
		wants []string
	}{
		{"overview", []string{"overview"}, "GET /api/v1/kennel", []string{"3 repositories", "Needs attention", "acme/api", "capacity"}},
		{"repositories", []string{"repositories", "--state", "attention", "--q", "acme", "--limit", "1"}, "GET /api/v1/kennel/repositories?limit=1&offset=0&q=acme&state=attention", []string{"ID", "REPOSITORY", "STANDING", "kcr_1", "acme/api", "attention", "Showing 1-1 of 7"}},
		{"repository", []string{"repository", "kcr_1", "--prompts"}, "GET /api/v1/kennel/repositories/kcr_1", []string{"acme/api", "attention", "WARNING  ci.no_timeout", "file .github/workflows/ci.yml:5 (job 0)", "pool zoomies-x", "Fix the Kennel Club finding `ci.no_timeout`"}},
		{"checks", []string{"checks"}, "GET /api/v1/kennel/checks", []string{"CODE", "AREA", "SEVERITY", "DISABLED", "ci.no_timeout", "capacity.unserved_label", "yes"}},
		{"recheck", []string{"recheck", "kcr_1"}, "POST /api/v1/kennel/repositories/kcr_1/recheck", []string{"Recheck asked for", "budget allows"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			asked = nil
			out, _ := runCLI(t, append(append([]string{"kennel"}, tt.args...), "--url", srv.URL)...)
			if len(asked) != 1 || asked[0] != tt.path {
				t.Errorf("asked %v, want %q", asked, tt.path)
			}
			for _, want := range tt.wants {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if tt.name == "recheck" {
				return
			}
			raw, _ := runCLI(t, append(append([]string{"kennel"}, tt.args...), "--url", srv.URL, "--output", "json")...)
			var a, b any
			if json.Unmarshal([]byte(raw), &a) != nil {
				t.Fatalf("--output json is not JSON:\n%s", raw)
			}
			_ = json.Unmarshal([]byte(map[string]string{"overview": overview, "repositories": list, "repository": kennelRepoDoc, "checks": checks}[tt.name]), &b)
			if ja, _ := json.Marshal(a); string(ja) != string(must(json.Marshal(b))) {
				t.Errorf("--output json is not the route's document:\n%s", raw)
			}
		})
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestKennelRecheckSaysWhenToTryAgain(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(http.StatusTooManyRequests)
		// The controller's own sentence: the CLI prints what it says, so the
		// terminal and the page tell the same time.
		_, _ = w.Write([]byte(`{"error":{"code":"kennel_cooldown","message":"this repository was asked to be read again a moment ago; it can be asked again at 08:05:00 UTC"}}`))
	}))
	t.Cleanup(srv.Close)
	out, errOut, code := runCLIFailing(t, "kennel", "recheck", "kcr_1", "--url", srv.URL)
	if code != exitError || !strings.Contains(out+errOut, "can be asked again at 08:05:00 UTC") {
		t.Errorf("exit %d, output:\n%s%s", code, out, errOut)
	}
}

// With a controller named, the fleet's findings join the report, marked as
// the controller's; a code the offline check already judged is not repeated.
func TestKennelCheckWithAControllerAppendsTheFleetsFindings(t *testing.T) {
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/kennel/repositories":
			_, _ = w.Write([]byte(`{"items":[{"id":"kcr_0","name":"acme/api-docs"},` + kennelRepoDoc + `],"total":2,"limit":50,"offset":0}`))
		case "/api/v1/kennel/repositories/kcr_1":
			_, _ = w.Write([]byte(kennelRepoDoc))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	out, errOut, code := runCLIFailing(t, "kennel", "check", "--output", "json", "--controller", "acme/api", "--url", srv.URL, filepath.Join(fixtures, "findings"))
	if code != kennelExitFindings {
		t.Fatalf("exit %d\n%s%s", code, out, errOut)
	}
	if len(asked) != 2 || !strings.Contains(asked[0], "q=acme%2Fapi") || !strings.Contains(asked[0], "limit=50") || asked[1] != "/api/v1/kennel/repositories/kcr_1" {
		t.Errorf("asked %v", asked)
	}
	var r struct {
		Findings []struct {
			Code string `json:"code"`
			From string `json:"from"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, f := range r.Findings {
		codes = append(codes, f.Code+"/"+f.From)
	}
	if want := "ci.no_timeout/,exposure.target_checkout_pr_head/,capacity.unserved_label/controller"; strings.Join(codes, ",") != want {
		t.Errorf("findings = %v, want %s", codes, want)
	}
}

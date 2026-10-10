package proxmox

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/version"
)

// A hypervisor credential in the clear is worse than an agent token in the
// clear: it can create and destroy virtual machines. There is no setting that
// permits it, which is why this is a construction-time refusal and not a
// warning.
func TestPlainHTTPIsRefusedForAHypervisor(t *testing.T) {
	tests := []struct {
		name     string
		endpoint string
		wantErr  string
	}{
		{"a plain HTTP cluster", "http://pve-1.example.com:8006", "plain HTTP"},
		{"a plain HTTP cluster by address", "http://10.0.0.10:8006", "plain HTTP"},
		{"another scheme entirely", "ssh://pve-1.example.com", "https://"},
		{"nothing at all", "  ", "no endpoint"},
		{"loopback, where nothing leaves the box", "http://127.0.0.1:8006", ""},
		{"the usual form", "https://pve-1.example.com:8006", ""},
		{"a bare hostname, which gets https and the port", "pve-1.example.com", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := New(Options{Endpoint: tc.endpoint, TokenID: tokenID, Secret: tokenSecret})
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("New(%q): %v", tc.endpoint, err)
			case tc.wantErr == "":
				if !strings.Contains(c.base, APIPath) {
					t.Errorf("base %q does not carry %s", c.base, APIPath)
				}
			case err == nil:
				t.Fatalf("New(%q) was accepted; it must be refused", tc.endpoint)
			case !strings.Contains(err.Error(), tc.wantErr):
				t.Errorf("New(%q) said %q, which does not mention %q", tc.endpoint, err, tc.wantErr)
			}
		})
	}
}

// A bare hostname is what an operator types, and the port is a detail of ours.
func TestABareHostnameGetsTheProxmoxPort(t *testing.T) {
	c, err := New(Options{Endpoint: "pve-1.example.com", TokenID: tokenID, Secret: tokenSecret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "https://pve-1.example.com:" + DefaultPort + APIPath; c.base != want {
		t.Errorf("base = %q, want %q", c.base, want)
	}
}

// An operator who pastes the address out of a browser brings the API path with
// it, and joining it twice would 404 every call with a path they never typed.
func TestAnEndpointThatAlreadyCarriesTheAPIPathIsNotDoubled(t *testing.T) {
	c, err := New(Options{Endpoint: "https://pve-1:8006/api2/json/", TokenID: tokenID, Secret: tokenSecret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if want := "https://pve-1:8006" + APIPath; c.base != want {
		t.Errorf("base = %q, want %q", c.base, want)
	}
}

// The token id and its secret are two fields, and pasting one into the other is
// the mistake everybody makes once. Each refusal says which half is wrong.
func TestATokenThatIsNotATokenIsRefusedWithTheFormItShouldHave(t *testing.T) {
	tests := []struct {
		name, id, secret, want string
	}{
		{"the whole credential in the id field", "zoomies@pve!fleet=" + tokenSecret, tokenSecret, "pasted together"},
		{"no realm or token name", "zoomies", tokenSecret, "user@realm!tokenid"},
		{"no secret", tokenID, "", "UUID"},
		{"the right shape", tokenID, tokenSecret, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(Options{Endpoint: "https://pve-1:8006", TokenID: tc.id, Secret: tc.secret})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("New: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("New said %v, which does not mention %q", err, tc.want)
			}
		})
	}
}

// The whole point of feeding the fake's own certificate in as the CA is that
// verification is really exercised. If the client ever stopped verifying, the
// first case here would start passing.
func TestAVerifiedConnectionNeedsTheClusterCA(t *testing.T) {
	f := newFakePVE(t, nil)

	withoutCA, err := New(Options{Endpoint: f.URL, TokenID: tokenID, Secret: tokenSecret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = withoutCA.Version(context.Background())
	if err == nil {
		t.Fatal("a cluster whose certificate signs for nothing we trust was accepted")
	}
	if got := provider.KindOf(err); got != provider.FailureUnreachable {
		t.Errorf("kind = %q, want %q", got, provider.FailureUnreachable)
	}
	if !strings.Contains(err.Error(), "pve-root-ca.pem") {
		t.Errorf("the refusal does not name the file to copy: %v", err)
	}

	withCA := f.client(t)
	if _, err := withCA.Version(context.Background()); err != nil {
		t.Fatalf("with the cluster CA: %v", err)
	}

	insecure, err := New(Options{Endpoint: f.URL, TokenID: tokenID, Secret: tokenSecret, Insecure: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := insecure.Version(context.Background()); err != nil {
		t.Fatalf("with verification off: %v", err)
	}
}

// A CA field holding the key rather than the certificate is the other half of
// that mistake, and the message has to say which file to copy.
func TestACAThatIsNotACertificateSaysWhichFileToCopy(t *testing.T) {
	_, err := New(Options{Endpoint: "https://pve-1:8006", TokenID: tokenID, Secret: tokenSecret,
		CAPEM: "-----BEGIN PRIVATE KEY-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END PRIVATE KEY-----\n"})
	if err == nil {
		t.Fatal("a private key was accepted as a certificate authority")
	}
	for _, want := range []string{"/etc/pve/pve-root-ca.pem", "private key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// Constructing a client must not depend on the cluster being up, for the reason
// backend.Probe reports an absent Docker rather than failing to construct: a
// controller that would not start while a hypervisor rebooted could not even
// show an operator why.
func TestTheClientDoesNotDialUntilItIsAsked(t *testing.T) {
	addr := deadAddress(t)
	c, err := New(Options{Endpoint: "https://" + addr, TokenID: tokenID, Secret: tokenSecret, Insecure: true})
	if err != nil {
		t.Fatalf("New against a closed port: %v", err)
	}
	if c.http.Timeout != 0 {
		// A client-wide timeout cannot tell a slow clone from a status read, so
		// every call carries its own deadline instead.
		t.Errorf("http.Client.Timeout = %v, want none", c.http.Timeout)
	}
	_, err = c.Version(context.Background())
	if got := provider.KindOf(err); got != provider.FailureUnreachable {
		t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureUnreachable, err)
	}
	if !strings.Contains(err.Error(), "nothing is listening") {
		t.Errorf("the failure does not say what is wrong: %v", err)
	}
}

// One header authenticates every call: no ticket, no CSRF token, no login round
// trip. That is why there is no cached credential in this package to refresh,
// and a test pins it so nobody adds one.
func TestEveryRequestCarriesTheTokenAndTheUserAgent(t *testing.T) {
	f := newFakePVE(t, nil)
	if _, err := f.client(t).Version(context.Background()); err != nil {
		t.Fatalf("Version: %v", err)
	}
	r := f.request(http.MethodGet, v+"/version")
	if r == nil {
		t.Fatal("no request was recorded")
	}
	if want := "PVEAPIToken=" + tokenID + "=" + tokenSecret; r.Header.Get("Authorization") != want {
		t.Errorf("Authorization = %q, want %q", r.Header.Get("Authorization"), want)
	}
	if got := r.Header.Get("User-Agent"); got != version.UserAgent() {
		t.Errorf("User-Agent = %q, want %q", got, version.UserAgent())
	}
	if got := r.Header.Get("CSRFPreventionToken"); got != "" {
		t.Errorf("a CSRF header was sent (%q); token authentication needs none", got)
	}
	if got := r.Header.Get("Cookie"); got != "" {
		t.Errorf("a cookie was sent (%q); token authentication needs none", got)
	}
}

// The classification the fleet's money rests on. A request that went out and
// was never answered may well have created a virtual machine, so it is an
// unknown outcome and never retried; one that never left created nothing.
func TestATimeoutAfterTheRequestWentOutIsAmbiguous(t *testing.T) {
	t.Run("a clone whose answer never came back", func(t *testing.T) {
		f := newFakePVE(t, nil)
		f.SetStall(http.MethodPost, "/clone")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		_, err := f.client(t).CloneVM(ctx, "pve-1", 9000, CloneRequest{NewID: 143, Name: "zoomies-mach-abc"})
		if got := provider.KindOf(err); got != provider.FailureAmbiguous {
			t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureAmbiguous, err)
		}
		if provider.Retryable(err) {
			t.Error("an unknown outcome was reported as retryable; retrying it is how one machine becomes two")
		}
	})

	t.Run("a read whose answer never came back changed nothing", func(t *testing.T) {
		f := newFakePVE(t, nil)
		f.SetStall(http.MethodGet, "/version")
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		_, err := f.client(t).Version(ctx)
		if got := provider.KindOf(err); got != provider.FailureUnreachable {
			t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureUnreachable, err)
		}
		if !provider.Retryable(err) {
			t.Error("a read that timed out is worth trying again")
		}
	})

	t.Run("a request that never left is only unreachable", func(t *testing.T) {
		c, err := New(Options{Endpoint: "https://" + deadAddress(t), TokenID: tokenID, Secret: tokenSecret, Insecure: true})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		_, err = c.CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143})
		if got := provider.KindOf(err); got != provider.FailureUnreachable {
			t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureUnreachable, err)
		}
	})
}

// A caller shutting down must not see its own stop reported as an unknown
// outcome: that would quarantine a machine nothing is wrong with.
func TestACancelledCallIsTheCallersOwnDoingAndNotTheClusters(t *testing.T) {
	f := newFakePVE(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := f.client(t).CloneVM(ctx, "pve-1", 9000, CloneRequest{NewID: 143})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want a context cancellation", err)
	}
	if errors.Is(err, provider.ErrAmbiguous) || errors.Is(err, provider.ErrUnreachable) {
		t.Errorf("a cancelled call was classified as a provider failure: %v", err)
	}
}

// Every refusal has to arrive as the category the reconciler branches on,
// because that decision is made here or it is made from an error string
// somewhere it cannot be trusted.
func TestEveryRefusalBecomesTheCategoryTheReconcilerDecidesOn(t *testing.T) {
	tests := []struct {
		name     string
		mutating bool
		status   int
		message  string
		want     provider.FailureKind
	}{
		{"a rejected token", false, http.StatusUnauthorized, "authentication failure", provider.FailureAuth},
		{"a privilege the token lacks", false, http.StatusForbidden, "Permission check failed (/vms/9000, VM.Clone)", provider.FailurePermission},
		{"a guest that is not there", false, http.StatusNotFound, "Configuration file does not exist", provider.FailureNotFound},
		{"a parameter Proxmox will not take", true, http.StatusBadRequest, "invalid format - value does not look like a valid bridge name", provider.FailureConfig},
		{"an endpoint this release does not have", false, http.StatusNotImplemented, "method not implemented", provider.FailureConfig},
		{"a storage with no room", true, http.StatusInternalServerError, "unable to create image: storage 'local-lvm' is full (no space left on device)", provider.FailureQuota},
		{"another operation holding the guest", true, http.StatusInternalServerError, "VM 143 is locked (clone)", provider.FailureConflict},
		{"an identifier somebody else took first", true, http.StatusInternalServerError, "VM 143 already exists", provider.FailureConflict},
		{"a server error on a call that changes something", true, http.StatusInternalServerError, "internal error", provider.FailureAmbiguous},
		{"a server error on a read", false, http.StatusInternalServerError, "internal error", provider.FailureRefused},
		{"a refusal in words of its own", false, http.StatusTeapot, "cluster not ready - no quorum?", provider.FailureRefused},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakePVE(t, nil)
			var err error
			if tc.mutating {
				f.SetError(http.MethodPost, "/clone", tc.status, tc.message)
				_, err = f.client(t).CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143})
			} else {
				f.SetError(http.MethodGet, "/version", tc.status, tc.message)
				_, err = f.client(t).Version(context.Background())
			}
			if got := provider.KindOf(err); got != tc.want {
				t.Fatalf("kind = %q, want %q (%v)", got, tc.want, err)
			}
			if !strings.Contains(err.Error(), tc.message) {
				t.Errorf("Proxmox's own words were lost: %v", err)
			}
			if got := StatusCode(err); got != tc.status {
				t.Errorf("StatusCode = %d, want %d", got, tc.status)
			}
		})
	}
}

// An operator who is refused deserves to be told which privilege on which path,
// because "403" sends them reading eleven role definitions.
func TestAForbiddenCallNamesThePrivilegeAndThePath(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetError(http.MethodPost, "/clone", http.StatusForbidden, "Permission check failed (/vms/9000, VM.Clone)")

	_, err := f.client(t).CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143})
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a provider failure", err)
	}
	for _, want := range []string{"VM.Clone", "/vms/9000", tokenID} {
		if !strings.Contains(pe.Remedy, want) {
			t.Errorf("the remedy %q does not name %q", pe.Remedy, want)
		}
	}
}

// A lock is another operation in progress, and the answer to it is to wait and
// look again -- not to record a failure and certainly not to force anything.
func TestALockedVMIsAConflictRatherThanAFailure(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetLock(9000, "backup")

	_, err := f.client(t).CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143})
	if got := provider.KindOf(err); got != provider.FailureConflict {
		t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureConflict, err)
	}
	if !provider.Retryable(err) {
		t.Error("a conflict is resolved by looking again, so it is retryable")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("the reason does not say what holds the guest: %v", err)
	}

	// The same fact is readable from the guest itself, which is what the
	// reconciler looks at rather than guessing from the last error.
	status, err := f.client(t).VMStatus(context.Background(), "pve-1", 9000)
	if err != nil {
		t.Fatalf("VMStatus: %v", err)
	}
	if status.Lock != "backup" {
		t.Errorf("Lock = %q, want %q", status.Lock, "backup")
	}
}

// "Finished" and "worked" are different questions, and a caller that conflated
// them would record a failed clone as a machine.
func TestATaskThatFailsSurfacesItsExitStatus(t *testing.T) {
	f := newFakePVE(t, nil)
	c := f.client(t)
	ctx := context.Background()

	upid, err := c.CloneVM(ctx, "pve-1", 9000, CloneRequest{NewID: 143, Name: "zoomies-mach-abc"})
	if err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	if _, err := ParseUPID(upid); err != nil {
		t.Fatalf("the handle Proxmox returned does not parse: %v", err)
	}
	f.SetTaskPending(upid, 2)
	f.SetTaskFailure(upid, "storage 'local-lvm' is full")

	for i := range 2 {
		status, err := c.Task(ctx, "pve-1", upid)
		if err != nil {
			t.Fatalf("poll %d: %v", i, err)
		}
		if status.Done() {
			t.Fatalf("poll %d: the task reported itself finished while still running", i)
		}
	}

	status, err := c.Task(ctx, "pve-1", upid)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	switch {
	case !status.Done():
		t.Fatal("the task never finished")
	case status.OK():
		t.Fatal("a task that ended with a failure reported itself as OK")
	case status.ExitStatus != "storage 'local-lvm' is full":
		t.Errorf("ExitStatus = %q, which is not what the cluster said", status.ExitStatus)
	}

	// The log is where the sentence an operator is shown comes from.
	log, err := c.TaskLog(ctx, "pve-1", upid, 10)
	if err != nil {
		t.Fatalf("TaskLog: %v", err)
	}
	if !strings.Contains(log, "TASK ERROR: storage 'local-lvm' is full") {
		t.Errorf("the task log lost its detail: %q", log)
	}
}

// A task that succeeded says "OK" and nothing else does -- warnings included.
func TestOnlyAnExitStatusOfOKCountsAsSuccess(t *testing.T) {
	tests := []struct {
		name, status, exit string
		done, ok           bool
	}{
		{"still running", "running", "", false, false},
		{"finished cleanly", "stopped", "OK", true, true},
		{"finished with warnings", "stopped", "WARNINGS: 1", true, false},
		{"finished badly", "stopped", "command 'qm clone' failed: exit code 2", true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := TaskStatus{Status: tc.status, ExitStatus: tc.exit}
			if task.Done() != tc.done || task.OK() != tc.ok {
				t.Errorf("Done/OK = %v/%v, want %v/%v", task.Done(), task.OK(), tc.done, tc.ok)
			}
		})
	}
}

// The ownership sweep is one call for the whole cluster. Iterating nodes would
// cost a request per node per pass and would miss the guests on a node that was
// briefly unreachable -- which is the reading that deletes somebody's machine.
func TestTheSweepIsOneCallForTheWholeCluster(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetForeignVM(201, "someone-elses-build-box")

	guests, err := f.client(t).ClusterVMs(context.Background())
	if err != nil {
		t.Fatalf("ClusterVMs: %v", err)
	}
	if len(guests) != 3 {
		t.Fatalf("got %d guests, want every guest in the cluster", len(guests))
	}
	if got := f.Requests(); len(got) != 1 || got[0] != "GET "+v+"/cluster/resources" {
		t.Fatalf("the sweep made %v; it must be one call to /cluster/resources", got)
	}
	if got := queryValue(f.request(http.MethodGet, v+"/cluster/resources"), "type"); got != "vm" {
		t.Errorf("type = %q, want vm", got)
	}
}

// Proxmox's JSON is generated from Perl and it shows: the same field arrives as
// a number from one endpoint and a string from another, and every boolean is 0
// or 1. Decoding these wrongly would make a template look like a machine.
func TestProxmoxWritesNumbersAsStringsAndBooleansAsZeroOrOne(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantID  int
		wantTpl bool
	}{
		{"a numeric vmid and a template flag of 1", `{"vmid":9000,"template":1,"maxmem":17179869184}`, 9000, true},
		{"a string vmid and a template flag of \"1\"", `{"vmid":"9000","template":"1","maxmem":"17179869184"}`, 9000, true},
		{"no template member at all", `{"vmid":143}`, 143, false},
		{"a template flag of 0", `{"vmid":143,"template":0}`, 143, false},
		{"a real JSON boolean, which also happens", `{"vmid":143,"template":true}`, 143, true},
		{"a size in exponential notation", `{"vmid":143,"maxmem":1.7179869184e+10}`, 143, false},
		{"a null where a number belongs", `{"vmid":143,"maxmem":null}`, 143, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var vm ClusterVM
			if err := json.Unmarshal([]byte(tc.body), &vm); err != nil {
				t.Fatalf("decoding %s: %v", tc.body, err)
			}
			if vm.VMID.Int() != tc.wantID {
				t.Errorf("VMID = %d, want %d", vm.VMID.Int(), tc.wantID)
			}
			if bool(vm.Template) != tc.wantTpl {
				t.Errorf("Template = %v, want %v", vm.Template, tc.wantTpl)
			}
		})
	}
}

// An empty hash serialises as an empty list in Perl, and a token with no
// privileges at all is exactly the case preflight exists to explain -- so it
// must not arrive as a decode error.
func TestATokenWithNoPrivilegesDecodesAsNoPrivileges(t *testing.T) {
	var p Permissions
	if err := json.Unmarshal([]byte(`[]`), &p); err != nil {
		t.Fatalf("decoding an empty permission set: %v", err)
	}
	if len(p) != 0 {
		t.Errorf("got %v, want nothing", p)
	}
}

// A storage's content types are a comma-separated string, and whether a clone
// can land there at all depends on reading it.
func TestAStorageKnowsWhetherItCanHoldADisk(t *testing.T) {
	tests := []struct {
		content string
		want    bool
	}{
		{"images,rootdir", true},
		{"rootdir,images", true},
		{"iso,vztmpl", false},
		{"", false},
		{"images", true},
		{" images , backup ", true},
	}
	for _, tc := range tests {
		t.Run(tc.content, func(t *testing.T) {
			if got := (Storage{Content: tc.content}).Accepts("images"); got != tc.want {
				t.Errorf("Accepts(images) = %v, want %v", got, tc.want)
			}
		})
	}
}

// The guest agent is how enrolment reaches a machine, and a template with it
// switched off produces machines that boot and never enrol.
func TestAGuestAgentSettingIsAPropertyStringRatherThanAFlag(t *testing.T) {
	tests := []struct {
		agent string
		want  bool
	}{
		{"1", true},
		{"enabled=1,fstrim_cloned_disks=1", true},
		{"0", false},
		{"", false},
		{"enabled=0", false},
	}
	for _, tc := range tests {
		t.Run(tc.agent, func(t *testing.T) {
			if got := (VMConfig{Agent: tc.agent}).AgentEnabled(); got != tc.want {
				t.Errorf("AgentEnabled(%q) = %v, want %v", tc.agent, got, tc.want)
			}
		})
	}
}

// Picking the identifier ourselves is what makes a configured range mean
// anything: the cluster's own "next free id" knows nothing about the range an
// operator allowed this provider.
func TestAVMIDIsOnlyEverPickedInsideTheConfiguredRange(t *testing.T) {
	tests := []struct {
		name    string
		taken   []int
		lo, hi  int
		want    int
		wantErr provider.FailureKind
	}{
		{"nothing taken", nil, 9000, 9099, 9000, ""},
		{"the first few taken", []int{9000, 9001}, 9000, 9099, 9002, ""},
		{"a gap in the middle is used before the end", []int{9000, 9002}, 9000, 9099, 9001, ""},
		{"guests outside the range are none of our business", []int{100, 101, 9000}, 9000, 9001, 9001, ""},
		{"a range with nothing left", []int{9000, 9001}, 9000, 9001, 0, provider.FailureQuota},
		{"a range of one", nil, 9000, 9000, 9000, ""},
		{"a range that runs backwards", nil, 9100, 9000, 0, provider.FailureConfig},
		{"no range at all", nil, 0, 0, 0, provider.FailureConfig},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextFreeVMID(tc.taken, tc.lo, tc.hi)
			if tc.wantErr != "" {
				if provider.KindOf(err) != tc.wantErr {
					t.Fatalf("kind = %q, want %q (%v)", provider.KindOf(err), tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NextFreeVMID: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// The identifier is asserted free immediately before the clone, because the
// pick and the create are not one operation and somebody else's automation may
// have taken it in between.
func TestAnIdentifierIsAssertedFreeBeforeItIsUsed(t *testing.T) {
	f := newFakePVE(t, nil)
	c := f.client(t)

	if err := c.AssertVMIDFree(context.Background(), 143); err != nil {
		t.Fatalf("an unused identifier was refused: %v", err)
	}
	if got := queryValue(f.request(http.MethodGet, v+"/cluster/nextid"), "vmid"); got != "143" {
		t.Errorf("vmid = %q, want 143", got)
	}

	err := c.AssertVMIDFree(context.Background(), 9000)
	if got := provider.KindOf(err); got != provider.FailureConfig && got != provider.FailureConflict {
		t.Fatalf("kind = %q; an identifier already in use has to be refused", got)
	}
}

// Every call has to send what the API actually expects; a parameter with the
// wrong name is a machine that is built with the wrong shape or not at all.
func TestEveryCallSendsTheParametersProxmoxExpects(t *testing.T) {
	f := newFakePVE(t, nil)
	c := f.client(t)
	ctx := context.Background()

	if _, err := c.CloneVM(ctx, "pve-1", 9000, CloneRequest{
		NewID: 143, Name: "zoomies-mach-abc", Full: true, Storage: "local-lvm", Target: "pve-1",
		Pool: "zoomies", Description: "Managed by Zoomies",
	}); err != nil {
		t.Fatalf("CloneVM: %v", err)
	}
	clone := f.request(http.MethodPost, v+"/nodes/pve-1/qemu/9000/clone")
	for key, want := range map[string]string{
		"newid": "143", "name": "zoomies-mach-abc", "full": "1", "storage": "local-lvm",
		"target": "pve-1", "pool": "zoomies", "description": "Managed by Zoomies",
	} {
		if got := formValue(clone, key); got != want {
			t.Errorf("clone %s = %q, want %q", key, got, want)
		}
	}

	if _, err := c.ConfigureVM(ctx, "pve-1", 143, map[string][]string{
		"cores": {"4"}, "memory": {"8192"}, "net0": {"virtio,bridge=vmbr0"}, "tags": {"zoomies;zoomies-abc"},
	}); err != nil {
		t.Fatalf("ConfigureVM: %v", err)
	}
	config := f.request(http.MethodPost, v+"/nodes/pve-1/qemu/143/config")
	if got := formValue(config, "net0"); got != "virtio,bridge=vmbr0" {
		t.Errorf("net0 = %q", got)
	}
	if got := formValue(config, "tags"); got != "zoomies;zoomies-abc" {
		t.Errorf("tags = %q", got)
	}

	if err := c.ResizeDisk(ctx, "pve-1", 143, "scsi0", "+40G"); err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}
	resize := f.request(http.MethodPut, v+"/nodes/pve-1/qemu/143/resize")
	if formValue(resize, "disk") != "scsi0" || formValue(resize, "size") != "+40G" {
		t.Errorf("resize sent disk=%q size=%q", formValue(resize, "disk"), formValue(resize, "size"))
	}

	if _, err := c.ShutdownVM(ctx, "pve-1", 143, 90*time.Second, true); err != nil {
		t.Fatalf("ShutdownVM: %v", err)
	}
	shutdown := f.request(http.MethodPost, v+"/nodes/pve-1/qemu/143/status/shutdown")
	if formValue(shutdown, "timeout") != "90" || formValue(shutdown, "forceStop") != "1" {
		t.Errorf("shutdown sent timeout=%q forceStop=%q", formValue(shutdown, "timeout"), formValue(shutdown, "forceStop"))
	}

	if _, err := c.DeleteVM(ctx, "pve-1", 143); err != nil {
		t.Fatalf("DeleteVM: %v", err)
	}
	del := f.request(http.MethodDelete, v+"/nodes/pve-1/qemu/143")
	// Without both of these a destroyed guest leaves backups, replication jobs
	// and unreferenced disks behind, still costing somebody money.
	if queryValue(del, "purge") != "1" || queryValue(del, "destroy-unreferenced-disks") != "1" {
		t.Errorf("delete sent %q", del.URL.RawQuery)
	}
}

// The bootstrap payload is argv, never a shell line: a payload that is never
// pasted into a shell has no quoting to get wrong.
func TestACommandForTheGuestIsSentAsArgvRatherThanAShellLine(t *testing.T) {
	f := newFakePVE(t, nil)
	c := f.client(t)
	ctx := context.Background()

	if err := c.AgentPing(ctx, "pve-1", 100); err != nil {
		t.Fatalf("AgentPing: %v", err)
	}
	if err := c.AgentFileWrite(ctx, "pve-1", 100, "/etc/zoomies/agent.env", []byte("ZOOMIES_JOIN_TOKEN=join_abc\n")); err != nil {
		t.Fatalf("AgentFileWrite: %v", err)
	}
	if got := f.vm(100).files["/etc/zoomies/agent.env"]; !strings.Contains(got, "join_abc") {
		t.Errorf("the guest did not receive the file: %q", got)
	}

	pid, err := c.AgentExec(ctx, "pve-1", 100, []string{"systemctl", "enable", "--now", "zoomies-agent"}, "")
	if err != nil {
		t.Fatalf("AgentExec: %v", err)
	}
	if pid != 4242 {
		t.Errorf("pid = %d, want 4242", pid)
	}
	exec := f.request(http.MethodPost, v+"/nodes/pve-1/qemu/100/agent/exec")
	if got := exec.PostForm["command"]; len(got) != 4 || got[0] != "systemctl" || got[3] != "zoomies-agent" {
		t.Errorf("command = %q, want one value per argument", got)
	}

	status, err := c.AgentExecStatus(ctx, "pve-1", 100, pid)
	if err != nil {
		t.Fatalf("AgentExecStatus: %v", err)
	}
	if !bool(status.Exited) || status.ExitCode != 0 {
		t.Errorf("exited=%v exitcode=%d, want a finished, successful command", status.Exited, status.ExitCode)
	}
}

// A guest agent that never answers is what a machine that booted but never came
// up looks like, and it has to read as a refusal rather than as success.
func TestAGuestAgentThatNeverAnswersIsAFailureAndNotASuccess(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetGuestAgentSilent()

	err := f.client(t).AgentPing(context.Background(), "pve-1", 100)
	if err == nil {
		t.Fatal("a silent guest agent was reported as answering")
	}
	if !strings.Contains(err.Error(), "guest agent is not running") {
		t.Errorf("the reason does not say what is wrong: %v", err)
	}
}

// Proxmox writes its complaint in the HTTP status line as often as in the body.
// A client that only read the body would throw away the only sentence it sent.
func TestProxmoxsMessageInTheStatusLineIsNotLost(t *testing.T) {
	const sentence = "storage 'local-lvm' does not exist"
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		const body = `{"data":null}`
		fmt.Fprintf(conn, "HTTP/1.1 500 %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
			sentence, len(body), body)
	}()

	// Loopback plain HTTP is the one case this client accepts, which is what
	// makes this test possible without a certificate.
	c, err := New(Options{Endpoint: "http://" + ln.Addr().String(), TokenID: tokenID, Secret: tokenSecret})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.Version(context.Background())
	if err == nil {
		t.Fatal("a 500 was accepted as an answer")
	}
	if !strings.Contains(err.Error(), sentence) {
		t.Errorf("the message in the status line was lost: %v", err)
	}
}

// deadAddress is an address nothing is listening on, for the cases about a
// request that never left this machine.
func deadAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return addr
}

// The worst case this package has to survive: the virtual machine was made and
// the answer saying so was lost. It has to arrive as an unknown outcome, and
// the machine has to be findable afterwards -- because the only safe next move
// is to look for it, and a retry would leave one nobody is tracking running and
// billing.
func TestACloneWhoseAnswerWasLostIsUnknownRatherThanFailed(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetAmbiguousClone()
	c := f.client(t)

	_, err := c.CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143, Name: "zoomies-mach-abc"})
	if got := provider.KindOf(err); got != provider.FailureAmbiguous {
		t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureAmbiguous, err)
	}
	if provider.Retryable(err) {
		t.Error("an unknown outcome was reported as retryable")
	}

	guests, err := c.ClusterVMs(context.Background())
	if err != nil {
		t.Fatalf("ClusterVMs: %v", err)
	}
	var found bool
	for _, g := range guests {
		if g.Name == "zoomies-mach-abc" {
			found = true
		}
	}
	if !found {
		t.Fatal("the machine the clone made is not findable by name, so an ambiguous create could never be reconciled")
	}
}

// A cluster with no room refuses in its own words. It is worth waiting for and
// is never fixed by asking again immediately, which is a different answer from
// "the call was refused".
func TestAStorageWithNoRoomIsAQuotaRefusal(t *testing.T) {
	f := newFakePVE(t, nil)
	f.SetQuotaExhausted()

	_, err := f.client(t).CloneVM(context.Background(), "pve-1", 9000, CloneRequest{NewID: 143})
	if got := provider.KindOf(err); got != provider.FailureQuota {
		t.Fatalf("kind = %q, want %q (%v)", got, provider.FailureQuota, err)
	}
	if !strings.Contains(err.Error(), "local-lvm") {
		t.Errorf("the refusal does not say which storage is full: %v", err)
	}
	if !provider.Retryable(err) {
		t.Error("a full storage is worth trying again once there is room")
	}
}

// A cluster on a home network has no address the controller can route to. The
// controller hands the client a dialer for the private connection instead, and
// the client has to use it for every socket -- and consult no proxy, which
// would take the connection straight back out of the tunnel -- while the
// endpoint's host stays the name the certificate is verified against.
func TestClientDialsThroughTheProvidedDialerAndUsesNoProxy(t *testing.T) {
	f := newFakePVE(t, nil)
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	var dialled []string
	c, err := New(Options{
		// example.com is what httptest's certificate is issued for, and it is
		// not where the fake listens: only the dialer can get there.
		Endpoint: "https://example.com:8006",
		TokenID:  tokenID, Secret: tokenSecret, CAPEM: f.caPEM(t),
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			dialled = append(dialled, address)
			return (&net.Dialer{}).DialContext(ctx, network, f.Listener.Addr().String())
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr := c.http.Transport.(*http.Transport); tr.Proxy != nil {
		t.Fatal("a proxy could take the connection out of the private tunnel")
	}
	if _, err := c.Version(context.Background()); err != nil {
		t.Fatalf("Version through the dialer: %v", err)
	}
	if len(dialled) == 0 || dialled[0] != "example.com:8006" {
		t.Fatalf("the dialer was asked for %v, want the endpoint's host and port", dialled)
	}
}

// A controller on the Proxmox host itself is usually pointed at localhost, and
// Proxmox never issues a certificate for that name, so pasting the CA cannot
// fix it. The message has to say so, and has to name the escape hatch and what
// it costs, or the operator is left to guess at both.
func TestALoopbackEndpointExplainsWhyItCannotBeVerified(t *testing.T) {
	for _, endpoint := range []string{"https://localhost:8006", "https://127.0.0.1:8006", "https://[::1]:8006"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, err := range []error{x509.UnknownAuthorityError{}, x509.HostnameError{Host: "localhost"}} {
				msg, remedy := unreachable(endpoint, err), unreachableRemedy(endpoint, err)
				if !strings.Contains(msg, "not for localhost") {
					t.Errorf("message does not explain the name: %q", msg)
				}
				for _, want := range []string{"real name or IP", "ca_pem", "insecure_skip_verify", "risk"} {
					if !strings.Contains(remedy, want) {
						t.Errorf("remedy does not mention %q: %q", want, remedy)
					}
				}
			}
		})
	}

	// A named node keeps the plain advice, and still offers the way out.
	remedy := unreachableRemedy("https://pve-1:8006", x509.UnknownAuthorityError{})
	if strings.Contains(remedy, "real name or IP") || !strings.Contains(remedy, "insecure_skip_verify") {
		t.Errorf("remedy for a named node is wrong: %q", remedy)
	}
}

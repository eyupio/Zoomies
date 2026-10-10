package proxmox

// A fake Proxmox VE cluster.
//
// It speaks the real /api2/json shapes over httptest.NewTLSServer, and its
// certificate is what the client is given as its CA -- so every test in this
// package exercises verified TLS rather than skipping it, and the one test that
// leaves the CA out proves the verification is really there.
//
// The knobs exist because the failures worth designing for cannot be produced
// by a happy-path double: a task that stays running and then fails, a clone
// whose answer is lost after the virtual machine was made, a locked guest, a
// storage with no room, a guest agent that never answers.

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const tokenID = "zoomies@pve!fleet"
const tokenSecret = "11111111-2222-3333-4444-555555555555"

// v is the path prefix every endpoint sits behind, for route tables that read
// like the API documentation.
const v = APIPath

type fakeVM struct {
	vmid        int
	node        string
	name        string
	status      string
	tags        string
	description string
	template    bool
	lock        string
	agent       string
	// disk is the guest's scsi0 line, empty for one with no disk attached.
	disk  string
	files map[string]string
}

type fakeTask struct {
	node string
	// pending is how many more polls answer "running" before the task stops.
	pending int
	exit    string
}

type injected struct {
	method  string
	suffix  string
	status  int
	message string
}

type fakePVE struct {
	*httptest.Server
	t *testing.T

	mu       sync.Mutex
	seen     []*http.Request
	vms      map[int]*fakeVM
	tasks    map[string]*fakeTask
	nodes    []Node
	storages map[string][]Storage
	bridges  map[string][]NetworkInterface
	// anyBridgeEmpty makes the type=any_bridge filter answer nothing, as a
	// restricted token's does, while the unfiltered list still has the bridge.
	anyBridgeEmpty bool
	// networkForbidden refuses the unfiltered list, as a token without Sys.Audit
	// on the node is refused.
	networkForbidden bool
	// noBridgesVisible answers every network listing with nothing and no
	// refusal, as a token without SDN.Use on the local network zone is.
	noBridgesVisible bool
	perms            Permissions
	errors           []injected
	stalls           []injected
	seq              int

	agentSilent    bool
	ambiguousClone bool
	quota          bool

	stop chan struct{}
}

// newFakePVE starts a cluster with two online nodes, one offline node, a
// template and somebody else's virtual machine -- the shape a real homelab
// cluster has, so that a test about ownership has something to get wrong.
// Routes given here replace the built-in handler for the same pattern.
func newFakePVE(t *testing.T, routes map[string]http.HandlerFunc) *fakePVE {
	t.Helper()
	f := &fakePVE{
		t:     t,
		vms:   map[int]*fakeVM{},
		tasks: map[string]*fakeTask{},
		nodes: []Node{
			{Node: "pve-1", Status: "online", MaxCPU: 8, MaxMem: 32 << 30},
			{Node: "pve-2", Status: "online", MaxCPU: 8, MaxMem: 32 << 30},
			{Node: "pve-3", Status: "offline"},
		},
		storages: map[string][]Storage{
			"pve-1": {
				{Storage: "local", Type: "dir", Content: "iso,vztmpl", Enabled: true, Active: true},
				{Storage: "local-lvm", Type: "lvmthin", Content: "images,rootdir", Enabled: true, Active: true, Avail: 500 << 30, Total: 1 << 40},
				{Storage: "ceph", Type: "rbd", Content: "images", Enabled: true, Active: true, Shared: true, Avail: 2 << 40, Total: 4 << 40},
			},
			"pve-2": {
				{Storage: "ceph", Type: "rbd", Content: "images", Enabled: true, Active: true, Shared: true, Avail: 2 << 40, Total: 4 << 40},
			},
		},
		bridges: map[string][]NetworkInterface{
			"pve-1": {
				{Iface: "vmbr0", Type: "bridge", Active: true, CIDR: "10.0.0.10/24"},
				{Iface: "vmbr1", Type: "bridge", Active: true, Comments: "lab only"},
			},
			"pve-2": {
				{Iface: "vmbr0", Type: "bridge", Active: true, CIDR: "10.0.0.11/24"},
			},
		},
		perms: Permissions{
			"/vms": {
				"VM.Clone": true, "VM.Allocate": true, "VM.Audit": true, "VM.PowerMgmt": true,
				"VM.Config.Disk": true, "VM.Config.CPU": true, "VM.Config.Memory": true,
				"VM.Config.Network": true, "VM.Config.Options": true,
				"VM.GuestAgent.FileWrite": true, "VM.GuestAgent.Unrestricted": true,
			},
			"/storage/local-lvm": {"Datastore.AllocateSpace": true},
		},
		stop: make(chan struct{}),
	}
	f.vms[9000] = &fakeVM{vmid: 9000, node: "pve-1", name: "ubuntu-24.04-template", status: "stopped", template: true, agent: "enabled=1", disk: "local-lvm:base-9000-disk-0,size=32G"}
	f.vms[100] = &fakeVM{vmid: 100, node: "pve-1", name: "someone-elses-vm", status: "running", description: "the finance database"}

	mux := http.NewServeMux()
	for pattern, h := range routes {
		mux.HandleFunc(pattern, h)
	}
	for pattern, h := range f.routes() {
		if _, taken := routes[pattern]; taken {
			continue
		}
		mux.HandleFunc(pattern, h)
	}

	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.seen = append(f.seen, r)
		f.mu.Unlock()

		if !strings.HasPrefix(r.Header.Get("Authorization"), "PVEAPIToken="+tokenID+"=") {
			writeFailure(w, http.StatusUnauthorized, "authentication failure")
			return
		}
		if f.stalled(r) {
			// The request has arrived and will never be answered, which is the
			// only way to produce the outcome nobody heard.
			select {
			case <-r.Context().Done():
			case <-f.stop:
			case <-time.After(5 * time.Second):
			}
			return
		}
		if in, ok := f.errorFor(r); ok {
			writeFailure(w, in.status, in.message)
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(f.stop)
		f.Close()
	})
	return f
}

// client builds a client that verifies this server's certificate, which is the
// point: a test that skipped verification would not notice the day the client
// stopped doing it.
func (f *fakePVE) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(Options{Endpoint: f.URL, TokenID: tokenID, Secret: tokenSecret, CAPEM: f.caPEM(t)})
	if err != nil {
		t.Fatalf("building a client: %v", err)
	}
	return c
}

func (f *fakePVE) caPEM(t *testing.T) string {
	t.Helper()
	cert := f.Certificate()
	if cert == nil {
		t.Fatal("the fake cluster has no certificate")
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
}

// ---------------------------------------------------------------------------
// Knobs
// ---------------------------------------------------------------------------

// SetError answers a matching call with a status and a message. The path is a
// suffix so a test can name "/qemu/9000/clone" without the whole prefix.
func (f *fakePVE) SetError(method, suffix string, status int, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.errors = append(f.errors, injected{method: method, suffix: suffix, status: status, message: message})
}

// SetStall makes a matching call reach the server and never be answered, which
// is the only way to produce the failure the whole ambiguity rule exists for.
func (f *fakePVE) SetStall(method, suffix string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stalls = append(f.stalls, injected{method: method, suffix: suffix})
}

// SetTaskPending keeps a task "running" for the next n polls.
func (f *fakePVE) SetTaskPending(upid string, n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[upid]; ok {
		t.pending = n
	}
}

// SetTaskFailure ends a task with Proxmox's own words instead of "OK".
func (f *fakePVE) SetTaskFailure(upid, reason string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[upid]; ok {
		t.exit = reason
	}
}

// SetAmbiguousClone makes a clone create the virtual machine and then fail, so
// the answer says nothing about what happened -- the case that decides whether
// this fleet ends up paying for a machine nobody is tracking.
func (f *fakePVE) SetAmbiguousClone() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ambiguousClone = true
}

// SetQuotaExhausted refuses every clone for want of space.
func (f *fakePVE) SetQuotaExhausted() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quota = true
}

// SetGuestAgentSilent leaves the guest agent unanswering, which is what a
// machine that booted but never came up looks like.
func (f *fakePVE) SetGuestAgentSilent() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agentSilent = true
}

// SetLock puts another operation's lock on a guest.
func (f *fakePVE) SetLock(vmid int, lock string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[vmid]; ok {
		vm.lock = lock
	}
}

// SetForeignVM adds a guest that is nothing to do with this fleet.
func (f *fakePVE) SetForeignVM(vmid int, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.vms[vmid] = &fakeVM{vmid: vmid, node: "pve-1", name: name, status: "running"}
}

// SetPermissions replaces what the token may do.
func (f *fakePVE) SetPermissions(p Permissions) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.perms = p
}

// SetVMDescription is how a test puts an ownership block on a guest.
func (f *fakePVE) SetVMDescription(vmid int, description, tags string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if vm, ok := f.vms[vmid]; ok {
		vm.description, vm.tags = description, tags
	}
}

// Requests returns every call made, as "METHOD /path".
func (f *fakePVE) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.seen))
	for _, r := range f.seen {
		out = append(out, r.Method+" "+r.URL.Path)
	}
	return out
}

// request returns the first recorded call for "METHOD /path", with its form
// already parsed, so a test can assert on what was actually sent.
func (f *fakePVE) request(method, path string) *http.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.seen {
		if r.Method == method && r.URL.Path == path {
			return r
		}
	}
	return nil
}

func (f *fakePVE) vm(vmid int) *fakeVM {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.vms[vmid]
}

func (f *fakePVE) errorFor(r *http.Request) (injected, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.errors {
		if in.method == r.Method && strings.HasSuffix(r.URL.Path, in.suffix) {
			return in, true
		}
	}
	return injected{}, false
}

func (f *fakePVE) stalled(r *http.Request) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, in := range f.stalls {
		if in.method == r.Method && strings.HasSuffix(r.URL.Path, in.suffix) {
			return true
		}
	}
	return false
}

// newUPID mints a task handle in the wire's own shape, nine hex digits of
// process start included, because that is the form a parser written from one
// example gets wrong.
func (f *fakePVE) newUPID(node, kind, id string) string {
	f.seq++
	return fmt.Sprintf("UPID:%s:0005%04X:1%08X:65F0A1B2:%s:%s:%s:", node, f.seq, f.seq, kind, id, tokenID)
}

// ---------------------------------------------------------------------------
// Routes
// ---------------------------------------------------------------------------

func (f *fakePVE) routes() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET " + v + "/version": func(w http.ResponseWriter, r *http.Request) {
			writeData(w, map[string]any{"version": "8.2.4", "release": "8.2", "repoid": "5e5fc8b5"})
		},
		"GET " + v + "/access/permissions": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			writeData(w, f.perms)
		},
		"GET " + v + "/nodes": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := make([]map[string]any, 0, len(f.nodes))
			for _, n := range f.nodes {
				// maxmem arrives as a string here and as a number in
				// /cluster/resources: the same field, two wire forms.
				out = append(out, map[string]any{
					"node": n.Node, "status": n.Status, "maxcpu": n.MaxCPU.Int(),
					"maxmem": strconv.FormatInt(int64(n.MaxMem), 10),
				})
			}
			writeData(w, out)
		},
		"GET " + v + "/nodes/{node}/storage": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			list, ok := f.storages[r.PathValue("node")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "no such node")
				return
			}
			out := make([]map[string]any, 0, len(list))
			for _, s := range list {
				// enabled, active and shared are 0 or 1, never true or false.
				out = append(out, map[string]any{
					"storage": s.Storage, "type": s.Type, "content": s.Content,
					"enabled": boolToInt(bool(s.Enabled)), "active": boolToInt(bool(s.Active)),
					"shared": boolToInt(bool(s.Shared)), "avail": int64(s.Avail), "total": int64(s.Total),
				})
			}
			writeData(w, out)
		},
		"GET " + v + "/nodes/{node}/network": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			list, ok := f.bridges[r.PathValue("node")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "no such node")
				return
			}
			if f.noBridgesVisible {
				list = nil
			}
			if f.networkForbidden && r.URL.Query().Get("type") == "" {
				writeFailure(w, http.StatusForbidden, "Permission check failed (/nodes/"+r.PathValue("node")+", Sys.Audit)")
				return
			}
			if f.anyBridgeEmpty && r.URL.Query().Get("type") == "any_bridge" {
				list = nil
			}
			out := make([]map[string]any, 0, len(list))
			for _, b := range list {
				out = append(out, map[string]any{
					"iface": b.Iface, "type": b.Type, "active": boolToInt(bool(b.Active)),
					"cidr": b.CIDR, "comments": b.Comments,
				})
			}
			writeData(w, out)
		},
		"GET " + v + "/cluster/resources": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			out := []map[string]any{}
			for _, vm := range f.vms {
				out = append(out, map[string]any{
					"vmid": vm.vmid, "node": vm.node, "name": vm.name, "status": vm.status,
					"type": "qemu", "tags": vm.tags,
					// template is 1 or absent, never false.
					"template": boolToInt(vm.template),
					// maxmem is a string here on some releases.
					"maxmem": "17179869184",
				})
			}
			writeData(w, out)
		},
		"GET " + v + "/cluster/nextid": func(w http.ResponseWriter, r *http.Request) {
			asked := r.URL.Query().Get("vmid")
			id, _ := strconv.Atoi(asked)
			f.mu.Lock()
			defer f.mu.Unlock()
			if _, taken := f.vms[id]; taken {
				writeFailure(w, http.StatusBadRequest, fmt.Sprintf("VM %d already exists", id))
				return
			}
			writeData(w, asked)
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/clone": func(w http.ResponseWriter, r *http.Request) {
			node, template := r.PathValue("node"), pathInt(r, "vmid")
			newid, _ := strconv.Atoi(r.PostFormValue("newid"))
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.quota {
				writeFailure(w, http.StatusInternalServerError, "unable to create image: storage 'local-lvm' is full (no space left on device)")
				return
			}
			if src, ok := f.vms[template]; ok && src.lock != "" {
				writeFailure(w, http.StatusInternalServerError, fmt.Sprintf("VM %d is locked (%s)", template, src.lock))
				return
			}
			if _, taken := f.vms[newid]; taken {
				writeFailure(w, http.StatusInternalServerError, fmt.Sprintf("VM %d already exists", newid))
				return
			}
			f.vms[newid] = &fakeVM{vmid: newid, node: node, name: r.PostFormValue("name"),
				status: "stopped", description: r.PostFormValue("description"), agent: "enabled=1"}
			upid := f.newUPID(node, "qmclone", strconv.Itoa(newid))
			f.tasks[upid] = &fakeTask{node: node, exit: "OK"}
			if f.ambiguousClone {
				// The machine now exists and the caller will never learn it.
				writeFailure(w, http.StatusInternalServerError, "clone failed: got timeout")
				return
			}
			writeData(w, upid)
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/config": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			vm, ok := f.vms[pathInt(r, "vmid")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			if vm.lock != "" {
				writeFailure(w, http.StatusInternalServerError, fmt.Sprintf("VM %d is locked (%s)", vm.vmid, vm.lock))
				return
			}
			if tags := r.PostFormValue("tags"); tags != "" {
				vm.tags = tags
			}
			if d := r.PostFormValue("description"); d != "" {
				vm.description = d
			}
			writeData(w, nil)
		},
		"GET " + v + "/nodes/{node}/qemu/{vmid}/config": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			vm, ok := f.vms[pathInt(r, "vmid")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			out := map[string]any{
				"name": vm.name, "description": vm.description, "tags": vm.tags,
				"agent": vm.agent,
				// cores is a number, memory a string: both happen.
				"cores": 2, "memory": "4096",
			}
			if vm.disk != "" {
				out["scsi0"] = vm.disk
			}
			if vm.template {
				out["template"] = 1
			}
			writeData(w, out)
		},
		"GET " + v + "/nodes/{node}/qemu/{vmid}/status/current": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			vm, ok := f.vms[pathInt(r, "vmid")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			out := map[string]any{
				// vmid is a string here, a number in /cluster/resources.
				"vmid": strconv.Itoa(vm.vmid), "name": vm.name, "status": vm.status,
				"qmpstatus": vm.status, "tags": vm.tags, "agent": boolToInt(vm.agent != ""),
			}
			if vm.lock != "" {
				out["lock"] = vm.lock
			}
			if vm.template {
				out["template"] = 1
			}
			writeData(w, out)
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/status/{action}": func(w http.ResponseWriter, r *http.Request) {
			node, action := r.PathValue("node"), r.PathValue("action")
			f.mu.Lock()
			defer f.mu.Unlock()
			vm, ok := f.vms[pathInt(r, "vmid")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			if vm.lock != "" {
				writeFailure(w, http.StatusInternalServerError, fmt.Sprintf("VM %d is locked (%s)", vm.vmid, vm.lock))
				return
			}
			if action == "start" {
				vm.status = "running"
			} else {
				vm.status = "stopped"
			}
			upid := f.newUPID(node, "qm"+action, strconv.Itoa(vm.vmid))
			f.tasks[upid] = &fakeTask{node: node, exit: "OK"}
			writeData(w, upid)
		},
		"PUT " + v + "/nodes/{node}/qemu/{vmid}/resize": func(w http.ResponseWriter, r *http.Request) {
			writeData(w, nil)
		},
		"DELETE " + v + "/nodes/{node}/qemu/{vmid}": func(w http.ResponseWriter, r *http.Request) {
			node := r.PathValue("node")
			f.mu.Lock()
			defer f.mu.Unlock()
			vmid := pathInt(r, "vmid")
			if _, ok := f.vms[vmid]; !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			delete(f.vms, vmid)
			upid := f.newUPID(node, "qmdestroy", strconv.Itoa(vmid))
			f.tasks[upid] = &fakeTask{node: node, exit: "OK"}
			writeData(w, upid)
		},
		"GET " + v + "/nodes/{node}/tasks/{upid}/status": func(w http.ResponseWriter, r *http.Request) {
			upid := r.PathValue("upid")
			f.mu.Lock()
			defer f.mu.Unlock()
			task, ok := f.tasks[upid]
			if !ok {
				writeFailure(w, http.StatusNotFound, "no such task")
				return
			}
			out := map[string]any{"upid": upid, "node": task.node, "type": "qmclone", "id": "143"}
			if task.pending > 0 {
				task.pending--
				out["status"] = "running"
				writeData(w, out)
				return
			}
			out["status"] = "stopped"
			out["exitstatus"] = task.exit
			writeData(w, out)
		},
		"GET " + v + "/nodes/{node}/tasks/{upid}/log": func(w http.ResponseWriter, r *http.Request) {
			writeData(w, []map[string]any{
				{"n": 1, "t": "create full clone of drive scsi0"},
				{"n": "2", "t": "TASK ERROR: storage 'local-lvm' is full"},
			})
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/agent/ping": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			silent := f.agentSilent
			f.mu.Unlock()
			if silent {
				writeFailure(w, http.StatusInternalServerError, "QEMU guest agent is not running")
				return
			}
			writeData(w, nil)
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/agent/file-write": func(w http.ResponseWriter, r *http.Request) {
			f.mu.Lock()
			defer f.mu.Unlock()
			vm, ok := f.vms[pathInt(r, "vmid")]
			if !ok {
				writeFailure(w, http.StatusNotFound, "Configuration file does not exist")
				return
			}
			if vm.files == nil {
				vm.files = map[string]string{}
			}
			vm.files[r.PostFormValue("file")] = r.PostFormValue("content")
			writeData(w, nil)
		},
		"POST " + v + "/nodes/{node}/qemu/{vmid}/agent/exec": func(w http.ResponseWriter, r *http.Request) {
			writeData(w, map[string]any{"pid": 4242})
		},
		"GET " + v + "/nodes/{node}/qemu/{vmid}/agent/exec-status": func(w http.ResponseWriter, r *http.Request) {
			// exited is 1, exitcode a number, and the guest's own stderr is
			// what an operator is shown when a bootstrap fails.
			writeData(w, map[string]any{"exited": 1, "exitcode": 0, "out-data": "enabled zoomies-agent", "err-data": ""})
		},
	}
}

func writeData(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"data": v})
}

// writeFailure answers the way Proxmox does when a call is refused: a status
// and a sentence, with the data member explicitly null.
func writeFailure(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "message": message})
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func pathInt(r *http.Request, name string) int {
	n, _ := strconv.Atoi(r.PathValue(name))
	return n
}

// formValue is what a test asserts a call actually sent.
func formValue(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	return r.PostForm.Get(key)
}

// queryValue is the same for the parameters that ride on the URL.
func queryValue(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	return r.URL.Query().Get(key)
}

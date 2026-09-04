package installer

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/store"
)

// hostWith builds the detection the deployment question actually reads, so
// that "what does this host offer" can be asked without a host.
func hostWith(compose, docker bool) Detection {
	d := Detection{ConfigDir: "/etc/zoomies", HasSystemd: true}
	if compose {
		d.Compose = ComposeInfo{Command: []string{"docker", "compose"}, Available: true}
		d.HasCompose = true
	} else {
		d.Compose = ComposeInfo{Detail: "neither `docker compose` nor `docker-compose` answered here"}
	}
	if docker {
		d.Docker = RuntimeInfo{Kind: store.BackendDocker, Available: true, Installed: true,
			Endpoint: "unix:///var/run/docker.sock"}
	} else {
		d.Docker = RuntimeInfo{Kind: store.BackendDocker, Detail: "the Docker socket is not reachable"}
	}
	return d
}

func deploymentsOffered(d Detection) []Deployment {
	var out []Deployment
	for _, o := range DeploymentOptions(d) {
		out = append(out, o.Deployment)
	}
	return out
}

func TestDeploymentOptionsOfferOnlyWhatTheHostCanDo(t *testing.T) {
	cases := []struct {
		name            string
		compose, docker bool
		want            []Deployment
		wantDefault     Deployment
	}{
		{"a host with compose and docker", true, true,
			[]Deployment{DeploymentNative, DeploymentCompose, DeploymentDocker}, DeploymentCompose},
		{"docker without the compose plugin", false, true,
			[]Deployment{DeploymentNative, DeploymentDocker}, DeploymentNative},
		{"no container runtime at all", false, false,
			[]Deployment{DeploymentNative}, DeploymentNative},
		{"compose answering but the socket refusing us", true, false,
			[]Deployment{DeploymentNative, DeploymentCompose}, DeploymentCompose},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			det := hostWith(tc.compose, tc.docker)
			got := deploymentsOffered(det)
			if len(got) != len(tc.want) {
				t.Fatalf("offered %v, want %v", got, tc.want)
			}
			for n := range got {
				if got[n] != tc.want[n] {
					t.Fatalf("offered %v, want %v", got, tc.want)
				}
			}
			if def := DefaultDeployment(det); def != tc.wantDefault {
				t.Errorf("default = %q, want %q", def, tc.wantDefault)
			}

			// Every option must say what choosing it costs, and exactly one
			// must be marked as the default.
			defaults := 0
			for _, o := range DeploymentOptions(det) {
				if o.Description == "" {
					t.Errorf("%s is offered with no consequence stated", o.Deployment)
				}
				if o.Default {
					defaults++
					if o.Deployment != tc.wantDefault {
						t.Errorf("%s is marked default, want %s", o.Deployment, tc.wantDefault)
					}
					if !strings.Contains(o.Label, "(default)") {
						t.Errorf("the default must say so in its label, got %q", o.Label)
					}
				}
			}
			if defaults != 1 {
				t.Errorf("exactly one option is the default, got %d", defaults)
			}
		})
	}
}

func TestNativeOptionNamesTheSupervisorThisHostHas(t *testing.T) {
	systemd := DeploymentOptions(hostWith(false, false))[0]
	if !strings.Contains(systemd.Label, "systemd") {
		t.Errorf("a systemd host should be told about systemd, got %q", systemd.Label)
	}

	bare := hostWith(false, false)
	bare.HasSystemd = false
	if label := DeploymentOptions(bare)[0].Label; strings.Contains(label, "systemd") {
		t.Errorf("a host with no systemd must not be promised a unit, got %q", label)
	}
}

func TestDeploymentAvailabilityExplainsItself(t *testing.T) {
	det := hostWith(false, false)
	for _, want := range []Deployment{DeploymentCompose, DeploymentDocker} {
		if DeploymentAvailable(det, want) {
			t.Errorf("%s is not available on this host", want)
		}
		why := UnavailableDeployment(det, want)
		if why == "" {
			t.Fatalf("%s must be refused with a reason", want)
		}
		if !strings.Contains(why, "--deployment native") {
			t.Errorf("the refusal must offer a way forward, got %q", why)
		}
	}
	if !DeploymentAvailable(det, DeploymentNative) {
		t.Error("native always works; it is the one that needs nothing installed")
	}
	if why := UnavailableDeployment(det, DeploymentNative); why != "" {
		t.Errorf("native needs no excuse, got %q", why)
	}
}

func TestDefaultDeploymentReasonIsSpecific(t *testing.T) {
	with := defaultDeploymentReason(hostWith(true, true))
	if !strings.Contains(with, "docker compose") {
		t.Errorf("the reason should name the command that answered, got %q", with)
	}
	without := defaultDeploymentReason(hostWith(false, false))
	if !strings.Contains(without, "no compose command") {
		t.Errorf("the reason should say why native won, got %q", without)
	}
}

func TestDeploymentRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := DeploymentRecord{
		Deployment:     DeploymentCompose,
		Directory:      dir,
		ComposeCommand: []string{"docker-compose"},
		Container:      "zoomies",
		Image:          "ghcr.io/eyupio/zoomies:v1.2.3",
		Volume:         "zoomies-data",
		Network:        "zoomies",
		EnvFile:        filepath.Join(dir, ".env"),
		Mode:           ModeSingle,
	}

	path, err := WriteDeploymentRecord(dir, want)
	if err != nil {
		t.Fatalf("WriteDeploymentRecord: %v", err)
	}
	if path != DeploymentRecordPath(dir) {
		t.Fatalf("record written to %q, want %q", path, DeploymentRecordPath(dir))
	}

	got, ok := ReadDeploymentRecord(dir)
	if !ok {
		t.Fatal("the record just written was not read back")
	}
	if got.Deployment != want.Deployment || got.Directory != want.Directory ||
		strings.Join(got.ComposeCommand, " ") != "docker-compose" || got.Volume != want.Volume {
		t.Fatalf("record round-tripped as %+v", got)
	}
	if got.CreatedAt == "" {
		t.Error("the record should say when it was written")
	}
	if got.ComposeFile() != filepath.Join(dir, ComposeFileName) {
		t.Errorf("compose file = %q", got.ComposeFile())
	}
}

func TestReadDeploymentRecordIsQuietWhenThereIsNone(t *testing.T) {
	dir := t.TempDir()
	if _, ok := ReadDeploymentRecord(dir); ok {
		t.Fatal("an empty directory records no deployment")
	}
	// A native install, or one from a version that predates the record, must
	// not be mistaken for a container.
	writeFile(t, dir, deploymentRecordFile, "{ this is not json")
	if _, ok := ReadDeploymentRecord(dir); ok {
		t.Fatal("an unreadable record must be treated as no record, not as a container")
	}
	writeFile(t, dir, deploymentRecordFile, `{"directory":"/etc/zoomies"}`)
	if _, ok := ReadDeploymentRecord(dir); ok {
		t.Fatal("a record naming no deployment says nothing about how this host runs")
	}
}

func TestUninstallItemsIncludeAContainerDeployment(t *testing.T) {
	opts := uninstallOpts(t)
	env := writeFile(t, opts.ConfigDir, ".env", "ZOOMIES_ENCRYPTION_KEY=abc\n")
	if _, err := WriteDeploymentRecord(opts.ConfigDir, DeploymentRecord{
		Deployment:     DeploymentCompose,
		Directory:      opts.ConfigDir,
		ComposeCommand: []string{"docker", "compose"},
		Volume:         "zoomies-data",
		EnvFile:        env,
	}); err != nil {
		t.Fatal(err)
	}

	byWhat := map[string]RemovalItem{}
	for _, it := range UninstallItems(opts) {
		byWhat[it.What] = it
	}
	for _, what := range []string{"compose project", "environment file", "data volume", "deployment record"} {
		if _, ok := byWhat[what]; !ok {
			t.Errorf("%s is not listed, so an operator would be told Zoomies is gone while it is still running", what)
		}
	}
	if note := byWhat["data volume"].Note; !strings.Contains(note, "database") {
		t.Errorf("the volume's note must say it is the database, got %q", note)
	}
	if note := byWhat["environment file"].Note; !strings.Contains(note, "encryption key") {
		t.Errorf("the env file's note must say what is in it, got %q", note)
	}

	// A docker deployment is named by its container, because there is no file
	// to point at.
	if err := os.Remove(DeploymentRecordPath(opts.ConfigDir)); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteDeploymentRecord(opts.ConfigDir, DeploymentRecord{
		Deployment: DeploymentDocker, Directory: opts.ConfigDir, Container: "zoomies",
	}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range UninstallItems(opts) {
		if it.What == "container" && it.Path == "zoomies" {
			found = true
		}
	}
	if !found {
		t.Error("a docker deployment must be listed by the container it left running")
	}
}

// newTestInstaller builds an installer with a known host and no terminal, so
// that the unattended path can be exercised without one.
func newTestInstaller(t *testing.T, det Detection, opts Options) (*Installer, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	opts.Out = out
	opts.In = strings.NewReader("")
	opts.NonInteractive = true
	opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	i, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	i.det = det
	return i, out
}

func TestResolveDeploymentTakesTheDefaultUnattendedAndSaysSo(t *testing.T) {
	det := hostWith(true, true)
	i, out := newTestInstaller(t, det, Options{})

	got, err := i.resolveDeployment(t.Context(), Plan{Deployment: DefaultDeployment(det)})
	if err != nil {
		t.Fatalf("resolveDeployment: %v", err)
	}
	if got.Deployment != DeploymentCompose {
		t.Fatalf("deployment = %q, want compose", got.Deployment)
	}
	// An operator reading the log later must be able to see what was chosen
	// for them, and why.
	body := out.String()
	for _, want := range []string{"compose", "docker compose", "--deployment"} {
		if !strings.Contains(body, want) {
			t.Errorf("the output must mention %q:\n%s", want, body)
		}
	}
}

func TestResolveDeploymentRefusesOneThisHostCannotDo(t *testing.T) {
	i, _ := newTestInstaller(t, hostWith(false, false), Options{Deployment: DeploymentCompose})

	_, err := i.resolveDeployment(t.Context(), Plan{})
	if err == nil {
		t.Fatal("a compose deployment on a host with no compose must be refused, not attempted")
	}
	if !strings.Contains(err.Error(), "compose") {
		t.Errorf("the error must name what is missing, got %q", err)
	}
}

func TestPlanMissingSkipsTheAdministratorForAContainer(t *testing.T) {
	p := testPlan(t)
	p.AdminUser, p.adminPassword = "", ""
	if len(p.Missing()) == 0 {
		t.Fatal("a native install must insist on an administrator")
	}

	p.Deployment = DeploymentCompose
	if got := p.Missing(); len(got) != 0 {
		t.Fatalf("a container creates its first administrator in the browser; got %v", keysOf(got))
	}
}

package aicontext

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestManagedWorkflowKeepsSourceGenerationReadOnlyAndUsesImmutableActions(t *testing.T) {
	key, config := setupInputs()
	config.SourceBranch = "trusted'branch"
	workflow, err := SetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		On struct {
			Push struct {
				Branches []string `yaml:"branches"`
			} `yaml:"push"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			If          string            `yaml:"if"`
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Uses string            `yaml:"uses"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.On.Push.Branches) != 1 || parsed.On.Push.Branches[0] != config.SourceBranch {
		t.Fatal("wrong trusted branch")
	}
	if parsed.Permissions["contents"] != "read" || parsed.Jobs["generate"].Permissions["contents"] != "read" || parsed.Jobs["publish"].Permissions["contents"] != "write" {
		t.Fatal("generation inherits write access")
	}
	for name, job := range parsed.Jobs {
		if !strings.Contains(job.If, "refs/heads/trusted''branch") {
			t.Fatalf("%s accepts other branches: %s", name, job.If)
		}
		for _, step := range job.Steps {
			if step.Uses != "" {
				_, sha, ok := strings.Cut(step.Uses, "@")
				if !ok || !commitPattern.MatchString(sha) {
					t.Fatalf("un-pinned action: %s", step.Uses)
				}
			}
			if name == "generate" && step.Env["GH_TOKEN"] != "" {
				t.Fatal("generator sees publication token")
			}
			if step.Env["EXPECTED_CONFIG"] != "" {
				var cfg managedConfig
				if err := json.Unmarshal([]byte(step.Env["EXPECTED_CONFIG"]), &cfg); err != nil || cfg.SourceBranch != config.SourceBranch {
					t.Fatal("configuration was corrupted by template expansion")
				}
			}
		}
	}
	changes, err := PlanManagedSetup(key, "owner/repo", config, nil)
	if err != nil {
		t.Fatal(err)
	}
	var saved []SetupFile
	for _, c := range changes {
		saved = append(saved, SetupFile{Path: c.Path, SHA: strings.Repeat("a", 40), Content: c.Content})
	}
	retry, err := PlanManagedSetup(key, "owner/repo", config, saved)
	if err != nil || len(retry) != 0 {
		t.Fatalf("retry: %d %v", len(retry), err)
	}
	saved[len(saved)-1].Content += "custom"
	if _, err := PlanManagedSetup(key, "owner/repo", config, saved); err == nil {
		t.Fatal("custom toolchain was replaced")
	}
}

func TestGeneratorProducesAValidCommitPinnedSnapshotWithRealRepomix(t *testing.T) {
	cli := os.Getenv("ZOOMIES_TEST_REPOMIX_CLI")
	if cli == "" {
		t.Skip("set ZOOMIES_TEST_REPOMIX_CLI to exercise the pinned Repomix generator")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is required for workflow validation")
	}
	root := t.TempDir()
	source := filepath.Join(root, "repo")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = source
		b, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %s", err, b)
		}
		return strings.TrimSpace(string(b))
	}
	run("git", "init", "-q")
	run("git", "config", "user.name", "Test")
	run("git", "config", "user.email", "test@example.org")
	key, config := setupInputs()
	cfg, _ := json.Marshal(managedConfig{Manager: "zoomies-ai-context", TemplateVersion: 1, Config: config})
	for p, c := range map[string]string{"main.go": "package main\n\nfunc main() { println(42) }\n", ".env": "SHOULD_NOT_APPEAR=1\n", "private.key": "excluded key\n", "zoomies-ai-context.config.json": string(cfg), "skip.go": "excluded\n",
		// One file over the limit and one the real secret scan withholds: neither
		// may fail the run, and neither may pass as absent. The credential-bearing
		// URL is assembled at run time: written out, it made this very file the one
		// the real secret scan withheld, and the repository's own context run failed.
		"big.txt": strings.Repeat("a line of generated text\n", MaxFileBytes/20), "fixture_test.go": "package main\n\nvar origin = \"https://user:" + "pass@example.com\"\n"} {
		if err := os.WriteFile(filepath.Join(source, p), []byte(c), 0600); err != nil {
			t.Fatal(err)
		}
	}
	config.Exclude = append(config.Exclude, "skip.go")
	cfg, _ = json.Marshal(managedConfig{Manager: "zoomies-ai-context", TemplateVersion: 1, Config: config})
	_ = os.WriteFile(filepath.Join(source, ConfigPath), cfg, 0600)
	run("git", "add", ".")
	run("git", "commit", "-qm", "source")
	commit := run("git", "rev-parse", "HEAD")
	script, _ := setupTemplates.ReadFile("templates/generate.py")
	scriptPath := filepath.Join(root, "generate.py")
	_ = os.WriteFile(scriptPath, script, 0600)
	output := filepath.Join(root, "out")
	identity, _ := json.Marshal(key)
	hash, _ := config.Hash()
	cmd := exec.Command(python, scriptPath)
	cmd.Env = append(os.Environ(), "SOURCE_DIR="+source, "OUTPUT_DIR="+output, "REPOMIX_CLI="+cli, "SOURCE_COMMIT="+commit, "EXPECTED_CONFIG="+string(cfg), "REPOSITORY_IDENTITY="+string(identity), "CONFIG_HASH="+hash)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("real generator: %v %s", err, b)
	}
	file, err := os.Open(filepath.Join(output, "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	snapshot, err := Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Match(key, config.SourceBranch, commit, hash); err != nil {
		t.Fatal(err)
	}
	for _, f := range snapshot.Files {
		if f.Path == ".env" || f.Path == "private.key" || f.Path == "skip.go" {
			t.Fatalf("excluded file %s", f.Path)
		}
	}
	found := false
	for _, f := range snapshot.Files {
		if f.Path == "main.go" {
			found = true
			if !strings.HasSuffix(f.Content, "\n") {
				t.Fatal("source newline changed")
			}
		}
	}
	if !found {
		t.Fatal("source missing")
	}
	reasons := map[string]string{}
	for _, o := range snapshot.Omitted {
		reasons[o.Path] = o.Reason
	}
	if len(reasons) != 2 || reasons["big.txt"] != OmittedTooLarge || reasons["fixture_test.go"] != OmittedFlagged {
		t.Fatalf("omitted files: %+v", snapshot.Omitted)
	}
	for _, f := range snapshot.Files {
		if f.Path == "big.txt" || f.Path == "fixture_test.go" || strings.Contains(f.Content, "user:pass") {
			t.Fatalf("withheld content was carried: %s", f.Path)
		}
	}
	page, err := snapshot.FilePage(strings.Repeat("d", 64), 0, 20, 8000)
	if err != nil || !strings.Contains(string(page), `"omitted_total":2`) {
		t.Fatalf("overview page: %s %v", page, err)
	}
}

func TestEnterpriseServerGetsAWorkflowItCanRun(t *testing.T) {
	// Artifact actions v4 and later fail on Enterprise Server, and it has no
	// GitHub-hosted runners: a GitHub.com workflow merged there would never
	// produce context, and only say so after the setup PR was merged.
	for _, destination := range []Destination{Both, Zoomies} {
		key, config := setupInputs()
		key.GitHubHost = "github.example.org"
		config.Destination = destination
		if destination == Zoomies {
			config.UploadURL = "https://zoomies.example.org" + UploadPath
		}
		workflow, err := SetupWorkflow(key, config)
		if err != nil {
			t.Fatal(destination, err)
		}
		for _, absent := range []string{"ubuntu-latest", "upload-artifact@043fb46", "download-artifact@634f93c", "checkout@3d3c42e", "setup-node@8207627", "package-manager-cache"} {
			if strings.Contains(workflow, absent) {
				t.Errorf("%s: Enterprise Server workflow still has %q", destination, absent)
			}
		}
		for _, present := range []string{"runs-on: [self-hosted, linux]", "actions/upload-artifact@c6a366c94c3e0affe28c06c8df20a878f24da3cf # v3.2.2", "actions/download-artifact@a9bc5e6ef2cb54c177f32aa5726adaa15e7e2d59 # v3.1.0", "actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0", "actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4.4.0"} {
			if !strings.Contains(workflow, present) {
				t.Errorf("%s: Enterprise Server workflow lacks %q", destination, present)
			}
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
			t.Fatal(destination, err)
		}
		// The generator still runs where it cannot write: only the last job
		// holds contents: write or id-token: write.
		generate := parsed["jobs"].(map[string]any)["generate"].(map[string]any)
		if perms := generate["permissions"].(map[string]any); len(perms) != 1 || perms["contents"] != "read" {
			t.Errorf("%s: generate job permissions = %v", destination, perms)
		}
	}
}

func TestGitHubComWorkflowIsUnchangedByEnterpriseSupport(t *testing.T) {
	// A workflow already merged on GitHub.com is recognised byte for byte;
	// Enterprise Server support must not move a single byte of it.
	key, config := setupInputs()
	workflow, err := SetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	if enterpriseServerWorkflow(workflow) == workflow || !strings.Contains(workflow, "runs-on: ubuntu-latest") {
		t.Fatal("GitHub.com workflow was rewritten for Enterprise Server")
	}
}

func TestGHEComIsRefusedRatherThanGuessed(t *testing.T) {
	key, config := setupInputs()
	key.GitHubHost = "acme.ghe.com"
	if _, err := SetupWorkflow(key, config); err == nil || !strings.Contains(err.Error(), "GHE.com") {
		t.Fatal("GHE.com received a workflow", err)
	}
}

func TestWorkflowPublicationBoundaries(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is required to exercise the Actions publication script")
	}
	cmd := exec.Command(python, "templates_test.py")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("publication tests: %v %s", err, output)
	}
}

func TestWorkflowEscapesGitHubFilterOperatorsInLiteralSourceBranches(t *testing.T) {
	key, config := setupInputs()
	config.SourceBranch = "!trusted+branch"
	workflow, err := SetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatal(err)
	}
	branches := parsed["on"].(map[string]any)["push"].(map[string]any)["branches"].([]any)
	if branches[0] != `\!trusted\+branch` {
		t.Fatalf("branch treated as a GitHub glob: %v", branches)
	}
}

// Zoomies-only output keeps the repository read-only: its last job may mint
// an OIDC token and nothing else, and sends the snapshot only to the address
// that was reviewed in the configuration.
func TestZoomiesOnlyWorkflowUploadsWithAnOIDCTokenAndNeverWritesTheRepository(t *testing.T) {
	key, config := setupInputs()
	config.Destination = Zoomies
	if _, err := SetupWorkflow(key, config); err == nil {
		t.Fatal("a Zoomies-only workflow was planned without an upload address")
	}
	config.UploadURL = UploadURLFor("https://zoomies.example.com/")
	workflow, err := SetupWorkflow(key, config)
	if err != nil {
		t.Fatal(err)
	}
	var parsed struct {
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal([]byte(workflow), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed.Jobs["publish"]; ok {
		t.Fatal("Zoomies-only output still publishes a branch")
	}
	upload, ok := parsed.Jobs["upload"]
	if !ok {
		t.Fatal("no upload job")
	}
	if len(upload.Permissions) != 1 || upload.Permissions["id-token"] != "write" {
		t.Fatalf("the upload job's permissions should be id-token: write alone, got %v", upload.Permissions)
	}
	for name, job := range parsed.Jobs {
		for permission, level := range job.Permissions {
			if level == "write" && !(name == "upload" && permission == "id-token") {
				t.Fatalf("%s grants %s: write", name, permission)
			}
		}
	}
	last := upload.Steps[len(upload.Steps)-1]
	if last.Env["UPLOAD_URL"] != "https://zoomies.example.com"+UploadPath {
		t.Fatalf("upload address %q", last.Env["UPLOAD_URL"])
	}
}

func TestOnlyZoomiesOnlyOutputCarriesAnHTTPSUploadAddress(t *testing.T) {
	_, config := setupInputs()
	for _, bad := range []string{"", "http://zoomies.example.com" + UploadPath, "https://zoomies.example.com/elsewhere", "https://u@zoomies.example.com" + UploadPath} {
		config.Destination, config.UploadURL = Zoomies, bad
		if config.Validate() == nil {
			t.Errorf("accepted upload address %q", bad)
		}
	}
	config.Destination, config.UploadURL = Both, UploadURLFor("https://zoomies.example.com")
	if config.Validate() == nil {
		t.Error("Both output accepted an upload address")
	}
	if UploadURLFor("http://zoomies.example.com") != "" || UploadURLFor("") != "" {
		t.Error("a non-https controller produced an upload address")
	}
}

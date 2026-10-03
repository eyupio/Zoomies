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
	for p, c := range map[string]string{"main.go": "package main\n\nfunc main() { println(42) }\n", ".env": "SHOULD_NOT_APPEAR=1\n", "private.key": "excluded key\n", "zoomies-ai-context.config.json": string(cfg), "skip.go": "excluded\n"} {
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
}

func TestManagedWorkflowRefusesUnsupportedEnterpriseArtifacts(t *testing.T) {
	key, config := setupInputs()
	key.GitHubHost = "github.example.org"
	if _, err := SetupWorkflow(key, config); err == nil {
		t.Fatal("GHES received an unsupported artifact workflow")
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

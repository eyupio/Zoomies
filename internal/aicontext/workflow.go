package aicontext

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	GeneratorPackagePath = ".github/zoomies-ai-context/package.json"
	GeneratorLockPath    = ".github/zoomies-ai-context/package-lock.json"
)

//go:embed templates/*
var setupTemplates embed.FS

func SetupWorkflow(key RepositoryKey, config Config) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	if err := config.Validate(); err != nil {
		return "", err
	}
	// GHE.com is laid out like GitHub.com but signs Actions tokens with its own
	// issuer and has not been tried; refuse it rather than guess, so nothing
	// is merged that would only fail afterwards.
	if key.GitHubHost == "ghe.com" || strings.HasSuffix(key.GitHubHost, ".ghe.com") {
		return "", fmt.Errorf("managed context workflows are not available for GHE.com yet; use a repository on GitHub.com or GitHub Enterprise Server")
	}
	cfg, _ := json.Marshal(managedConfig{Manager: "zoomies-ai-context", TemplateVersion: SetupTemplateVersion, Config: config})
	identity, _ := json.Marshal(key)
	hash, _ := config.Hash()
	branch, _ := json.Marshal(config.SourceBranch)
	pushBranch, _ := json.Marshal(strings.NewReplacer("!", `\!`, "+", `\+`).Replace(config.SourceBranch))
	generate, _ := setupTemplates.ReadFile("templates/generate.py")
	publish, _ := setupTemplates.ReadFile("templates/publish.py")
	upload, _ := setupTemplates.ReadFile("templates/upload.py")
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	workflow := `# Managed by Zoomies AI Context; template 1. Review upgrades through a PR.
name: Zoomies AI Context
on:
  push:
    branches: [@@PUSH_BRANCH@@]
  workflow_dispatch:
permissions:
  contents: read
concurrency:
  group: zoomies-ai-context
  cancel-in-progress: true
jobs:
  generate:
    if: github.ref == TRUSTED_REF && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')
    runs-on: ubuntu-latest
    timeout-minutes: 15
    permissions:
      contents: read
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          ref: ${{ github.sha }}
          path: source
          persist-credentials: false
          submodules: false
          lfs: false
      - uses: actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0
        with:
          node-version: '24'
          package-manager-cache: false
      - name: Install pinned generator without repository scripts
        shell: bash
        run: |
          set -eu
          mkdir -p "$RUNNER_TEMP/zoomies-tools"
          cp source/.github/zoomies-ai-context/package.json "$RUNNER_TEMP/zoomies-tools/package.json"
          cp source/.github/zoomies-ai-context/package-lock.json "$RUNNER_TEMP/zoomies-tools/package-lock.json"
          npm ci --prefix "$RUNNER_TEMP/zoomies-tools" --ignore-scripts --no-audit --no-fund > /dev/null 2>&1
      - name: Generate bounded source context
        env:
          SOURCE_DIR: ${{ github.workspace }}/source
          OUTPUT_DIR: ${{ runner.temp }}/zoomies-context
          REPOMIX_CLI: ${{ runner.temp }}/zoomies-tools/node_modules/.bin/repomix
          SOURCE_COMMIT: ${{ github.sha }}
          EXPECTED_CONFIG: @@CONFIG@@
          REPOSITORY_IDENTITY: @@IDENTITY@@
          CONFIG_HASH: @@HASH@@
        shell: python
        run: |
@@GENERATE@@
      - uses: actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1
        with:
          name: zoomies-ai-context
          path: ${{ runner.temp }}/zoomies-context/
          if-no-files-found: error
          retention-days: 1
@@DELIVER@@`
	// Zoomies-only output never writes to the repository: its last job holds
	// an OIDC token instead of contents: write, and sends the snapshot to the
	// controller named in the reviewed configuration.
	deliver := `  publish:
    needs: generate
    if: github.ref == TRUSTED_REF
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      contents: write
    steps:
      - uses: actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0 # v5
        with:
          name: zoomies-ai-context
          path: ${{ runner.temp }}/zoomies-context
      - name: Validate and atomically publish the generated branch
        env:
          GH_TOKEN: ${{ github.token }}
          ARTIFACT_DIR: ${{ runner.temp }}/zoomies-context
          SOURCE_COMMIT: ${{ github.sha }}
          SOURCE_BRANCH: @@BRANCH@@
          REPOSITORY_IDENTITY: @@IDENTITY@@
          CONFIG_HASH: @@HASH@@
        shell: python
        run: |
@@PUBLISH@@
`
	if config.Destination == Zoomies {
		deliver = `  upload:
    needs: generate
    if: github.ref == TRUSTED_REF
    runs-on: ubuntu-latest
    timeout-minutes: 10
    permissions:
      id-token: write
    steps:
      - uses: actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0 # v5
        with:
          name: zoomies-ai-context
          path: ${{ runner.temp }}/zoomies-context
      - name: Upload the verified context to Zoomies
        env:
          ARTIFACT_DIR: ${{ runner.temp }}/zoomies-context
          SOURCE_COMMIT: ${{ github.sha }}
          SOURCE_BRANCH: @@BRANCH@@
          REPOSITORY_IDENTITY: @@IDENTITY@@
          CONFIG_HASH: @@HASH@@
          UPLOAD_URL: @@UPLOAD_URL@@
        shell: python
        run: |
@@UPLOAD@@
`
	}
	workflow = strings.Replace(workflow, "@@DELIVER@@", deliver, 1)
	// GitHub expressions need a single-quoted literal, with apostrophes
	// doubled; JSON/YAML quoting protects the surrounding YAML scalar.
	trustedRef := quote("${{ github.ref == 'refs/heads/" + strings.ReplaceAll(config.SourceBranch, "'", "''") + "' }}")
	condition := "${{ github.ref == 'refs/heads/" + strings.ReplaceAll(config.SourceBranch, "'", "''") + "' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch') }}"
	workflow = strings.ReplaceAll(workflow, "github.ref == TRUSTED_REF && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')", quote(condition))
	workflow = strings.ReplaceAll(workflow, "github.ref == TRUSTED_REF", trustedRef)
	if key.GitHubHost != "github.com" {
		workflow = enterpriseServerWorkflow(workflow)
	}
	workflow = strings.NewReplacer("@@PUSH_BRANCH@@", string(pushBranch), "@@BRANCH@@", string(branch), "@@CONFIG@@", quote(string(cfg)), "@@IDENTITY@@", quote(string(identity)), "@@HASH@@", quote(hash), "@@GENERATE@@", indentScript(string(generate)), "@@PUBLISH@@", indentScript(string(publish)), "@@UPLOAD_URL@@", quote(config.UploadURL), "@@UPLOAD@@", indentScript(string(upload))).Replace(workflow)
	return workflow, nil
}

// enterpriseServerWorkflow rewrites the GitHub.com workflow for GitHub
// Enterprise Server. Artifact actions v4 and later do not run there, so the
// jobs hand the context over with v3 -- which keeps the generator in a job
// that cannot write to the repository -- and every action is pinned to the
// release Enterprise Server bundles, so the workflow runs without GitHub
// Connect. Enterprise Server has no GitHub-hosted runners: the jobs ask for
// any self-hosted Linux runner, which a Zoomies pool is.
func enterpriseServerWorkflow(workflow string) string {
	return strings.NewReplacer(
		"runs-on: ubuntu-latest", "runs-on: [self-hosted, linux]",
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1", "actions/checkout@11d5960a326750d5838078e36cf38b85af677262 # v4.4.0",
		// setup-node v4 has no package-manager-cache input, and caches nothing
		// unless asked, which is what the GitHub.com workflow sets it to.
		"actions/setup-node@820762786026740c76f36085b0efc47a31fe5020 # v7.0.0", "actions/setup-node@49933ea5288caeca8642d1e84afbd3f7d6820020 # v4.4.0",
		"          package-manager-cache: false\n", "",
		"actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a # v7.0.1", "actions/upload-artifact@c6a366c94c3e0affe28c06c8df20a878f24da3cf # v3.2.2",
		"actions/download-artifact@634f93cb2916e3fdff6788551b99b062d0335ce0 # v5", "actions/download-artifact@a9bc5e6ef2cb54c177f32aa5726adaa15e7e2d59 # v3.1.0",
	).Replace(workflow)
}

func indentScript(s string) string {
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = "          " + line
		}
	}
	return strings.Join(lines, "\n")
}

// LegacySetupWorkflow recognises the exact initial template during upgrades;
// arbitrary changes still close the ingestion gate.
func LegacySetupWorkflow(key RepositoryKey, config Config) (string, error) {
	w, err := SetupWorkflow(key, config)
	if err != nil {
		return "", err
	}
	for _, name := range []string{"generate", "publish"} {
		current, _ := setupTemplates.ReadFile("templates/" + name + ".py")
		legacy, _ := setupTemplates.ReadFile("templates/legacy_" + name + ".py")
		oldIndent := "          " + strings.ReplaceAll(strings.TrimSuffix(string(legacy), "\n"), "\n", "\n          ")
		w = strings.Replace(w, indentScript(string(current)), oldIndent, 1)
	}
	return w, nil
}

// olderGenerations are the generator and publisher scripts earlier releases put
// in installed workflows, newest first. Installed workflows are verified byte
// for byte, so a template change that left them unrecognised would close the
// access gate of every enabled repository until somebody ran a repair. Add the
// scripts being replaced here, in the same change that replaces them; the
// initial template is handled by LegacySetupWorkflow.
var olderGenerations = []struct{ generate, publish string }{
	// Named oversized files, but refused the run when there was one.
	{"templates/named_generate.py", "templates/older_publish.py"},
	// Said only that "a source file exceeds the context size limit".
	{"templates/previous_generate.py", "templates/older_publish.py"},
}

func olderSetupWorkflow(key RepositoryKey, config Config, generation int) (string, error) {
	w, err := SetupWorkflow(key, config)
	if err != nil {
		return "", err
	}
	g := olderGenerations[generation]
	for _, swap := range [][2]string{{"templates/generate.py", g.generate}, {"templates/publish.py", g.publish}} {
		current, _ := setupTemplates.ReadFile(swap[0])
		older, _ := setupTemplates.ReadFile(swap[1])
		w = strings.Replace(w, indentScript(string(current)), indentScript(string(older)), 1)
	}
	return w, nil
}

// PreviousSetupWorkflow is the template immediately before the current one.
func PreviousSetupWorkflow(key RepositoryKey, config Config) (string, error) {
	return olderSetupWorkflow(key, config, 0)
}

// IsOlderSetupWorkflow reports whether content is exactly a workflow an earlier
// Zoomies release wrote for this repository and configuration. Such a workflow
// is still owned and still safe; it is merely due a repair. Anything else is
// somebody's edit.
func IsOlderSetupWorkflow(key RepositoryKey, config Config, content string) bool {
	for i := range olderGenerations {
		if w, err := olderSetupWorkflow(key, config, i); err == nil && w == content {
			return true
		}
	}
	if w, err := LegacySetupWorkflow(key, config); err == nil && w == content {
		return true
	}
	return false
}

// PlanManagedSetup adds executable workflow/toolchain files only when they are
// absent or byte-for-byte owned. User edits never become silent upgrades.
func PlanManagedSetup(key RepositoryKey, name string, config Config, files []SetupFile) ([]SetupChange, error) {
	workflow, err := SetupWorkflow(key, config)
	if err != nil {
		return nil, err
	}
	lock, _ := setupTemplates.ReadFile("templates/package-lock.json")
	packageJSON := `{"name":"zoomies-ai-context","private":true,"dependencies":{"repomix":"1.18.1"}}` + "\n"
	desired := map[string]string{WorkflowPath: workflow, GeneratorPackagePath: packageJSON, GeneratorLockPath: string(lock)}
	basic := make([]SetupFile, 0, len(files))
	seen := map[string]bool{}
	for _, file := range files {
		if file.Mode != "" && file.Mode != "100644" && file.Mode != "100755" {
			return nil, fmt.Errorf("setup file %q must be a regular file", file.Path)
		}
		if seen[file.Path] {
			return nil, fmt.Errorf("setup includes duplicate file %q", file.Path)
		}
		seen[file.Path] = true
		if want, ok := desired[file.Path]; ok {
			if file.SHA == "" && file.Content != "" || file.SHA != "" && !commitPattern.MatchString(file.SHA) {
				return nil, fmt.Errorf("setup file %q needs a valid blob identity", file.Path)
			}
			if file.SHA != "" && file.Content != want {
				return nil, fmt.Errorf("%s already exists with different contents; review a repair or upgrade", file.Path)
			}
			if file.SHA != "" {
				delete(desired, file.Path)
			}
		} else {
			basic = append(basic, file)
		}
	}
	changes, err := PlanSetupFiles(key, name, config, basic)
	if err != nil {
		return nil, err
	}
	for _, p := range []string{WorkflowPath, GeneratorPackagePath, GeneratorLockPath} {
		if content, ok := desired[p]; ok {
			changes = append(changes, SetupChange{Mode: "100644", Path: p, Content: content})
		}
	}
	return changes, nil
}

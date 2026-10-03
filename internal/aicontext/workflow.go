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
	if config.Destination == Zoomies {
		return "", fmt.Errorf("zoomies-only output needs secure uploads")
	}
	// Artifact actions v4+ are unavailable on GHES. Refuse a workflow that
	// would only fail after merge; Enterprise templates need a separate pilot.
	if key.GitHubHost != "github.com" {
		return "", fmt.Errorf("managed context workflows currently require GitHub.com; GitHub Enterprise artifact support needs a separate workflow template")
	}
	cfg, _ := json.Marshal(managedConfig{Manager: "zoomies-ai-context", TemplateVersion: SetupTemplateVersion, Config: config})
	identity, _ := json.Marshal(key)
	hash, _ := config.Hash()
	branch, _ := json.Marshal(config.SourceBranch)
	pushBranch, _ := json.Marshal(strings.NewReplacer("!", `\!`, "+", `\+`).Replace(config.SourceBranch))
	generate, _ := setupTemplates.ReadFile("templates/generate.py")
	publish, _ := setupTemplates.ReadFile("templates/publish.py")
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
  publish:
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
	// GitHub expressions need a single-quoted literal, with apostrophes
	// doubled; JSON/YAML quoting protects the surrounding YAML scalar.
	trustedRef := quote("${{ github.ref == 'refs/heads/" + strings.ReplaceAll(config.SourceBranch, "'", "''") + "' }}")
	condition := "${{ github.ref == 'refs/heads/" + strings.ReplaceAll(config.SourceBranch, "'", "''") + "' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch') }}"
	workflow = strings.ReplaceAll(workflow, "github.ref == TRUSTED_REF && (github.event_name == 'push' || github.event_name == 'workflow_dispatch')", quote(condition))
	workflow = strings.ReplaceAll(workflow, "github.ref == TRUSTED_REF", trustedRef)
	workflow = strings.NewReplacer("@@PUSH_BRANCH@@", string(pushBranch), "@@BRANCH@@", string(branch), "@@CONFIG@@", quote(string(cfg)), "@@IDENTITY@@", quote(string(identity)), "@@HASH@@", quote(hash), "@@GENERATE@@", indentScript(string(generate)), "@@PUBLISH@@", indentScript(string(publish))).Replace(workflow)
	return workflow, nil
}

func indentScript(s string) string {
	return "          " + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n          ")
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

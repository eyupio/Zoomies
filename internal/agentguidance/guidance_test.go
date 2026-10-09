package agentguidance

import (
	"slices"
	"strings"
	"testing"
)

func kinds(issues []Issue) []string {
	var out []string
	for _, i := range issues {
		out = append(out, i.Kind)
	}
	slices.Sort(out)
	return out
}

func TestValidSingleFilesAndRelativeImportsNeedNoRestructuring(t *testing.T) {
	for _, p := range []string{"AGENTS.md", "CLAUDE.md", "GEMINI.md", "src/AGENTS.md", ".github/copilot-instructions.md"} {
		f := []File{{Path: p, Content: "Follow existing conventions.\n"}}
		if got := Inspect(f, []string{p}, true); len(got) != 0 {
			t.Fatalf("%s: %+v", p, got)
		}
		if got := Plan(f, []string{p}); len(got) != 0 {
			t.Fatalf("%s: %+v", p, got)
		}
	}
	f := []File{{Path: "AGENTS.md", Content: "Read [build](docs/build%20guide.md).\n```sh\n[example](missing.md)\n```\n"}, {Path: ".claude/CLAUDE.md", Content: "@../AGENTS.md\n"}}
	if got := Inspect(f, []string{"AGENTS.md", ".claude/CLAUDE.md", "docs/build guide.md"}, true); len(got) != 0 {
		t.Fatalf("valid references: %+v", got)
	}
}

func TestMissingGuidanceUsesDeclaredCommandsWithoutExecutingThem(t *testing.T) {
	f := []File{{Path: "Makefile", Content: "build: ui\n\techo danger\ntest: ## tests\n\techo tests\n"}, {Path: "package.json", Content: `{"scripts":{"lint":"do something","test;danger":"ignored"}}`}}
	plan := Plan(f, []string{"Makefile", "package.json"})
	if len(plan) != 2 || plan[0].Path != "AGENTS.md" || plan[1].Content != "@AGENTS.md\n" {
		t.Fatalf("plan=%+v", plan)
	}
	for _, cmd := range []string{"make build", "make test", "npm run lint"} {
		if !strings.Contains(plan[0].Content, cmd) {
			t.Errorf("missing %s", cmd)
		}
	}
	if strings.Contains(plan[0].Content, "danger") {
		t.Fatal("script contents reached generated instructions")
	}
}

func TestOnlyExactDuplicatesAndUnambiguousImportsAreRepaired(t *testing.T) {
	f := []File{{Path: "AGENTS.md", Content: "# Rules\n\nKeep user guidance.\n"}, {Path: "CLAUDE.md", Content: "# Rules\r\n\r\nKeep user guidance.\r\n", SHA: "original", Mode: "100755"}, {Path: ".claude/CLAUDE.md", Content: "# Claude\r\n\r\n@AGENTS.md\r\n\r\nKeep my extra rules.\r\n", SHA: "nested", Mode: "100644"}}
	plan := Plan(f, []string{"AGENTS.md", "CLAUDE.md", ".claude/CLAUDE.md"})
	if len(plan) != 2 {
		t.Fatalf("plan=%+v", plan)
	}
	for _, change := range plan {
		if change.Path == "CLAUDE.md" && (change.Content != "@AGENTS.md\r\n" || change.Mode != "100755" || change.PreviousSHA != "original") {
			t.Fatalf("duplicate=%+v", change)
		}
		if change.Path == ".claude/CLAUDE.md" && change.Content != "# Claude\r\n\r\n@../AGENTS.md\r\n\r\nKeep my extra rules.\r\n" {
			t.Fatalf("custom text changed: %+v", change)
		}
	}
	f[1].Content += "Extra rules.\n"
	if plan := Plan(f, []string{"AGENTS.md", "CLAUDE.md", ".claude/CLAUDE.md", "src/AGENTS.md"}); len(plan) != 0 {
		t.Fatalf("ambiguous or customised guidance rewritten: %+v", plan)
	}
}

func TestBrokenLinksCyclesAndInvalidTextAreReportedWithoutRepeatingProse(t *testing.T) {
	f := []File{{Path: "CLAUDE.md", Content: "@nested/CLAUDE.md\n[bad](../../outside.md)\n"}, {Path: "nested/CLAUDE.md", Content: "@../CLAUDE.md\n"}, {Path: "AGENTS.md", Content: string([]byte{0xff})}}
	got := Inspect(f, []string{"CLAUDE.md", "nested/CLAUDE.md", "AGENTS.md"}, true)
	if !slices.Equal(kinds(got), []string{"broken_reference", "broken_reference", "broken_reference", "unreadable"}) {
		t.Fatalf("issues=%+v", got)
	}
	if plan := Plan(f, []string{"CLAUDE.md", "nested/CLAUDE.md", "AGENTS.md"}); len(plan) != 0 {
		t.Fatalf("unsafe repair=%+v", plan)
	}
	if got := Inspect(nil, nil, false); len(got) != 0 {
		t.Fatalf("partial inventory established absence: %+v", got)
	}
}

func TestAnUnambiguousImportRepairNeverIntroducesACycle(t *testing.T) {
	files := []File{{Path: "CLAUDE.md", Content: "@nested/CLAUDE.md\n"}, {Path: "nested/CLAUDE.md", Content: "@CLAUDE.md\n"}}
	if plan := Plan(files, []string{"CLAUDE.md", "nested/CLAUDE.md"}); len(plan) != 0 {
		t.Fatalf("repair introduces cycle: %+v", plan)
	}
	files = []File{{Path: "AGENTS.md", Content: "@CLAUDE.md\n"}, {Path: "CLAUDE.md", Content: "@AGENTS.md\n"}}
	if got := Inspect(files, []string{"AGENTS.md", "CLAUDE.md"}, true); len(got) != 2 {
		t.Fatalf("imported shared guidance cycle not detected: %+v", got)
	}
}

func TestCommentedAndInlineExamplesDoNotBecomeBrokenReferences(t *testing.T) {
	files := []File{{Path: "AGENTS.md", Content: "<!-- [example](absent.md)\n[another](absent.md) -->\n`[syntax](absent.md)`\n"}}
	if got := Inspect(files, []string{"AGENTS.md"}, true); len(got) != 0 {
		t.Fatalf("examples were judged: %+v", got)
	}
}

func TestImportRepairsPreserveMixedLineEndings(t *testing.T) {
	files := []File{{Path: "AGENTS.md", Content: "Rules.\n"}, {Path: ".claude/CLAUDE.md", Content: "# Claude\r\n\n  @AGENTS.md\r\n\nCustom guidance.\n"}}
	plan := Plan(files, []string{"AGENTS.md", ".claude/CLAUDE.md"})
	if len(plan) != 1 || plan[0].Content != "# Claude\r\n\n  @../AGENTS.md\r\n\nCustom guidance.\n" {
		t.Fatalf("mixed endings changed: %+v", plan)
	}
}

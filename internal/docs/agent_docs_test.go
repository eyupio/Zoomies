package docs

import (
	"os"
	"slices"
	"strings"
	"testing"
)

// A person whose job did not run, or failed, is on the queued-job page or the
// FAQ before they are anywhere else. Both send them to the fleet's own verdict
// before a log, and the FAQ's answer about the offline check says what a
// laptop cannot check, so "nothing found" is not read as an all clear.
func TestTheQueuedJobPageAndTheFAQSendAReaderToTheVerdictFirst(t *testing.T) {
	for page, wants := range map[string][]string{
		"../../docs/queued-job.md": {"## 6. Ask the fleet why", "`zoomies why", "before it reads a log"},
		"../../docs/faq.md": {
			"## Why did my job fail, and how do I find out?",
			"## Can a coding agent check my workflow files without sending them anywhere?",
			"`zoomies kennel check", "not checked here",
		},
	} {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(raw), want) {
				t.Errorf("%s does not say %q", page, want)
			}
		}
	}
}

// A row's "See" link is the only onward route a reference page gives, so it
// has to land on the page that actually documents the command: the provider
// contract page never mentions the wizard, and a link that resolves to the
// wrong page is one the strict site build cannot catch.
func TestTheConnectProxmoxRowSendsAReaderToTheProxmoxPage(t *testing.T) {
	raw, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, "`providers connect-proxmox`") {
			continue
		}
		if !strings.Contains(line, "proxmox.md#the-provider") {
			t.Errorf("the connect-proxmox row does not link to proxmox.md#the-provider:\n%s", line)
		}
		return
	}
	t.Fatal("docs/cli.md has no `providers connect-proxmox` row")
}

// The CLI page is written by hand and the binary's command list is not, so
// the two drift unless something holds them together: every subcommand the
// binary has is named on the page, with its command, in the backticks the
// page's tables use. A bare verb does not count, because a reader searching
// for "zoomies kennel recheck" would not find "recheck" under a heading.
func TestEverySubcommandIsDocumented(t *testing.T) {
	raw, err := os.ReadFile("../../docs/cli.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	names, groups := commandNames(t)
	var missing []string
	for name := range names {
		words := strings.Fields(name)
		if len(words) != 3 || !groups[words[0]+" "+words[1]] {
			continue
		}
		two := words[1] + " " + words[2]
		if strings.Contains(page, "`zoomies "+two) || strings.Contains(page, "`"+two+"`") || strings.Contains(page, "`"+two+" ") {
			continue
		}
		missing = append(missing, name)
	}
	slices.Sort(missing)
	if len(missing) > 0 {
		t.Errorf("these subcommands exist and are not on docs/cli.md:\n  %s", strings.Join(missing, "\n  "))
	}
}

// Review: three sentences promised a refusal code the API does not send.
// The assistant's private-address refusal is a 422 whose base_url field
// error names the switch; the code egress.private_target is what a value in
// the file is warned about as. The pages say which is which.
func TestTheAssistantSwitchDocsSayHowTheRefusalReads(t *testing.T) {
	for _, page := range []string{"../../docs/security.md", "../../docs/configuration.md"} {
		raw, err := os.ReadFile(page)
		if err != nil {
			t.Fatal(err)
		}
		text := strings.Join(strings.Fields(string(raw)), " ")
		if !strings.Contains(text, "names this switch in the `base_url` field error") {
			t.Errorf("%s does not say the 422 names the switch in the base_url field error", page)
		}
	}
}

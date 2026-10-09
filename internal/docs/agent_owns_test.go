package docs

import (
	"os"
	"strings"
	"testing"
)

// The enrolment command asks a team to hand part of its own machine to the
// agent, and "what the agent owns" is the promise that makes that a bounded
// request. It is only a promise while it is written where the command links to,
// so the section, its facts and both links to it are kept here: a page edit
// that drops one of them is a promise quietly withdrawn.
func TestTheSecurityPageSaysWhatTheAgentOwnsOnAHost(t *testing.T) {
	security, err := os.ReadFile("../../docs/security.md")
	if err != nil {
		t.Fatal(err)
	}
	page := string(security)
	const heading = "### What the agent owns on a host\n"
	start := strings.Index(page, heading)
	if start < 0 {
		t.Fatal("docs/security.md has no \"What the agent owns on a host\" section, and the Add-a-host page and the private-hosts page both link to it")
	}
	section := page[start+len(heading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	if end := strings.Index(section, "\n### "); end >= 0 {
		section = section[:end]
	}
	for _, want := range []struct{ fact, why string }{
		{"`zoomies-agent`", "its service"},
		{"`zoomies` system user", "the user it runs as"},
		{"`agent.work_dir`", "its work directory"},
		{"`io.zoomies.managed=true`", "the label that is the whole boundary of which containers it touches"},
		{"zoomies-cache-<identity>", "its per-pool cache"},
		{"never removes an image", "what it never prunes"},
		{"`agent.docker_build_cache_mb`", "the builder-cache target, the one daemon-wide thing it does"},
		{"default `0`", "the builder-cache default"},
		{"agent.docker_build_cache_mb: 0", "the shared-daemon advice"},
		{"`zoomies-update.path`", "the helper's trigger, present only where its owner installed it"},
		{"`request.json`", "the update folder the service writes a request into and the helper reads"},
		{"is not the agent", "that the update helper is root's and not the agent's"},
	} {
		if !strings.Contains(section, want.fact) {
			t.Errorf("the \"What the agent owns on a host\" section no longer says %s (%q)", want.why, want.fact)
		}
	}
}

func TestThePrivateHostsPageAndTheAddAHostPageLinkWhatTheAgentOwns(t *testing.T) {
	for _, link := range []struct{ file, needle string }{
		{"../../docs/private-hosts.md", "security.md#what-the-agent-owns-on-a-host"},
		{"../../web/src/lib/links.ts", "#what-the-agent-owns-on-a-host"},
		{"../../web/src/lib/hosts/AddHostFlow.svelte", "AGENT_OWNS_URL"},
	} {
		body, err := os.ReadFile(link.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), link.needle) {
			t.Errorf("%s does not link what the agent owns on a host (%q)", strings.TrimPrefix(link.file, "../../"), link.needle)
		}
	}
}

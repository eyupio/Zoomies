package proxmox

import (
	"context"
	"fmt"

	"github.com/eyupio/zoomies/internal/config"
)

// SetupAnswers is what an installer knows about a provider before anybody has
// been asked anything: the node it is running on, the bridge and template it
// has just found or built.
type SetupAnswers struct {
	Node         string
	Bridge       string
	TemplateID   int
	TemplateNode string
}

// SetupCheck runs the check the Check button runs, as the token setup has just
// created, and returns only what stops the provider working.
//
// The point is to learn on the Proxmox host, with the exact reason and before
// anything is saved, that a token cannot do what the controller will ask of
// it. A provider that is saved first and fails its first check on the other
// side of a private connection sends an operator hunting for a cause that was
// plain on the host.
//
// The storage is the one answer an installer does not have, so it is chosen the
// way the template step chooses one: an active storage that takes disk images,
// the one with most room. Whether the operator later picks another is not this
// check's business; whether the token can see any is.
func SetupCheck(ctx context.Context, c *Client, a SetupAnswers) []config.Finding {
	storage := ""
	if storages, err := c.Storages(ctx, a.Node); err == nil {
		var best int64
		for _, s := range storages {
			if !s.Accepts("images") || !bool(s.Enabled) || !bool(s.Active) {
				continue
			}
			if storage == "" || int64(s.Avail) > best {
				storage, best = s.Storage, int64(s.Avail)
			}
		}
	}

	report := Preflight(ctx, c, Prereqs{
		Nodes: []string{a.Node}, Storage: storage, Bridge: a.Bridge,
		TemplateNode: a.TemplateNode, TemplateID: a.TemplateID,
		// The default runner block, which the form offers and the template
		// deliberately sits outside.
		VMIDMin: 9000, VMIDMax: 9099, GuestAgent: true,
	})

	var out []config.Finding
	for _, f := range config.Findings(report.Findings).Errors() {
		if storage == "" && f.Setting == SettingStorage {
			continue // Replaced below by the one finding that says what is true.
		}
		out = append(out, f)
	}
	if storage == "" && report.Reachable {
		out = append(out, config.Finding{
			Code: "proxmox.storage_missing", Severity: config.SeverityError, Setting: SettingStorage,
			Title:  fmt.Sprintf("the API token sees no storage on %s that can hold disk images", a.Node),
			Detail: "a clone has nowhere to put the machine's disk.",
			Fix: fmt.Sprintf("grant the token Datastore.Audit on /storage (pveum acl modify /storage --tokens '%s' --roles <role with Datastore.Audit>), "+
				"and make sure a storage on this node is enabled for disk images.", c.TokenID()),
		})
	}
	return out
}

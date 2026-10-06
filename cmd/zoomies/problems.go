package main

import (
	"context"
	"fmt"
	"strings"
)

// runProblems is `zoomies problems ...`: what the controller thinks is wrong, and the
// changes it proposes for the ones it has worked out a change for.
func runProblems(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "problems", "What is wrong, and the changes the controller proposes for it.", []*subcommand{
		{"list", "[--proposals]", "Everything wrong now, and the change proposed for each where there is one", problemsList},
		{"apply", "<code> [--target <id>]", "Make the change the controller proposes for a problem", problemsApply},
	}, args)
}

func problemsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies problems list [--proposals]", "List what the controller thinks is wrong. Where it has worked out and priced a change, the last columns say what and at what cost, and `zoomies problems apply` makes it.")
	cf := registerClientFlags(fs, true)
	proposals := fs.Bool("proposals", false, "only the problems that carry a proposed change")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var out problemsResponse
	raw, err := client.get(ctx, "/problems", nil, &out)
	if err != nil {
		return err
	}
	out.sanitise()
	if p.structured() {
		return p.emit(raw)
	}
	rows := [][]string{}
	for _, it := range out.Items {
		if *proposals && it.Remedy == nil {
			continue
		}
		change, effect := "", ""
		if it.Remedy != nil {
			change, effect = it.Remedy.Label, it.Remedy.Effect
		}
		rows = append(rows, []string{it.Severity, it.Code, dash(it.TargetID), it.Title, dash(change), dash(effect)})
	}
	if len(rows) == 0 {
		if *proposals {
			p.note("Nothing is proposing a change right now.")
		} else {
			p.note("Nothing is wrong.")
		}
		return nil
	}
	p.table([]string{"severity", "code", "target", "problem", "proposed change", "cost"}, rows)
	return nil
}

func problemsApply(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies problems apply <code> [--target <id>] [--dry-run]",
		"Make the change the controller proposes for a problem. It is the pool's or host's own update, made as you, so it needs the role that update needs, "+
			"it is refused when it would leave a pool with nowhere to run, and it is on the audit trail. Only a change the controller is proposing now is made.")
	cf := registerClientFlags(fs, true)
	target := fs.String("target", "", "the pool or host, when more than one has a proposal for this problem")
	remedyID := fs.String("remedy", "", "the proposal's ID from `zoomies problems list --output json`, to apply exactly that one")
	dry := fs.Bool("dry-run", false, "say what would be changed, and make no change")
	fs.example(
		"zoomies problems list --proposals",
		"zoomies problems apply host.slots_below_capacity --dry-run",
		"zoomies problems apply pool.daemon_share_suggested --target pool_k3f9qz2m",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	code, err := fs.oneArg("a problem code, as `zoomies problems list` shows it")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	var list problemsResponse
	if _, err := client.get(ctx, "/problems", nil, &list); err != nil {
		return err
	}
	list.sanitise()
	var matches []problemItem
	for _, it := range list.Items {
		if it.Code == code && it.Remedy != nil && (*target == "" || it.TargetID == *target) {
			matches = append(matches, it)
		}
	}
	switch len(matches) {
	case 0:
		return fmt.Errorf("the controller is not proposing a change for %s%s; `zoomies problems list --proposals` shows what it is", code, onTarget(*target))
	case 1:
	default:
		var names []string
		for _, m := range matches {
			names = append(names, m.TargetID)
		}
		return usagef("problems apply", "%s has a proposal for more than one target (%s); name one with --target", code, strings.Join(names, ", "))
	}
	m := matches[0]
	if *dry {
		p.keyValues([][2]string{{"problem", m.Title}, {"change", m.Remedy.Label}, {"cost", dash(m.Remedy.Effect)},
			{"kind", m.Remedy.Kind}, {"target", m.TargetID}, {"request", string(m.Remedy.Body)}})
		p.note("Dry run: nothing was changed.")
		return nil
	}
	id := firstNonBlank(*remedyID, m.Remedy.ID)
	var done struct {
		Applied bool `json:"applied"`
		Remedy  struct {
			Label string `json:"label"`
		} `json:"remedy"`
	}
	raw, err := client.post(ctx, "/problems/apply", nil, map[string]any{"code": code, "target_id": m.TargetID, "remedy_id": id}, &done)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	p.note("Done: %s on %s.", done.Remedy.Label, m.TargetID)
	if m.Remedy.Effect != "" {
		p.note("  %s.", m.Remedy.Effect)
	}
	p.note("It applies to runners created from now on.")
	return nil
}

func onTarget(target string) string {
	if target == "" {
		return ""
	}
	return " on " + target
}

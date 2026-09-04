package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// runInstallations is `zoomies installations ...`.
func runInstallations(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "installations", "The GitHub App installations pools register runners with.", []*subcommand{
		{"list", "", "Every installation and whether its credentials still work", installationsList},
		{"verify", "<installation-id>", "Probe the credentials and permissions now", installationsVerify},
	}, args)
}

func installationsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies installations list", "List GitHub App installations. Key material is never included.")
	cf := registerClientFlags(fs, true)
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

	var out listResponse[installationItem]
	raw, err := client.get(ctx, "/installations", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No installations. Add one in the UI under Installations, which walks you through creating the GitHub App.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, inst := range out.Items {
		health := p.paint(colourGreen, "ok")
		if !inst.Healthy {
			health = p.paint(colourRed, "failing")
		}
		rows = append(rows, []string{
			inst.Target,
			inst.ID,
			inst.TargetType,
			health,
			strconv.Itoa(inst.PoolCount),
			p.relTimePtr(inst.LastCheckedAt),
			truncate(dash(inst.LastError), 40),
		})
	}
	p.table([]string{"target", "id", "kind", "health", "pools", "checked", "last error"}, rows)
	return nil
}

func installationsVerify(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies installations verify <installation-id>",
		"Ask GitHub whether this installation's credentials and permissions are still what Zoomies needs.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("an installation ID, as shown by `zoomies installations list`")
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

	var health installationHealth
	raw, err := client.post(ctx, "/installations/"+url.PathEscape(id)+"/verify", nil, nil, &health)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	rows := [][2]string{
		{"result", map[bool]string{true: p.paint(colourGreen, "ok"), false: p.paint(colourRed, "not usable")}[health.OK]},
		{"app", dash(health.AppName)},
		{"slug", dash(health.AppSlug)},
		{"message", dash(health.Message)},
	}
	if health.RateLimitRemaining > 0 {
		rows = append(rows, [2]string{"api quota left", strconv.Itoa(health.RateLimitRemaining)})
	}
	p.keyValues(rows)

	if len(health.MissingPermissions) > 0 {
		fmt.Fprintf(p.out, "\nMissing permissions: %s\n", strings.Join(health.MissingPermissions, ", "))
		fmt.Fprintln(p.out, "Grant them on the App's settings page, then accept the request on the installation.")
	}
	if len(health.MissingEvents) > 0 {
		fmt.Fprintf(p.out, "Missing webhook events: %s\n", strings.Join(health.MissingEvents, ", "))
		fmt.Fprintln(p.out, "Without workflow_job this controller only scales on the fallback poller.")
	}
	if !health.OK {
		return fmt.Errorf("installation %s cannot be used as it stands", id)
	}
	return nil
}

package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// runProviders is `zoomies providers ...`: where machines are rented from, and
// the machines themselves, over the same routes the UI uses.
//
// Every verb here is one an operator reaches for when something is wrong at an
// hour when nobody wants to open a browser: is this hypervisor answering, stop
// buying machines, what is out there that we have lost track of.
func runProviders(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "providers", "Where machines are rented from, and the machines themselves.", []*subcommand{
		{"list", "", "Every provider, with its machines and whether it is buying", providersList},
		{"kinds", "", "What this build can rent from, and the settings each kind asks for", providersKinds},
		{"connect-proxmox", "[flags]", "Connect a Proxmox host using the provider wizard's setup command", providersConnectProxmox},
		{"add", "<kind> [flags]", "Add a provider from the terminal, then ask it what it would refuse", providersAdd},
		{"edit", "<name|id> [flags]", "Change the settings you name and nothing else", providersEdit},
		{"check", "<name|id>", "Ask a provider what it would refuse, changing nothing", providersCheck},
		{"pause", "<name|id>", "Stop buying new machines; drains and deletes continue", providersPause},
		{"resume", "<name|id>", "Let it buy machines again", providersResume},
		{"machines", "", "The machines that exist right now", providersMachines},
		{"orphans", "<name|id>", "Resources and rows that disagree, for a person to decide", providersOrphans},
	}, args)
}

func providersList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers list [--output table|json|yaml]",
		"List every provider, with how many machines it has and whether it may buy more.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies providers list", "zoomies providers list --output json | jq '.items[].name'")
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

	var out listResponse[providerItem]
	raw, err := client.get(ctx, "/providers", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No providers. Add one with: zoomies providers add proxmox --name <n> --endpoint <url> ... (or in the UI under Providers); a credential belongs in the database, sealed, rather than in a configuration file.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, item := range out.Items {
		buying := p.paint(colourGreen, "yes")
		switch {
		case item.Paused:
			buying = p.paint(colourYellow, "paused")
		case !item.Enabled:
			buying = p.paint(colourDim, "disabled")
		case item.Held != "":
			buying = p.paint(colourYellow, "held")
		}
		rows = append(rows, []string{
			item.Name,
			item.ID,
			item.Kind,
			dash(item.Endpoint),
			fmt.Sprintf("%d/%d", item.Owned, item.MaxMachines),
			machineStates(item.Machines),
			buying,
			p.relTimePtr(item.LastCheckAt),
		})
	}
	p.table([]string{"name", "id", "kind", "endpoint", "machines", "states", "buying", "last check"}, rows)

	// Why a provider is buying nothing is the question this table exists to
	// answer, and it is a sentence rather than a column.
	for _, item := range out.Items {
		if item.Held != "" {
			p.note("%s: %s", item.Name, item.Held)
		}
		if item.LastCheckError != "" {
			p.note("%s: the last check said: %s", item.Name, item.LastCheckError)
		}
	}
	return nil
}

// providersCheck runs the live preflight and prints it with printFindings, so
// the terminal and the wizard say the same words about the same provider.
func providersCheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers check <name|id>",
		"Ask a provider what it would refuse. It changes nothing, and exits non-zero if anything would stop a machine being created.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies providers check proxmox-lab")
	if err := fs.parse(args); err != nil {
		return err
	}
	ref, err := fs.oneArg("a provider name or ID, as shown by `zoomies providers list`")
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
	provider, err := resolveProvider(ctx, client, ref)
	if err != nil {
		return err
	}
	if p.structured() {
		var report providerCheckItem
		raw, err := client.post(ctx, "/providers/"+url.PathEscape(provider.ID)+"/check", nil, nil, &report)
		if err != nil {
			return err
		}
		return p.emit(raw)
	}
	return checkProvider(ctx, e, p, client, provider)
}

func providersPause(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers pause <name|id> [--reason \"...\"]",
		"Stop a provider buying new machines. Draining, deleting and recovering carry on.")
	cf := registerClientFlags(fs, true)
	reason := fs.String("reason", "", "why, for the card and the audit row")
	fs.example(`zoomies providers pause proxmox-lab --reason "the cluster is being patched"`)
	if err := fs.parse(args); err != nil {
		return err
	}
	ref, err := fs.oneArg("a provider name or ID")
	if err != nil {
		return err
	}
	return setProviderPaused(ctx, e, cf, ref, true, *reason)
}

func providersResume(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers resume <name|id>",
		"Let a provider buy machines again.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies providers resume proxmox-lab")
	if err := fs.parse(args); err != nil {
		return err
	}
	ref, err := fs.oneArg("a provider name or ID")
	if err != nil {
		return err
	}
	return setProviderPaused(ctx, e, cf, ref, false, "")
}

// setProviderPaused is both halves of the kill switch. Pressing it twice is not
// an error on the server and is not one here either: the person doing it wants
// the fleet in a known state, not an argument about whether it was already.
func setProviderPaused(ctx context.Context, e *env, cf *clientFlags, ref string, paused bool, reason string) error {
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	provider, err := resolveProvider(ctx, client, ref)
	if err != nil {
		return err
	}
	verb := "resume"
	var body any
	if paused {
		verb = "pause"
		body = map[string]string{"reason": reason}
	}

	var out providerItem
	raw, err := client.post(ctx, "/providers/"+url.PathEscape(provider.ID)+"/"+verb, nil, body, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if paused {
		p.note("%s is buying no new machines. Machines it already has keep running, and drains, deletes and recovery carry on.", out.Name)
		if out.Held != "" {
			p.note("%s", out.Held)
		}
		return nil
	}
	p.note("%s may buy machines again.", out.Name)
	if out.Held != "" {
		// Resuming one switch does not mean the fleet is buying: the fence,
		// the configuration and the ceiling each hold it too.
		p.note("It still is not: %s", out.Held)
	}
	return nil
}

func providersMachines(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers machines [--provider X] [--state ready]",
		"List the machines that exist right now, and what each one is waiting on.")
	cf := registerClientFlags(fs, true)
	provider := fs.String("provider", "", "only this provider's machines, by name or ID")
	state := fs.String("state", "", "only machines in this state, e.g. ready, creating, quarantined")
	includeDeleted := fs.Bool("include-deleted", false, "keep machines whose resource is confirmed gone")
	fs.example("zoomies providers machines --state quarantined", "zoomies providers machines --provider proxmox-lab")
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

	q := url.Values{}
	if strings.TrimSpace(*provider) != "" {
		row, err := resolveProvider(ctx, client, *provider)
		if err != nil {
			return err
		}
		q.Set("provider", row.ID)
	}
	if s := strings.TrimSpace(*state); s != "" {
		q.Set("state", s)
	}
	if *includeDeleted {
		q.Set("include_deleted", "true")
	}

	var out listResponse[machineItem]
	raw, err := client.get(ctx, "/machines", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No machines.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, m := range out.Items {
		rows = append(rows, []string{
			m.Name,
			m.ID,
			dash(m.ProviderName),
			p.paint(machineStateColour(m.State), m.State),
			dash(m.HostName),
			dash(resourceOf(m)),
			dash(m.Address),
			p.relTime(m.CreatedAt),
		})
	}
	p.table([]string{"name", "id", "provider", "state", "host", "resource", "address", "age"}, rows)

	// A machine that is stuck says why here rather than making somebody open
	// the page: the operation handle is what an operator pastes into the
	// provider's own task log.
	for _, m := range out.Items {
		switch {
		case m.ProviderError != "":
			p.note("%s: %s", m.Name, m.ProviderError)
		case m.BootstrapError != "":
			p.note("%s: %s", m.Name, m.BootstrapError)
		case m.Message != "" && (m.State == "failed" || m.State == "quarantined"):
			p.note("%s: %s", m.Name, m.Message)
		}
		if m.Operation != "" && m.OperationHandle != "" {
			p.note("%s: %s in flight, provider task %s", m.Name, m.Operation, m.OperationHandle)
		}
	}
	return nil
}

// providersOrphans prints the review page in a terminal. It offers to delete
// nothing: every row here is something for a person to look at, and the one
// destructive act needs the machine's name typed into the API.
func providersOrphans(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies providers orphans <name|id>",
		"Show the resources and rows that disagree: untracked resources, rows holding nothing, and machines nobody can vouch for.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies providers orphans proxmox-lab")
	if err := fs.parse(args); err != nil {
		return err
	}
	ref, err := fs.oneArg("a provider name or ID")
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
	provider, err := resolveProvider(ctx, client, ref)
	if err != nil {
		return err
	}

	var out orphanReport
	raw, err := client.get(ctx, "/providers/"+url.PathEscape(provider.ID)+"/orphans", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	if len(out.Untracked) == 0 && len(out.NoResource) == 0 && len(out.Unverified) == 0 {
		p.note("Every machine %s has is accounted for. Last swept %s.", provider.Name, p.relTimePtr(out.LastSweepAt))
		return nil
	}
	if len(out.Untracked) > 0 {
		fmt.Fprintf(e.out, "\nResources with no row (Zoomies will never delete these):\n")
		for _, o := range out.Untracked {
			fmt.Fprintf(e.out, "  - %s\n      %s\n", o.Name, o.Note)
		}
	}
	if len(out.NoResource) > 0 {
		fmt.Fprintf(e.out, "\nRows holding no resource (releasing one loses nothing):\n")
		for _, m := range out.NoResource {
			fmt.Fprintf(e.out, "  - %s (%s) %s\n", m.Name, m.ID, dash(m.Message))
		}
	}
	if len(out.Unverified) > 0 {
		fmt.Fprintf(e.out, "\nMachines nobody can vouch for (nothing will act on these until you do):\n")
		for _, m := range out.Unverified {
			detail := m.OwnershipError
			if detail == "" {
				detail = m.SafeToDeleteWhy
			}
			fmt.Fprintf(e.out, "  - %s (%s) at %s\n      %s\n", m.Name, m.ID, dash(resourceOf(m)), dash(detail))
		}
	}
	fmt.Fprintln(e.out)
	p.note("Last swept %s.", p.relTimePtr(out.LastSweepAt))
	return nil
}

// resolveProvider turns what an operator typed into a provider, accepting
// either the name they know it by or the ID a log line quoted.
func resolveProvider(ctx context.Context, client *apiClient, ref string) (providerItem, error) {
	ref = strings.TrimSpace(ref)
	var out listResponse[providerItem]
	if _, err := client.get(ctx, "/providers", nil, &out); err != nil {
		return providerItem{}, err
	}
	var names []string
	for _, item := range out.Items {
		if item.ID == ref || strings.EqualFold(item.Name, ref) {
			return item, nil
		}
		names = append(names, item.Name)
	}
	if len(names) == 0 {
		return providerItem{}, fmt.Errorf("there are no providers configured, so %q is not one", ref)
	}
	return providerItem{}, fmt.Errorf("no provider called %q; this fleet has: %s", ref, strings.Join(names, ", "))
}

// machineStates renders the by-state counts a provider carries, in the order a
// machine lives through them so the line reads as a pipeline.
func machineStates(counts map[string]int) string {
	order := []string{"planned", "creating", "starting", "bootstrapping", "enrolling", "ready", "draining", "deleting", "failed", "quarantined"}
	parts := make([]string, 0, len(order))
	for _, state := range order {
		if n := counts[state]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+state)
		}
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

// machineStateColour is the fixed status mapping the UI uses, in the four
// colours a terminal has: pending states are the ones still being waited on,
// ready is good, and the two that need a person are red.
func machineStateColour(state string) string {
	switch state {
	case "ready":
		return colourGreen
	case "failed", "quarantined":
		return colourRed
	case "draining", "deleting":
		return colourYellow
	case "deleted":
		return colourDim
	}
	return colourCyan
}

// resourceOf names the resource behind a machine the way its provider does, so
// it can be pasted into the provider's own console.
func resourceOf(m machineItem) string {
	switch {
	case m.ResourceZone != "" && m.ResourceID != "":
		return m.ResourceZone + "/" + m.ResourceID
	case m.ResourceID != "":
		return m.ResourceID
	}
	return ""
}

func pluralWarnings(n int) string {
	if n == 1 {
		return "1 warning"
	}
	return strconv.Itoa(n) + " warnings"
}

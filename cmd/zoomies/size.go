package main

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// tagValue collects repeatable --tag flags: key=value, or a bare key, which is
// key=true. It is what makes `--tag gpu` mean what an operator expects, where
// the key=value flag every other command takes would refuse it.
//
// A flag is one tag and is never split on commas: a value may have one in it,
// and the flag is repeatable, so `--tag rack=b4,b5` is a rack called "b4,b5"
// rather than a rack called b4 and a tag called b5.
type tagValue map[string]string

func (m tagValue) String() string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+m[k])
	}
	return strings.Join(parts, ",")
}

func (m tagValue) Set(v string) error {
	part := strings.TrimSpace(v)
	k, val, found := strings.Cut(part, "=")
	if k = strings.TrimSpace(k); k == "" {
		return fmt.Errorf("%q is not a tag: write key=value, or a bare key for key=true", part)
	}
	if !found {
		val = "true"
	}
	m[k] = strings.TrimSpace(val)
	return nil
}

// hostSizeWithClass is a host's machine and the size class it is in, which is
// what the size column says once the controller has worked one out.
func hostSizeWithClass(h hostItem) string {
	out := hostSize(h)
	if h.SizeClass == nil || h.SizeClass.Class == "" {
		return out
	}
	class := h.SizeClass.Class
	if h.SizeClass.Source == "tag" {
		class += ", by tag"
	}
	if out == "" {
		return class
	}
	return out + " (" + class + ")"
}

// describeTags says what a host's operator tags are, with what an operator's
// size tag replaced, and nothing about the automatic ones: those are the
// machine's own, and the size column already shows the class.
func describeTags(h hostItem) string {
	var parts []string
	for _, t := range h.Tags {
		if t.Source != "operator" {
			continue
		}
		part := t.Key + "=" + t.Value
		if t.Overrides != "" {
			part += " (its machine would be " + t.Overrides + ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------
// size-pins
// ---------------------------------------------------------------------------

// runSizePins is `zoomies size-pins ...`: the jobs and repositories an operator
// has put in a size class by hand.
func runSizePins(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "size-pins", "Put a job, or a whole repository, in a size class by hand.", []*subcommand{
		{"list", "", "Every pin", sizePinsList},
		{"set", "<owner/repo> --class <small|medium|large> [--workflow <w> --job <j>]", "Pin a repository, or one job, to a class", sizePinsSet},
		{"delete", "<owner/repo> [--workflow <w> --job <j>]", "Take a pin away", sizePinsDelete},
	}, args)
}

func sizePinsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies size-pins list", "List the jobs and repositories an operator has put in a size class.")
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
	var out listResponse[sizePinItem]
	raw, err := client.get(ctx, "/size-pins", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No pins. A job is put in the class its runs say, or the default class; pin one with: zoomies size-pins set acme/widgets --class large")
		return nil
	}
	rows := make([][]string, 0, len(out.Items))
	for _, pin := range out.Items {
		pin.sanitise()
		what := "every job"
		if pin.Workflow != "" || pin.JobName != "" {
			what = pin.Workflow + " / " + pin.JobName
		}
		rows = append(rows, []string{pin.Repo, what, pin.Class, dash(pin.CreatedBy), p.relTime(pin.CreatedAt)})
	}
	p.table([]string{"repository", "jobs", "class", "pinned by", "when"}, rows)
	return nil
}

func sizePinsSet(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies size-pins set <owner/repo> --class <small|medium|large> [--workflow <w> --job <j>]",
		"Pin a repository, or one job in it, to a size class. It takes the place of what the job's runs measured, for the jobs that arrive and the ones already waiting.")
	cf := registerClientFlags(fs, true)
	class := fs.String("class", "", "small, medium or large")
	workflow := fs.String("workflow", "", "the workflow, with --job, to pin one job instead of the whole repository")
	job := fs.String("job", "", "the job's name, with --workflow")
	fs.example("zoomies size-pins set acme/widgets --class large",
		`zoomies size-pins set acme/widgets --workflow CI --job "Go (controller)" --class large`)
	if err := fs.parse(args); err != nil {
		return err
	}
	repo, err := fs.oneArg("a repository, written owner/name")
	if err != nil {
		return err
	}
	if *class == "" {
		return usagef("size-pins set", "name the class with --class small, --class medium or --class large")
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var out struct {
		Pin          sizePinItem `json:"pin"`
		Reclassified int         `json:"reclassified"`
	}
	raw, err := client.put(ctx, "/size-pins", nil, map[string]any{
		"repo": repo, "workflow": *workflow, "job_name": *job, "class": *class,
	}, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	out.Pin.sanitise()
	what := "every job in " + out.Pin.Repo
	if out.Pin.Workflow != "" {
		what = out.Pin.JobName + " in " + out.Pin.Workflow + " of " + out.Pin.Repo
	}
	p.note("Pinned %s to %s.", what, out.Pin.Class)
	if out.Reclassified > 0 {
		p.note("%s already waiting moved to it.", plural(out.Reclassified, "job"))
	}
	return nil
}

func sizePinsDelete(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies size-pins delete <owner/repo> [--workflow <w> --job <j>]",
		"Take a pin away. The jobs it covered go back to the class their runs say, the ones already waiting included.")
	cf := registerClientFlags(fs, true)
	workflow := fs.String("workflow", "", "the workflow, with --job, for one job's pin")
	job := fs.String("job", "", "the job's name, with --workflow")
	if err := fs.parse(args); err != nil {
		return err
	}
	repo, err := fs.oneArg("a repository, written owner/name")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	q := url.Values{"repo": {repo}}
	if *workflow != "" {
		q.Set("workflow", *workflow)
	}
	if *job != "" {
		q.Set("job_name", *job)
	}
	var out struct {
		Reclassified int `json:"reclassified"`
	}
	if _, err := client.del(ctx, "/size-pins", q, &out); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Removed the pin on %s.", repo)
	if out.Reclassified > 0 {
		fmt.Fprintf(e.out, " %s already waiting went back to the class their runs say.", plural(out.Reclassified, "job"))
	}
	fmt.Fprintln(e.out)
	return nil
}

// ---------------------------------------------------------------------------
// auto-pools
// ---------------------------------------------------------------------------

// runAutoPools is `zoomies auto-pools`: whether size routing and automatic pools
// are on, what the controller keeps and why anything is not as expected.
func runAutoPools(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies auto-pools", "Show what the controller keeps for each size of host, and why a host is not in one.")
	cf := registerClientFlags(fs, true)
	fs.example("zoomies auto-pools", "zoomies auto-pools --output json")
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
	var out autoPoolsItem
	raw, err := client.get(ctx, "/auto-pools", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	p.keyValues([][2]string{
		{"size routing", out.SizeRouting},
		{"automatic pools", out.AutoPools},
		{"installation", dash(out.Installation)},
		{"default class", dash(out.DefaultClass)},
	})
	if out.SizeRouting == "off" && out.AutoPools == "off" {
		p.note("\nBoth are off, which is how every fleet starts. Set scheduler.auto_pools and scheduler.size_routing to shadow to see what they would do before they do it.")
		return nil
	}
	if out.Problem != "" {
		p.note("\n%s.", out.Problem)
	}
	if len(out.Classes) > 0 {
		rows := make([][]string, 0, len(out.Classes))
		for _, c := range out.Classes {
			hosts := "any larger"
			if c.HostMaxCPUs > 0 {
				hosts = fmt.Sprintf("up to %s CPUs, %s", strconv.FormatFloat(c.HostMaxCPUs, 'f', -1, 64), formatMemoryMB(c.HostMaxMemoryMB))
			}
			rows = append(rows, []string{c.Class, c.Label, hosts,
				fmt.Sprintf("%s CPUs, %s", strconv.FormatFloat(c.RunnerCPUs, 'f', -1, 64), formatMemoryMB(c.RunnerMemoryMB))})
		}
		fmt.Fprintln(p.out)
		p.table([]string{"class", "runs-on label", "a host in it has", "one runner is"}, rows)
	}
	if len(out.Pools) > 0 {
		rows := make([][]string, 0, len(out.Pools))
		for _, pool := range out.Pools {
			rows = append(rows, []string{pool.Name, dash(strings.Join(pool.Hosts, ", ")), plural(pool.Slots, "slot")})
		}
		fmt.Fprintln(p.out)
		p.table([]string{"pool", "hosts", "they give"}, rows)
	}
	for _, s := range out.Skipped {
		p.note("%s: %s", s.Host, plain(s.Message))
	}
	for _, f := range out.Findings {
		p.note("%s", plain(f.Message))
		if f.Fix != "" {
			p.note("  Fix: %s", plain(f.Fix))
		}
	}
	for _, c := range out.Pending {
		p.note("would %s %s: %s", c.Kind, c.Pool, plain(c.Cause))
	}
	return nil
}

// ---------------------------------------------------------------------------
// jobs advice
// ---------------------------------------------------------------------------

func jobsAdvice(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies jobs advice [--kind too_small|unguaranteed|too_large]",
		"What to change in the runs-on of the jobs whose measured runs call for something other than what they ask for.")
	cf := registerClientFlags(fs, true)
	page := registerPageFlags(fs, 20)
	kind := fs.String("kind", "", "only this kind: too_small, unguaranteed or too_large")
	fs.example("zoomies jobs advice", "zoomies jobs advice --kind too_small")
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
	page.apply(q)
	if *kind != "" {
		q.Set("kind", *kind)
	}
	var out struct {
		Items  []labelAdviceItem `json:"items"`
		Total  int               `json:"total"`
		Offset int               `json:"offset"`
	}
	raw, err := client.get(ctx, "/label-advice", q, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("Nothing to change. Advice needs size routing on or watching, and at least five measured runs of a job.")
		return nil
	}
	rows := make([][]string, 0, len(out.Items))
	for i := range out.Items {
		out.Items[i].sanitise()
		a := out.Items[i]
		asks := dash(a.Asked)
		rows = append(rows, []string{adviceKind(a.Kind), a.Repo, truncate(a.Workflow+" / "+a.JobName, 40), asks, a.Class, strconv.Itoa(a.Runs)})
	}
	p.table([]string{"problem", "repository", "job", "asks for", "needs", "runs"}, rows)
	p.footer(len(out.Items), out.Total, out.Offset)
	for _, a := range out.Items {
		fmt.Fprintln(p.out)
		fmt.Fprintf(p.out, "%s / %s / %s\n", a.Repo, a.Workflow, a.JobName)
		fmt.Fprintf(p.out, "  %s\n", plain(capitaliseFirst(a.Message)))
		fmt.Fprintf(p.out, "  %s %s\n", p.paint(colourYellow, "Fix:"), plain(a.Fix))
	}
	return nil
}

// adviceKind says a kind of advice in a word or two.
func adviceKind(kind string) string {
	switch kind {
	case "too_small":
		return "too small"
	case "unguaranteed":
		return "not guaranteed"
	case "too_large":
		return "too large"
	}
	return kind
}

func capitaliseFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// sizeRows says how a job was classed, where it was sent and which class of host
// took it. It is empty for a job nobody classed, which is every job while size
// routing is off, so the table is unchanged there.
func sizeRows(j jobItem) [][2]string {
	if j.SizeClass == "" {
		return nil
	}
	rows := [][2]string{{"size class", j.SizeClass + " (" + plain(j.SizeReason) + ")"}}
	switch {
	case j.RoutedClass == "":
		rows = append(rows, [2]string{"routed to", "nothing was sent anywhere: size routing is only watching"})
	case j.RoutedNote != "":
		rows = append(rows, [2]string{"routed to", j.RoutedClass + " (" + plain(j.RoutedNote) + ")"})
	case j.SizeBasis != "explicit":
		rows = append(rows, [2]string{"routed to", j.RoutedClass + ", best effort: GitHub decides which waiting job a runner takes"})
	default:
		rows = append(rows, [2]string{"routed to", j.RoutedClass})
	}
	if j.RanClass != "" {
		ran := j.RanClass
		if j.RanClass != j.SizeClass {
			ran += " (not the class it was put in)"
		}
		rows = append(rows, [2]string{"ran on", ran})
	}
	if j.ThrottledShare != nil && *j.ThrottledShare > 0 {
		rows = append(rows, [2]string{"held back by its CPU limit", fmt.Sprintf("%.0f%% of the time", *j.ThrottledShare*100)})
	}
	return rows
}

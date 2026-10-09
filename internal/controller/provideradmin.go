package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/provider"
	"github.com/eyupio/zoomies/internal/store"
)

// This file is the operator's half of the provider feature: the four questions
// a person asks that the reconciler never does -- what can this build rent
// from, are these answers sensible, does this credential actually work, and
// what is out there that no row accounts for.
//
// It lives here rather than in internal/api because every one of them needs a
// built provider, and building one needs the registry, the instance key and the
// operator's deadlines. The API is transport: it renders what these return and
// has no opinion about the fleet, which is why each of them answers in a view
// type rather than in provider's own vocabulary.

// ErrProviderKindUnknown is a provider row, or a draft, naming a kind this
// build has no driver for. It is a conflict rather than a validation failure
// for a stored row -- the row was legal when it was written -- and the message
// names the kind so an operator knows which build they need.
var ErrProviderKindUnknown = errors.New("this build has no driver for that provider kind")

// ErrDiscoveryUnsupported is a provider whose driver cannot list what a
// credential can see. The guided form asks the operator to type identifiers
// instead, so this is reported rather than hidden.
var ErrDiscoveryUnsupported = errors.New("this provider cannot list what its credential can see")

// ---------------------------------------------------------------------------
// The kinds this build ships
// ---------------------------------------------------------------------------

// ProviderKindView is one provider driver this build carries, with the
// questions its configuration form has to ask. The form, the CLI and
// docs/providers.md all render this one description, so a setting cannot
// appear in one of them and be missing from the others.
type ProviderKindView struct {
	Kind  store.ProviderKind `json:"kind"`
	Label string             `json:"label"`
	// Bootstrap says how the enrolment payload reaches a guest, which is the
	// one capability an operator has to care about before they buy anything:
	// "metadata" means the payload is readable by anyone who can read the
	// machine's metadata.
	Bootstrap        string `json:"bootstrap"`
	CanStartStop     bool   `json:"can_start_stop"`
	CanDiscover      bool   `json:"can_discover"`
	CanMarkOwnership bool   `json:"can_mark_ownership"`
	AsyncOperations  bool   `json:"async_operations"`
	CostUnit         string `json:"cost_unit,omitempty"`
	EndpointExample  string `json:"endpoint_example,omitempty"`
	// Where the three answers every kind is asked for are found, in the
	// provider's own console's terms.
	EndpointSource   string `json:"endpoint_source,omitempty"`
	CredentialSource string `json:"credential_source,omitempty"`
	CASource         string `json:"ca_source,omitempty"`
	// Guide is what has to exist before this kind can rent anything, in the
	// order it is done, so the wizard can say it before its first question.
	Guide    []ProviderGuideStepView `json:"guide"`
	Settings []ProviderSettingView   `json:"settings"`
}

// ProviderGuideStepView is one thing to do before a provider of a kind works.
type ProviderGuideStepView struct {
	Title   string `json:"title"`
	Detail  string `json:"detail,omitempty"`
	Command string `json:"command,omitempty"`
}

// ProviderSettingView is one answer a provider needs.
type ProviderSettingView struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Kind     string `json:"kind"`
	Required bool   `json:"required"`
	Advanced bool   `json:"advanced"`
	Help     string `json:"help,omitempty"`
	// Consequence says what choosing this costs, in the voice the pool form
	// already uses. A list of identifiers with no consequences is a list
	// somebody guesses at.
	Consequence string `json:"consequence,omitempty"`
	Discovers   string `json:"discovers,omitempty"`
	Default     string `json:"default,omitempty"`
	// Source says where to find the answer when discovery cannot offer it.
	Source string `json:"source,omitempty"`
}

// ProviderKinds is every driver this build can speak, in the registry's stable
// order.
func (c *Controller) ProviderKinds() []ProviderKindView {
	kinds := c.providers.Kinds()
	out := make([]ProviderKindView, 0, len(kinds))
	for _, k := range kinds {
		f, err := c.providers.Get(k)
		if err != nil {
			continue
		}
		caps := f.Describe()
		v := ProviderKindView{
			Kind:             k,
			Label:            caps.Label,
			Bootstrap:        string(caps.Bootstrap),
			CanStartStop:     caps.CanStartStop,
			CanDiscover:      caps.CanDiscover,
			CanMarkOwnership: caps.CanMarkOwnership,
			AsyncOperations:  caps.AsyncOperations,
			CostUnit:         caps.CostUnit,
			EndpointExample:  caps.EndpointExample,
			EndpointSource:   caps.EndpointSource,
			CredentialSource: caps.CredentialSource,
			CASource:         caps.CASource,
			Guide:            make([]ProviderGuideStepView, 0, len(caps.Guide)),
			Settings:         make([]ProviderSettingView, 0, len(f.Settings())),
		}
		for _, step := range caps.Guide {
			v.Guide = append(v.Guide, ProviderGuideStepView{Title: step.Title, Detail: step.Detail, Command: step.Command})
		}
		for _, spec := range f.Settings() {
			v.Settings = append(v.Settings, ProviderSettingView{
				Key:         spec.Key,
				Label:       spec.Label,
				Kind:        string(spec.Kind),
				Required:    spec.Required,
				Advanced:    spec.Advanced,
				Help:        spec.Help,
				Consequence: spec.Consequence,
				Discovers:   spec.Discovers,
				Default:     spec.Default,
				Source:      spec.Source,
			})
		}
		out = append(out, v)
	}
	return out
}

// ProvidersAvailable reports whether this deployment can rent machines at all:
// a driver to rent from, and an operator who has turned the machine loop on.
// The page that would otherwise offer to buy machines nothing would ever create
// asks this first.
func (c *Controller) ProvidersAvailable() bool {
	return len(c.providers.Kinds()) > 0 && c.cfg().Provider.Enabled
}

// ValidateProviderSettings runs a driver's own offline check over a draft's
// answers. It dials nothing and needs no credential, which is what lets the
// wizard run it as somebody types; CheckProvider is the call that talks to the
// hypervisor.
func (c *Controller) ValidateProviderSettings(kind store.ProviderKind, settings map[string]string) ([]config.Finding, error) {
	f, err := c.providers.Get(kind)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrProviderKindUnknown, kind)
	}
	return f.Validate(settings), nil
}

// ---------------------------------------------------------------------------
// The live preflight
// ---------------------------------------------------------------------------

// ProviderCheckView is a preflight result: whether the hypervisor answered,
// what it said it is, and everything an operator has to fix before a machine
// will ever be created. The findings are config.Finding so that "what is wrong
// with this provider" reads exactly like "what is wrong with this
// configuration", in the wizard, in the problems drawer and in the CLI.
type ProviderCheckView struct {
	ProviderID string           `json:"provider_id"`
	OK         bool             `json:"ok"`
	Reachable  bool             `json:"reachable"`
	Version    string           `json:"version,omitempty"`
	Findings   []config.Finding `json:"findings"`
	CheckedAt  time.Time        `json:"checked_at"`
}

// CheckProvider asks a provider what it would refuse, changing nothing.
//
// The result is recorded on the row as well as returned, because the page, the
// problems drawer and the reconciler's own "this template has not been verified
// since the configuration changed" warning all read it from there -- a check an
// operator ran in a terminal should quiet the warning the UI is showing.
func (c *Controller) CheckProvider(ctx context.Context, row *store.Provider) (ProviderCheckView, error) {
	pr, err := c.providerFor(ctx, row)
	if err != nil {
		return ProviderCheckView{}, providerBuildError(row, err)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.stepTimeout(pr))
	defer cancel()
	report := pr.p.Preflight(callCtx)

	now := c.Now()
	out := ProviderCheckView{
		ProviderID: row.ID,
		OK:         report.OK(),
		Reachable:  report.Reachable,
		Version:    report.Version,
		Findings:   report.Findings,
		CheckedAt:  now,
	}
	if out.Findings == nil {
		out.Findings = []config.Finding{}
	}
	// The recorded error is the worst thing the check found, in one sentence,
	// because that is what a card has room for; the findings are the whole of
	// it and are returned to the caller who asked.
	//
	// The whole result is kept too, because the sentence is only a title and
	// the detail that says what to do ("it offers vmbr1") would otherwise
	// exist for as long as one browser tab did.
	if err := c.st.SetProviderChecked(ctx, row.ID, now, checkComplaint(report), encodeCheckReport(out)); err != nil {
		c.log.Warn("could not record a provider check", "provider", row.ID, "error", err)
	}
	if fresh, err := c.st.GetProvider(ctx, row.ID); err == nil {
		c.PublishProvider(ctx, fresh)
	}
	return out, nil
}

// storedCheck is what is kept of a preflight. The time and the provider are
// the row's own (last_check_at, id), so they are not stored a second time to
// disagree with it.
type storedCheck struct {
	OK        bool             `json:"ok"`
	Reachable bool             `json:"reachable"`
	Version   string           `json:"version,omitempty"`
	Findings  []config.Finding `json:"findings"`
}

func encodeCheckReport(v ProviderCheckView) string {
	b, err := json.Marshal(storedCheck{OK: v.OK, Reachable: v.Reachable, Version: v.Version, Findings: v.Findings})
	if err != nil {
		return ""
	}
	return string(b)
}

// decodeCheckReport reads back what encodeCheckReport kept, or nil when the
// provider has never been checked or the stored text is not a report. A row
// from before the report was kept reads as the second, which the page shows as
// its one sentence, as it always did.
func decodeCheckReport(p *store.Provider) *ProviderCheckView {
	if p.LastCheckReport == "" || p.LastCheckAt == nil {
		return nil
	}
	var s storedCheck
	if err := json.Unmarshal([]byte(p.LastCheckReport), &s); err != nil {
		return nil
	}
	if s.Findings == nil {
		s.Findings = []config.Finding{}
	}
	return &ProviderCheckView{ProviderID: p.ID, OK: s.OK, Reachable: s.Reachable,
		Version: s.Version, Findings: s.Findings, CheckedAt: *p.LastCheckAt}
}

// checkComplaint is the one sentence a preflight leaves on the row. A provider
// that could not be reached has one problem rather than eleven, so that is what
// it says; otherwise it is the first error, and warnings alone leave the row
// clean because they never stopped anything.
func checkComplaint(r provider.Report) string {
	if !r.Reachable {
		return "could not be reached"
	}
	for _, f := range r.Findings {
		if f.Severity == config.SeverityError {
			return f.Title
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Discovery
// ---------------------------------------------------------------------------

// ProviderDiscoveryView is what a credential can actually see. A provider with
// no such concept leaves a list empty rather than inventing one.
type ProviderDiscoveryView struct {
	Nodes     []ProviderChoiceView `json:"nodes"`
	Storages  []ProviderChoiceView `json:"storages"`
	Bridges   []ProviderChoiceView `json:"bridges"`
	Templates []ProviderChoiceView `json:"templates"`
	// Unavailable is why the lists are empty when the provider could not be
	// asked: the address did not answer, the credential was refused. A draft
	// in a wizard is expected to be wrong at first, and a sentence beside the
	// form is what corrects it; the form asks for identifiers meanwhile.
	Unavailable string `json:"unavailable,omitempty"`
}

// ProviderChoiceView is one option in a guided form.
type ProviderChoiceView struct {
	Value       string `json:"value"`
	Label       string `json:"label,omitempty"`
	Consequence string `json:"consequence,omitempty"`
}

// DiscoverProvider asks a provider what this credential can see, so the form
// offers a menu rather than asking somebody to go and look an identifier up.
func (c *Controller) DiscoverProvider(ctx context.Context, row *store.Provider) (ProviderDiscoveryView, error) {
	pr, err := c.providerFor(ctx, row)
	if err != nil {
		return ProviderDiscoveryView{}, providerBuildError(row, err)
	}
	return c.discover(ctx, pr)
}

// DiscoverDraft asks a provider that has not been saved what its credential
// can see, so the wizard can offer the nodes, storages, bridges and templates
// as a menu before there is a row to save them on.
//
// The secrets arrive in plaintext because there is no row to unseal them
// from, and they go nowhere: the client is built for this one call and
// dropped, and a private connection opened for it is closed with it. Nothing
// is cached, because a draft has no identity to cache under and its
// credential may be about to be corrected.
func (c *Controller) DiscoverDraft(ctx context.Context, row *store.Provider, credential, address string) (ProviderDiscoveryView, error) {
	if address != "" && !c.cfg().Server.TailcatEnabled {
		return ProviderDiscoveryView{}, errPrivateConnectionsDisabled(row)
	}
	f, err := c.providers.Get(row.Kind)
	if err != nil {
		return ProviderDiscoveryView{}, providerBuildError(row, err)
	}
	if dd, ok := f.(provider.DraftDiscoverer); ok {
		// The address and the credential are enough: the placement answers
		// a built provider would insist on are the ones the menu is for.
		cfg, tunnel, err := c.providerConfig(row, credential, address)
		if err != nil {
			return ProviderDiscoveryView{}, err
		}
		if tunnel != nil {
			defer tunnel.Close()
		}
		callCtx, cancel := context.WithTimeout(ctx, c.providerDeadlines().Call)
		defer cancel()
		got, err := dd.DiscoverDraft(callCtx, cfg)
		if err != nil {
			return ProviderDiscoveryView{}, err
		}
		return discoveryView(got), nil
	}
	p, tunnel, err := c.buildProvider(ctx, row, credential, address)
	if err != nil {
		return ProviderDiscoveryView{}, providerBuildError(row, err)
	}
	if tunnel != nil {
		defer tunnel.Close()
	}
	return c.discover(ctx, &machineProvider{row: row, p: p})
}

func (c *Controller) discover(ctx context.Context, pr *machineProvider) (ProviderDiscoveryView, error) {
	row := pr.row
	d, ok := pr.p.(provider.Discoverer)
	if !ok {
		return ProviderDiscoveryView{}, fmt.Errorf("%w: %s is a %s provider", ErrDiscoveryUnsupported, row.Name, row.Kind)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.stepTimeout(pr))
	defer cancel()
	got, err := d.Discover(callCtx)
	if err != nil {
		return ProviderDiscoveryView{}, err
	}
	return discoveryView(got), nil
}

func discoveryView(got provider.Discovery) ProviderDiscoveryView {
	return ProviderDiscoveryView{
		Nodes:     choiceViews(got.Nodes),
		Storages:  choiceViews(got.Storages),
		Bridges:   choiceViews(got.Bridges),
		Templates: choiceViews(got.Templates),
	}
}

func choiceViews(in []provider.Choice) []ProviderChoiceView {
	out := make([]ProviderChoiceView, 0, len(in))
	for _, c := range in {
		out = append(out, ProviderChoiceView{Value: c.Value, Label: c.Label, Consequence: c.Consequence})
	}
	return out
}

// providerBuildError explains a provider that could not be built at all, which
// is a different failure from one that answered badly: the credential would not
// unseal, or this build has no driver for the row's kind.
func providerBuildError(row *store.Provider, err error) error {
	if errors.Is(err, provider.ErrUnsupported) {
		return fmt.Errorf("%w: %s is a %s provider", ErrProviderKindUnknown, row.Name, row.Kind)
	}
	return err
}

// ---------------------------------------------------------------------------
// The orphan review
// ---------------------------------------------------------------------------

// OrphanReportView is the review page in one answer: the three ways a row and a
// resource can fail to agree. Nothing here is ever acted on automatically --
// each section is a list of things for a person to look at, and the only
// destructive act on the page asks for a name to be typed.
type OrphanReportView struct {
	ProviderID   string     `json:"provider_id"`
	ProviderName string     `json:"provider_name"`
	LastSweepAt  *time.Time `json:"last_sweep_at,omitempty"`
	// Untracked is a resource wearing this fleet's naming grammar that no row
	// accounts for. It is never deleted from here: the row is the only record
	// of what was rented, and a resource with no row may be another fleet's.
	Untracked []OrphanView `json:"untracked"`
	// NoResource is a row that believes it holds nothing, which is a row an
	// operator can release without anything being lost.
	NoResource []MachineView `json:"no_resource"`
	// Unverified is a machine whose ownership nothing has confirmed, including
	// every quarantined one. Until a person decides, nothing will touch them.
	Unverified []MachineView `json:"unverified"`
}

// OrphanView is one untracked resource, as the last sweep saw it.
type OrphanView struct {
	Name string `json:"name"`
	// Note is what an operator should do about it, which for an untracked
	// resource is always "look at it yourself": Zoomies will not delete a
	// machine it has no record of creating.
	Note string `json:"note"`
}

// OrphanReport gathers a provider's three disagreements. The untracked half
// comes from the last ownership sweep rather than from a call made now, because
// the page is read far more often than the hypervisor should be asked -- the
// sweep's own timestamp is returned so a stale answer says so.
func (c *Controller) OrphanReport(ctx context.Context, row *store.Provider) (OrphanReportView, error) {
	machines, err := c.st.ListMachinesForProvider(ctx, row.ID)
	if err != nil {
		return OrphanReportView{}, fmt.Errorf("listing the provider's machines: %w", err)
	}
	renderer, err := c.MachineRenderer(ctx)
	if err != nil {
		return OrphanReportView{}, err
	}
	out := OrphanReportView{
		ProviderID:   row.ID,
		ProviderName: row.Name,
		LastSweepAt:  row.LastSweepAt,
		Untracked:    []OrphanView{},
		NoResource:   []MachineView{},
		Unverified:   []MachineView{},
	}
	names := c.ProviderOrphans()[row.ID]
	slices.Sort(names)
	for _, name := range names {
		out.Untracked = append(out.Untracked, OrphanView{
			Name: name,
			Note: "this resource wears the naming Zoomies gives its machines but no row accounts for it. " +
				"Zoomies will never delete it; look at it in " + row.Name + " and remove it there if it is yours and idle.",
		})
	}
	for _, m := range machines {
		switch {
		case m.State == store.MachineDeleted:
			// History. The resource is confirmed gone and the row goes with
			// the next prune.
		case !m.Owns():
			out.NoResource = append(out.NoResource, renderer.View(m))
		case m.State == store.MachineQuarantined || m.OwnershipError != "" || m.OwnershipVerifiedAt == nil:
			out.Unverified = append(out.Unverified, renderer.View(m))
		}
	}
	return out, nil
}

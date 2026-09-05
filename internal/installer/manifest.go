package installer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/store"
)

// GitHubPlan is the GitHub half of the plan: which account's runners this
// fleet manages, and the App credentials that let it manage them.
type GitHubPlan struct {
	// APIBaseURL is https://api.github.com, or a GitHub Enterprise Server
	// endpoint such as https://ghes.example.com/api/v3.
	APIBaseURL string
	// Target is an organisation login, or "owner/repo" for a single
	// repository.
	Target     string
	TargetType store.TargetType
	// AppName is the App's display name, which must be unique across all of
	// GitHub.
	AppName        string
	AppID          int64
	InstallationID int64
	AppSlug        string
	HTMLURL        string
	// PrivateKeyFile is where an unattended run reads the .pem from.
	PrivateKeyFile string
	// Skip records a deliberate decision to connect GitHub later in the UI,
	// which is a reasonable thing to want when the browser is elsewhere.
	Skip bool
}

// manifestWait is how long the installer waits for GitHub to come back. Five
// minutes is enough to create an App on a phone; longer and an operator who
// has walked away is just watching a countdown.
const manifestWait = 5 * time.Minute

// stepGitHubApp connects the fleet to GitHub.
//
// This is the part that makes setup feel like a product rather than a
// checklist: the operator's browser is sent to a pre-filled App manifest with
// exactly the permissions Zoomies needs and the webhook URL already correct,
// and the credentials come back here on their own. Everything about it is
// still recoverable by hand -- the URL is always printed, and the code can be
// pasted -- because half of these hosts are headless.
func (i *Installer) stepGitHubApp(ctx context.Context, st *store.Store, key *cryptox.Key, p *Plan) error {
	i.ui.step("GitHub App")

	if p.GitHub.Skip {
		i.ui.note("skipped. Connect GitHub later on the Installations page; until then no runners can be created.")
		return nil
	}
	if !i.interactive {
		return i.appFromAnswers(ctx, st, key, p)
	}
	return i.appFromManifest(ctx, st, key, p)
}

// appFromAnswers takes credentials that already exist, which is the only thing
// an unattended run can do: creating an App is a browser handshake with a human
// in it.
func (i *Installer) appFromAnswers(ctx context.Context, st *store.Store, key *cryptox.Key, p *Plan) error {
	if i.answers == nil {
		return errors.New("installer: an unattended run cannot create a GitHub App, because creating one is a browser handshake; " +
			"create the App once by hand and give this run github.app_id, github.installation_id, github.private_key_file and " +
			"github.webhook_secret in the answer file, or set github.skip to connect it later in the UI")
	}
	pem, err := i.answers.PrivateKey()
	if err != nil {
		return err
	}
	secret, err := i.answers.WebhookSecret()
	if err != nil {
		return err
	}
	if p.GitHub.AppID == 0 || p.GitHub.InstallationID == 0 || pem == "" {
		return i.answers.Validate(p.Mode)
	}

	inst, err := storeInstallation(ctx, st, key, installationInput{
		AppID:          p.GitHub.AppID,
		InstallationID: p.GitHub.InstallationID,
		Target:         p.GitHub.Target,
		TargetType:     p.GitHub.TargetType,
		APIBaseURL:     p.GitHub.APIBaseURL,
		PrivateKeyPEM:  pem,
		WebhookSecret:  secret,
	})
	if err != nil {
		return err
	}
	i.wrote(fmt.Sprintf("recorded installation %s for %s", inst.ID, inst.Target))
	i.verifyInstallation(ctx, inst, pem)
	return nil
}

// appFromManifest runs the browser handshake.
func (i *Installer) appFromManifest(ctx context.Context, st *store.Store, key *cryptox.Key, p *Plan) error {
	if p.ExternalURL == "" {
		return errors.New("installer: the GitHub App needs the external URL, because it is what the webhook URL is built from")
	}

	create := true
	if err := i.confirm(ctx, "Create the GitHub App now?",
		"Your browser opens a pre-filled form on GitHub with exactly the permissions Zoomies needs. "+
			"Answering no finishes setup and leaves GitHub to be connected on the Installations page; no runners can be created until it is.",
		&create); err != nil {
		return err
	}
	if !create {
		p.GitHub.Skip = true
		i.ui.note("skipped. Connect GitHub later on the Installations page.")
		return nil
	}

	if err := i.askGitHubTarget(ctx, p); err != nil {
		return err
	}

	// A webhook URL is fixed when the App is created, and GitHub cannot deliver
	// to loopback. Loopback is also the default listener, so the flagship
	// handshake would otherwise cheerfully create a real App on the operator's
	// organisation whose webhook can never fire -- discovered weeks later as
	// "scaling is slow", and fixable only by editing the App on GitHub.
	if err := i.checkWebhookReachable(ctx, p); err != nil {
		return err
	}
	if p.GitHub.Skip {
		return nil
	}

	cfg := p.Config()
	webhookURL := cfg.WebhookURL()
	org := ""
	if p.GitHub.TargetType == store.TargetOrg {
		org = p.GitHub.Target
	}

	srv, err := newCallbackServer()
	if err != nil {
		return err
	}
	defer srv.Close()

	manifest, err := github.Manifest(github.ManifestOptions{
		Name:         p.GitHub.AppName,
		URL:          p.ExternalURL,
		WebhookURL:   webhookURL,
		Organization: org,
		// The durable address: the route the controller serves as soon as it
		// starts, a few steps below in this same run. The temporary loopback
		// listener catches only this handshake, and putting it here instead
		// meant every future "install on another repository" ended on a
		// refused connection to a port that stopped existing years earlier.
		SetupURL:    strings.TrimRight(p.ExternalURL, "/") + "/settings/github/setup",
		RedirectURL: srv.CallbackURL(),
	})
	if err != nil {
		return err
	}
	srv.Configure(manifest, github.ManifestURL(p.GitHub.APIBaseURL, org))
	srv.Start()

	i.ui.note("webhook URL   " + webhookURL)
	i.ui.note("permissions   actions:read, metadata:read, " + runnerPermission(p.GitHub.TargetType) + ":write, event workflow_job")
	// The migration wizard's three are named separately because they are the
	// ones an operator may not expect a runner controller to ask for, and a
	// permission list that quietly grew is worse than one that says why.
	i.ui.note("              contents:write, pull_requests:write, workflows:write -- for the")
	i.ui.note("              migration wizard, which rewrites runs-on lines by pull request")
	i.ui.blank()
	i.ui.note("Open this to create the App:")
	i.ui.note("  " + srv.URL())
	if err := openBrowser(ctx, srv.URL()); err != nil {
		i.ui.note("(this host could not open a browser itself: " + err.Error() + ")")
	}
	i.ui.note("If your browser is on another machine, open the URL there -- it is a loopback")
	i.ui.note("address, so use an SSH tunnel, or paste the ?code= value from GitHub's redirect below.")
	i.ui.blank()

	paste := lineReader(ctx, i.in)
	res, err := srv.WaitFor(ctx, manifestWait, paste, i.countdown("waiting for GitHub"))
	i.clearLine()
	if err != nil {
		return err
	}

	creds, err := github.ExchangeManifestCode(ctx, p.GitHub.APIBaseURL, res.Code)
	if err != nil {
		return err
	}
	p.GitHub.AppID = creds.AppID
	p.GitHub.AppSlug = creds.Slug
	p.GitHub.HTMLURL = creds.HTMLURL
	// GitHub's manifest schema has no place for a secret of our choosing, so
	// the one it generated is the only one that verifies its deliveries.
	secret := creds.WebhookSecret
	if secret == "" {
		i.ui.warn("GitHub returned no webhook secret for this App, so deliveries cannot be verified yet; " +
			"set one on the App's settings page and paste it into the Installations page.")
	}
	i.wrote(fmt.Sprintf("created the GitHub App %q (id %d) on %s", creds.Name, creds.AppID, p.GitHub.Target))

	// Seal the credentials now, before the operator is asked to go and do
	// something on GitHub.
	//
	// GitHub hands the private key over exactly once. Holding it in a local
	// variable across an install page, a five-minute countdown and possibly a
	// typed installation ID meant that a ctrl-c anywhere in that window --
	// entirely natural when the browser is on another machine -- left a real
	// App on the operator's organisation whose key existed nowhere. Recording
	// it with installation 0 costs nothing: storeInstallation updates the row
	// for this target rather than duplicating it, so the call below finishes
	// the same record.
	if _, err := storeInstallation(ctx, st, key, installationInput{
		AppID:         creds.AppID,
		Target:        p.GitHub.Target,
		TargetType:    p.GitHub.TargetType,
		APIBaseURL:    p.GitHub.APIBaseURL,
		AppSlug:       creds.Slug,
		PrivateKeyPEM: creds.PEM,
		WebhookSecret: secret,
	}); err != nil {
		return err
	}
	i.wrote("sealed the App's private key with this instance's encryption key, before anything else can go wrong")

	// The App exists but is installed nowhere yet, so it can do nothing. The
	// operator has to say yes on GitHub's own page.
	installURL := github.InstallURL(creds.HTMLURL)
	i.ui.blank()
	i.ui.note("Now install it on " + p.GitHub.Target + ":")
	i.ui.note("  " + installURL)
	if err := openBrowser(ctx, installURL); err != nil {
		i.ui.note("(open that URL yourself: this host has no browser)")
	}
	i.ui.blank()

	res, err = srv.WaitFor(ctx, manifestWait, paste, i.countdown("waiting for the installation"))
	i.clearLine()
	if err != nil || res.InstallationID == 0 {
		// Not fatal: the App and its key are recorded either way, and the
		// installation ID is a number the operator can read off the URL.
		if err != nil {
			i.ui.warn("did not see the installation come back: " + err.Error())
		}
		id := ""
		if err := i.input(ctx, "Installation ID",
			"It is the number at the end of the URL GitHub sent you to after installing the App, e.g. .../installations/12345678.",
			"", &id, func(s string) error {
				if _, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64); err != nil {
					return errors.New("enter the numeric installation ID from the URL")
				}
				return nil
			}); err != nil {
			return err
		}
		p.GitHub.InstallationID, _ = strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	} else {
		p.GitHub.InstallationID = res.InstallationID
	}
	i.ui.ok(fmt.Sprintf("installed on %s (installation %d)", p.GitHub.Target, p.GitHub.InstallationID))

	inst, err := storeInstallation(ctx, st, key, installationInput{
		AppID:          creds.AppID,
		InstallationID: p.GitHub.InstallationID,
		Target:         p.GitHub.Target,
		TargetType:     p.GitHub.TargetType,
		APIBaseURL:     p.GitHub.APIBaseURL,
		AppSlug:        creds.Slug,
		PrivateKeyPEM:  creds.PEM,
		WebhookSecret:  secret,
	})
	if err != nil {
		return err
	}
	i.ui.ok("recorded installation " + strconv.FormatInt(p.GitHub.InstallationID, 10) + " against the sealed key")
	i.verifyInstallation(ctx, inst, creds.PEM)
	return nil
}

// checkWebhookReachable stops an App being created with a webhook URL GitHub
// cannot reach. It offers the three real answers rather than a warning that
// scrolls past: fix the URL, accept the poller, or connect GitHub later.
func (i *Installer) checkWebhookReachable(ctx context.Context, p *Plan) error {
	host := p.ExternalURL
	if u, err := url.Parse(p.ExternalURL); err == nil {
		host = u.Hostname()
	}
	if host != "localhost" && !isLoopbackHost(host) {
		return nil
	}

	i.ui.warn("GitHub cannot deliver webhooks to " + p.ExternalURL + ".")
	i.ui.note("an App created now would carry that address for ever, and its webhook would never fire;")
	i.ui.note("scaling would fall back to the poller, which reacts in tens of seconds rather than instantly.")

	if !i.interactive {
		return nil
	}

	const (
		optFix   = "fix"
		optAny   = "anyway"
		optLater = "later"
	)
	choice := optFix
	if err := i.selectOne(ctx, "What should this be reached at?",
		"The webhook URL is baked into the App when GitHub creates it.",
		[]huh.Option[string]{
			huh.NewOption("Enter the address GitHub will use (default)", optFix),
			huh.NewOption("Create it anyway -- I will fix the App's webhook URL on GitHub", optAny),
			huh.NewOption("Skip GitHub for now -- connect it later in the browser", optLater),
		}, &choice); err != nil {
		return err
	}
	switch choice {
	case optLater:
		p.GitHub.Skip = true
		i.ui.note("skipped. Connect GitHub on the Installations page.")
		return nil
	case optAny:
		return nil
	}

	updated, err := i.askExternalURL(ctx, *p)
	if err != nil {
		return err
	}
	*p = updated
	return nil
}

// askGitHubTarget settles which account's runners this fleet manages and where
// its API lives.
func (i *Installer) askGitHubTarget(ctx context.Context, p *Plan) error {
	kind := string(store.TargetOrg)
	if p.GitHub.TargetType != "" {
		kind = string(p.GitHub.TargetType)
	}
	if err := i.selectOne(ctx, "Whose runners will this fleet manage?",
		"An organisation App manages runners for every repository in the org, which is almost always what you want. "+
			"A repository App is narrower and needs a different permission.",
		[]huh.Option[string]{
			huh.NewOption("An organisation (default)", string(store.TargetOrg)),
			huh.NewOption("A single repository", string(store.TargetRepo)),
		}, &kind); err != nil {
		return err
	}
	p.GitHub.TargetType = store.TargetType(kind)

	title, desc := "Organisation login", "The name as it appears in the URL: github.com/<this>."
	if p.GitHub.TargetType == store.TargetRepo {
		title, desc = "Repository", "In owner/name form, e.g. acme/widgets."
	}
	target := p.GitHub.Target
	if err := i.input(ctx, title, desc, target, &target, func(s string) error {
		s = strings.TrimSpace(s)
		if s == "" {
			return errors.New("a name is required")
		}
		if p.GitHub.TargetType == store.TargetRepo && !strings.Contains(s, "/") {
			return errors.New("use owner/name for a repository")
		}
		if p.GitHub.TargetType == store.TargetOrg && strings.Contains(s, "/") {
			return errors.New("an organisation login has no slash in it")
		}
		return nil
	}); err != nil {
		return err
	}
	p.GitHub.Target = strings.TrimSpace(target)

	api := p.GitHub.APIBaseURL
	if api == "" {
		api = "https://api.github.com"
	}
	if err := i.input(ctx, "GitHub API base URL",
		"Leave this for github.com. For Enterprise Server it is https://your-host/api/v3.",
		api, &api, func(s string) error {
			if _, err := github.NormalizeAPIBaseURL(s); err != nil {
				return err
			}
			return nil
		}); err != nil {
		return err
	}
	p.GitHub.APIBaseURL = strings.TrimSpace(api)

	if p.GitHub.AppName == "" {
		p.GitHub.AppName = defaultAppName(p.GitHub.Target)
	}
	name := p.GitHub.AppName
	if err := i.input(ctx, "App name",
		"It has to be unique across all of GitHub, and it is what your organisation's members will see next to the runners.",
		name, &name, func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("a name is required")
			}
			if len([]rune(s)) > 34 {
				return errors.New("GitHub allows at most 34 characters")
			}
			return nil
		}); err != nil {
		return err
	}
	p.GitHub.AppName = strings.TrimSpace(name)
	return nil
}

// defaultAppName suggests a name that identifies this controller and fits
// inside GitHub's 34-character limit.
func defaultAppName(target string) string {
	slug := strings.ToLower(target)
	slug = strings.ReplaceAll(slug, "/", "-")
	var b strings.Builder
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := "zoomies-" + strings.Trim(b.String(), "-")
	if len(name) > 34 {
		name = strings.TrimRight(name[:34], "-")
	}
	if name == "zoomies-" || name == "" {
		name = "zoomies"
	}
	return name
}

func runnerPermission(kind store.TargetType) string {
	if kind == store.TargetRepo {
		return "administration"
	}
	return "organization_self_hosted_runners"
}

// installationInput is what storeInstallation needs. The private key and the
// webhook secret are passed as plaintext exactly once, sealed, and dropped.
type installationInput struct {
	AppID          int64
	InstallationID int64
	Target         string
	TargetType     store.TargetType
	APIBaseURL     string
	AppSlug        string
	PrivateKeyPEM  string
	WebhookSecret  string
}

// storeInstallation seals the App's secrets with the instance key and records
// the installation, updating an existing row for the same target rather than
// creating a duplicate, so that re-running setup is safe.
func storeInstallation(ctx context.Context, st *store.Store, key *cryptox.Key, in installationInput) (*store.Installation, error) {
	if key == nil {
		return nil, errors.New("installer: no encryption key, so the App's private key cannot be sealed")
	}
	if in.TargetType == "" {
		in.TargetType = targetTypeFor(in.Target, "")
	}
	if in.APIBaseURL == "" {
		in.APIBaseURL = "https://api.github.com"
	}
	sealedKey, err := key.SealString(in.PrivateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("installer: sealing the App's private key: %w", err)
	}
	sealedSecret, err := key.SealString(in.WebhookSecret)
	if err != nil {
		return nil, fmt.Errorf("installer: sealing the webhook secret: %w", err)
	}

	existing, err := st.ListInstallations(ctx)
	if err != nil {
		return nil, fmt.Errorf("installer: reading existing installations: %w", err)
	}
	for _, e := range existing {
		if e.Target == in.Target && e.TargetType == in.TargetType {
			e.AppID = in.AppID
			e.InstallationID = in.InstallationID
			e.APIBaseURL = in.APIBaseURL
			e.AppSlug = in.AppSlug
			e.PrivateKeyEnc = sealedKey
			e.WebhookSecretEnc = sealedSecret
			e.LastError = ""
			if err := st.UpdateInstallation(ctx, e); err != nil {
				return nil, fmt.Errorf("installer: updating the installation for %s: %w", in.Target, err)
			}
			return e, nil
		}
	}

	inst := &store.Installation{
		AppID:            in.AppID,
		InstallationID:   in.InstallationID,
		Target:           in.Target,
		TargetType:       in.TargetType,
		APIBaseURL:       in.APIBaseURL,
		AppSlug:          in.AppSlug,
		PrivateKeyEnc:    sealedKey,
		WebhookSecretEnc: sealedSecret,
	}
	if err := st.CreateInstallation(ctx, inst); err != nil {
		return nil, fmt.Errorf("installer: recording the installation for %s: %w", in.Target, err)
	}
	return inst, nil
}

// verifyInstallation proves the credentials work before setup claims success,
// and reports what the App was actually granted. An App created by hand with
// the wrong permissions fails much later, in a way that looks like a Zoomies
// bug rather than a setup mistake.
func (i *Installer) verifyInstallation(ctx context.Context, inst *store.Installation, pem string) {
	client, err := github.NewAppFactory(nil).For(ctx, inst, []byte(pem))
	if err != nil {
		i.ui.warn("could not build a GitHub client from these credentials: " + err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	info, err := client.Probe(ctx)
	if err != nil {
		i.ui.warn("GitHub rejected these credentials: " + err.Error())
		i.ui.note("setup will finish; fix it on the Installations page and the fleet will start scaling.")
		return
	}
	i.ui.ok(fmt.Sprintf("verified against GitHub as %s", info.Name))

	perms := make([]string, 0, len(info.Permissions))
	for name, level := range info.Permissions {
		perms = append(perms, name+":"+level)
	}
	slices.Sort(perms)
	if len(perms) > 0 {
		i.ui.note("granted  " + strings.Join(perms, ", "))
	}
	if len(info.Events) > 0 {
		i.ui.note("events   " + strings.Join(info.Events, ", "))
	}
	for _, missing := range info.MissingRequirements(inst.TargetType) {
		i.ui.warn("missing: " + missing)
	}
}

// ---------------------------------------------------------------------------
// The callback server
// ---------------------------------------------------------------------------

// callbackResult is what came back from GitHub: a manifest code after the App
// was created, or an installation ID after it was installed.
type callbackResult struct {
	Code           string
	InstallationID int64
}

// callbackServer is the temporary local listener that carries the App manifest
// handshake.
//
// It serves two pages. The first auto-POSTs the manifest to GitHub, because a
// manifest is a form submission and there is no URL that carries one. The
// second is the redirect target GitHub returns to, which is where the code and
// later the installation ID arrive. It binds loopback on a free port and lives
// only as long as the handshake.
type callbackServer struct {
	ln       net.Listener
	srv      *http.Server
	state    string
	manifest []byte
	postURL  string

	results chan callbackResult
	// stateErrs carries a rejected callback out to the waiter, so that a
	// mismatched state is reported rather than looking like a timeout.
	stateErrs chan error
	closeOnce sync.Once
}

// newCallbackServer binds a free loopback port. Loopback because nothing off
// this host has any business in this handshake, and a free port because 8080
// is very often the controller's own.
func newCallbackServer() (*callbackServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("installer: could not open a local port for the GitHub handshake: %w", err)
	}
	return &callbackServer{
		ln:        ln,
		state:     store.NewSecret(16),
		results:   make(chan callbackResult, 4),
		stateErrs: make(chan error, 4),
	}, nil
}

// Configure sets what the start page posts and where.
func (c *callbackServer) Configure(manifest []byte, postURL string) {
	c.manifest = manifest
	c.postURL = postURL
}

// URL is the page the operator opens.
func (c *callbackServer) URL() string {
	return "http://" + c.ln.Addr().String() + "/"
}

// CallbackURL is the redirect target GitHub is told to come back to.
func (c *callbackServer) CallbackURL() string {
	return "http://" + c.ln.Addr().String() + "/callback"
}

// State is the anti-forgery value tying a callback to this setup session.
func (c *callbackServer) State() string { return c.state }

// Start serves in the background until Close.
func (c *callbackServer) Start() {
	c.srv = &http.Server{Handler: c.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = c.srv.Serve(c.ln) }()
}

// Close shuts the listener down. It is safe to call twice, which matters
// because the flow closes it on both the success and the failure path.
func (c *callbackServer) Close() {
	c.closeOnce.Do(func() {
		if c.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = c.srv.Shutdown(ctx)
			return
		}
		_ = c.ln.Close()
	})
}

// Handler is the two-page site. It is separate from Start so that it can be
// tested with httptest and no real browser.
func (c *callbackServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		action := c.postURL
		if action != "" {
			action += "?state=" + url.QueryEscape(c.state)
		}
		_ = manifestPage.Execute(w, map[string]any{
			"Action":   action,
			"Manifest": string(c.manifest),
		})
	})
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		// GitHub sends the state back on the manifest redirect. An installation
		// redirect has no state of its own, so it is only checked when present.
		if got := q.Get("state"); got != "" && got != c.state {
			c.stateErrs <- errors.New("installer: a callback arrived with the wrong state value, so it was ignored; " +
				"this request did not come from this setup session -- start `zoomies init` again")
			http.Error(w, "This request did not come from this setup session. Close this page and start setup again.", http.StatusBadRequest)
			return
		}

		var res callbackResult
		res.Code = strings.TrimSpace(q.Get("code"))
		if id := strings.TrimSpace(q.Get("installation_id")); id != "" {
			res.InstallationID, _ = strconv.ParseInt(id, 10, 64)
		}
		if res.Code == "" && res.InstallationID == 0 {
			http.Error(w, "GitHub sent no code and no installation ID. Start setup again.", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = donePage.Execute(w, map[string]any{"Installed": res.InstallationID != 0})
		select {
		case c.results <- res:
		default:
		}
	})
	return mux
}

// WaitFor blocks until GitHub comes back, the operator pastes a value, the
// context ends or the timeout passes.
//
// The paste channel is the fallback that makes this work when the browser is
// on another machine: the operator copies the ?code= or ?installation_id= out
// of the URL bar and types it here.
func (c *callbackServer) WaitFor(ctx context.Context, timeout time.Duration, paste <-chan string, tick func(time.Duration)) (callbackResult, error) {
	deadline := time.Now().Add(timeout)
	// One second is the countdown's resolution. A shorter wait than that only
	// happens in tests, and it should end when it says it will.
	interval := time.Second
	if timeout < interval {
		interval = max(timeout/4, time.Millisecond)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if tick != nil {
			tick(time.Until(deadline).Truncate(time.Second))
		}
		select {
		case res := <-c.results:
			return res, nil
		case err := <-c.stateErrs:
			return callbackResult{}, err
		case line := <-paste:
			if res, ok := parsePasted(line); ok {
				return res, nil
			}
		case <-ctx.Done():
			return callbackResult{}, ctx.Err()
		case <-ticker.C:
			if time.Now().After(deadline) {
				return callbackResult{}, fmt.Errorf("installer: GitHub did not come back within %s; "+
					"if your browser is on another machine, run `zoomies init` again and paste the code from the "+
					"URL GitHub redirected to, or connect GitHub later on the Installations page", timeout)
			}
		}
	}
}

// parsePasted accepts what an operator actually pastes: a bare code, a bare
// installation ID, or the whole redirect URL with either in its query string.
func parsePasted(line string) (callbackResult, bool) {
	s := strings.TrimSpace(line)
	if s == "" {
		return callbackResult{}, false
	}
	if u, err := url.Parse(s); err == nil && u.Query() != nil {
		q := u.Query()
		if code := strings.TrimSpace(q.Get("code")); code != "" {
			return callbackResult{Code: code}, true
		}
		if id := strings.TrimSpace(q.Get("installation_id")); id != "" {
			n, err := strconv.ParseInt(id, 10, 64)
			if err == nil {
				return callbackResult{InstallationID: n}, true
			}
		}
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > 0 {
		return callbackResult{InstallationID: n}, true
	}
	return callbackResult{Code: s}, true
}

// lineReader turns an input stream into a channel of lines, so that a paste
// can be selected on alongside the browser callback and the countdown.
func lineReader(ctx context.Context, r io.Reader) <-chan string {
	out := make(chan string, 1)
	if r == nil {
		return out
	}
	go func() {
		defer close(out)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			select {
			case out <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// countdown returns a tick function that rewrites one line, so the operator
// can see that the installer is still waiting rather than hung.
func (i *Installer) countdown(label string) func(time.Duration) {
	if !i.interactive {
		return nil
	}
	return func(left time.Duration) {
		if left < 0 {
			left = 0
		}
		fmt.Fprintf(i.out, "\r      %s -- %s left, or paste the code and press enter    ", label, left)
	}
}

// clearLine wipes the countdown so the next line does not land on top of it.
func (i *Installer) clearLine() {
	if i.interactive {
		fmt.Fprint(i.out, "\r"+strings.Repeat(" ", 78)+"\r")
	}
}

// manifestPage auto-submits the manifest, because GitHub only accepts one as a
// form POST. The form is visible and has a button, so a browser with
// JavaScript disabled still works.
var manifestPage = template.Must(template.New("manifest").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Zoomies -- create the GitHub App</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:34rem;padding:0 1rem;color:#0B0C0E}
 @media (prefers-color-scheme:dark){body{background:#0A0B0D;color:#F4F5F7}}
 button{font:inherit;padding:.6rem 1.1rem;border-radius:.4rem;border:0;background:#1A63D8;color:#fff;cursor:pointer}
</style></head>
<body>
<h1>Creating your GitHub App</h1>
<p>Sending you to GitHub with the App's name, webhook URL and permissions already filled in.
If nothing happens, press the button.</p>
<form id="f" action="{{.Action}}" method="post">
  <input type="hidden" name="manifest" value="{{.Manifest}}">
  <button type="submit">Continue to GitHub</button>
</form>
<script>document.getElementById("f").submit();</script>
</body></html>`))

// donePage is what the operator sees when the handshake lands.
var donePage = template.Must(template.New("done").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Zoomies</title>
<style>
 body{font:16px/1.5 system-ui,sans-serif;margin:4rem auto;max-width:34rem;padding:0 1rem;color:#0B0C0E}
 @media (prefers-color-scheme:dark){body{background:#0A0B0D;color:#F4F5F7}}
</style></head>
<body>
{{if .Installed}}
<h1>Installed</h1>
<p>Zoomies has the installation. Go back to your terminal -- setup is carrying on there.</p>
{{else}}
<h1>App created</h1>
<p>Zoomies has the credentials. Go back to your terminal; it will send you here once more to
install the App on your organisation.</p>
{{end}}
</body></html>`))

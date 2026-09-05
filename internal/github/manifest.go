package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxAppNameLength is GitHub's limit on a GitHub App name. Hitting it during
// the manifest POST produces an unhelpful 422, so the check happens here.
const maxAppNameLength = 34

// ManifestOptions describes the App the installer asks GitHub to create.
type ManifestOptions struct {
	// Name is the App's display name, unique across all of GitHub.
	Name string
	// URL is the App's homepage, shown on its GitHub page.
	URL string
	// WebhookURL is where GitHub delivers workflow_job events.
	WebhookURL string
	// Organization is the org the App is created under. Empty creates it on
	// the operator's own account, which is the right choice for repo targets.
	Organization string
	// SetupURL is permanent App configuration: GitHub sends the operator here
	// after every future install or reconfiguration of the App -- adding a
	// repository to the installation, most commonly, years later. It has to be
	// an address that still answers then.
	SetupURL string
	// RedirectURL is where GitHub returns during *this* handshake, carrying
	// ?code=. Empty means "the same as SetupURL", which is right when the
	// handshake is driven from the controller's own web UI.
	//
	// They are separate because the installer is not: it catches the callback
	// on a temporary loopback listener that is gone minutes later, and writing
	// that ephemeral port into setup_url left every App `zoomies init` ever
	// created sending its operator to a refused connection.
	RedirectURL string
	// Public allows the App to be installed on accounts other than the one
	// that created it. Zoomies defaults it off: a fleet controller's App has
	// no business being installable by strangers.
	Public bool
}

// manifest is the wire format of GitHub's App manifest.
type manifest struct {
	Name               string            `json:"name"`
	URL                string            `json:"url"`
	HookAttributes     hookAttributes    `json:"hook_attributes"`
	RedirectURL        string            `json:"redirect_url,omitempty"`
	SetupURL           string            `json:"setup_url,omitempty"`
	SetupOnUpdate      bool              `json:"setup_on_update"`
	Description        string            `json:"description,omitempty"`
	Public             bool              `json:"public"`
	DefaultEvents      []string          `json:"default_events"`
	DefaultPermissions map[string]string `json:"default_permissions"`
}

// hookAttributes carries the webhook configuration. GitHub's manifest schema
// permits exactly url and active here: sending a "secret" key makes GitHub
// reject the whole manifest with `"secret" is not a permitted key`. The secret
// is GitHub's to generate, and it comes back from the conversion in
// ManifestCredentials.WebhookSecret.
type hookAttributes struct {
	URL    string `json:"url"`
	Active bool   `json:"active"`
}

// Manifest renders the JSON an operator's browser POSTs to GitHub to create
// the App.
//
// The permission set is deliberately minimal, because an App that manages a
// fleet's runners is a high-value credential: it asks for the runner
// administration permission for the kind of target it will manage, read access
// to Actions so it can see queued jobs, the three the migration wizard needs to
// rewrite a runs-on line and open a pull request for it, and nothing else.
// Metadata read is mandatory for every App.
//
// Every key here is one GitHub's manifest schema permits; it rejects anything
// else outright, so nothing speculative belongs in this struct.
func Manifest(o ManifestOptions) ([]byte, error) {
	name := strings.TrimSpace(o.Name)
	switch {
	case name == "":
		return nil, fmt.Errorf("github: app manifest: a name is required; pick something that "+
			"identifies this controller, such as %q", "zoomies-acme")
	case len(name) > maxAppNameLength:
		return nil, fmt.Errorf("github: app manifest: the name %q is %d characters; GitHub allows "+
			"at most %d", name, len(name), maxAppNameLength)
	}
	if strings.TrimSpace(o.URL) == "" {
		return nil, fmt.Errorf("github: app manifest: a homepage url is required; set " +
			"server.external_url in zoomies.yaml")
	}
	if strings.TrimSpace(o.WebhookURL) == "" {
		return nil, fmt.Errorf("github: app manifest: a webhook url is required; set " +
			"server.external_url in zoomies.yaml so GitHub can reach this controller")
	}

	redirect := o.RedirectURL
	if redirect == "" {
		redirect = o.SetupURL
	}

	m := manifest{
		Name: name,
		URL:  o.URL,
		// No secret is sent: GitHub generates one for a manifest-created App
		// and returns it through the conversion, which Zoomies then seals.
		HookAttributes: hookAttributes{URL: o.WebhookURL, Active: true},
		RedirectURL:    redirect,
		SetupURL:       o.SetupURL,
		Description:    "Self-hosted runner fleet managed by Zoomies.",
		Public:         o.Public,
		// workflow_job is the only event Zoomies acts on. Subscribing to more
		// would mean parsing payloads it has no use for.
		DefaultEvents:      []string{"workflow_job"},
		DefaultPermissions: manifestPermissions(o.Organization != ""),
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil, fmt.Errorf("github: app manifest: %w", err)
	}
	return b, nil
}

// manifestPermissions returns the least privilege that works for the target
// kind. An org App manages runners through the organisation permission and
// never needs repository administration; a repo App is the other way round.
//
// The three migration permissions are asked for here, at creation, rather than
// left for the operator to add later. Adding a permission to an App that
// already exists is not a setting an operator can just flip: GitHub holds the
// change until the account's owner accepts it on the installation, and until
// they do the migration wizard cannot even *read* a workflow -- it reports
// every repository as unreadable, which is a broken product rather than a
// missing permission. Asking once, on the consent screen the operator is
// already reading, is both honest and the only point in the flow where saying
// yes costs a click.
func manifestPermissions(org bool) map[string]string {
	p := map[string]string{
		"actions":  "read",
		"metadata": "read",
		// The migration wizard's three: read a repository's workflows, commit
		// the rewritten file to a branch, and open the pull request. GitHub
		// requires "workflows" specifically for a change under
		// .github/workflows, and grants nothing else with it.
		"contents":      "write",
		"pull_requests": "write",
		"workflows":     "write",
	}
	if org {
		p["organization_self_hosted_runners"] = "write"
	} else {
		p["administration"] = "write"
	}
	return p
}

// ManifestURL returns the page the manifest form posts to. GitHub has a
// separate endpoint for creating an App under an organisation, and posting the
// org manifest to the user endpoint silently creates a personal App instead.
func ManifestURL(apiBaseURL, org string) string {
	web := WebURLForAPI(apiBaseURL)
	if org = strings.TrimSpace(org); org != "" {
		return web + "/organizations/" + org + "/settings/apps/new"
	}
	return web + "/settings/apps/new"
}

// ManifestCredentials is everything GitHub hands back once the operator has
// created the App. Every field except HTMLURL is a secret or identifies one.
type ManifestCredentials struct {
	AppID int64
	Slug  string
	Name  string
	// PEM is the App's private key. It is shown once and must be sealed
	// immediately.
	PEM string
	// WebhookSecret is the secret GitHub generated for the App's webhook. It
	// is the only copy: the manifest cannot ask for a particular one, and
	// GitHub does not show it again.
	WebhookSecret string
	ClientID      string
	ClientSecret  string
	HTMLURL       string
}

// manifestConversion is the wire format of the conversion response.
type manifestConversion struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	PEM           string `json:"pem"`
	WebhookSecret string `json:"webhook_secret"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	HTMLURL       string `json:"html_url"`
	Message       string `json:"message"`
}

// ExchangeManifestCode trades the temporary code GitHub redirected back with
// for the App's real credentials.
//
// The code is valid for one hour and exactly one exchange, so a failure here
// means starting the manifest flow again rather than retrying.
func ExchangeManifestCode(ctx context.Context, apiBaseURL, code string) (*ManifestCredentials, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("github: exchange manifest code: no code was returned by GitHub; " +
			"start the App creation again from the setup page")
	}
	base, err := NormalizeAPIBaseURL(apiBaseURL)
	if err != nil {
		return nil, fmt.Errorf("github: exchange manifest code: unusable api base url %q: %w", apiBaseURL, err)
	}
	url := base + "app-manifests/" + code + "/conversions"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	if err != nil {
		return nil, fmt.Errorf("github: exchange manifest code: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	client := &http.Client{Timeout: defaultHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github: exchange manifest code: cannot reach %s: %w", base, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("github: exchange manifest code: reading response: %w", err)
	}
	var out manifestConversion
	// A non-JSON body here usually means a proxy answered instead of GitHub,
	// so the decode error is reported only when the status was otherwise fine.
	decodeErr := json.Unmarshal(bytes.TrimSpace(body), &out)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		msg := out.Message
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("%w: the manifest code has already been used or has expired "+
				"(codes last one hour): start the App creation again", ErrNotFound)
		}
		return nil, fmt.Errorf("github: exchange manifest code: GitHub returned %s: %s", resp.Status, msg)
	}
	if decodeErr != nil {
		return nil, fmt.Errorf("github: exchange manifest code: %s did not return JSON, which "+
			"usually means a proxy answered instead of GitHub: %w", url, decodeErr)
	}
	if out.ID == 0 || out.PEM == "" {
		return nil, fmt.Errorf("github: exchange manifest code: GitHub returned no app id or " +
			"private key; start the App creation again")
	}
	return &ManifestCredentials{
		AppID:         out.ID,
		Slug:          out.Slug,
		Name:          out.Name,
		PEM:           out.PEM,
		WebhookSecret: out.WebhookSecret,
		ClientID:      out.ClientID,
		ClientSecret:  out.ClientSecret,
		HTMLURL:       out.HTMLURL,
	}, nil
}

// InstallURL returns the page that installs an App on an account, given the
// html_url the manifest conversion returned.
func InstallURL(htmlURL string) string {
	h := strings.TrimRight(strings.TrimSpace(htmlURL), "/")
	if h == "" {
		return ""
	}
	return h + "/installations/new"
}

// SettingsURL returns the App's own settings page, which is the only place its
// logo can be set.
//
// The App manifest schema has no field for an avatar -- GitHub takes it as an
// upload and nothing else -- so an App created by Zoomies arrives with the grey
// default, and every "Set up job" line in the organisation's logs is signed by
// an anonymous App. Pointing the operator at the page and handing them the
// mark is as close to automatic as GitHub allows.
//
// An App owned by an organisation has its settings under that organisation;
// one owned by a user has them under the user's own settings, and GitHub
// answers the wrong URL with a 404 rather than a redirect.
func SettingsURL(apiBaseURL, slug, org string) string {
	slug = strings.Trim(strings.TrimSpace(slug), "/")
	if slug == "" {
		return ""
	}
	web := strings.TrimRight(WebURLForAPI(apiBaseURL), "/")
	if org = strings.Trim(strings.TrimSpace(org), "/"); org != "" {
		return web + "/organizations/" + org + "/settings/apps/" + slug
	}
	return web + "/settings/apps/" + slug
}

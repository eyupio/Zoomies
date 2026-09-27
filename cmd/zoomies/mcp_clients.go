package main

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// runMCPClients is `zoomies mcp-clients ...`: the OAuth clients that may ask
// a person for an MCP connection. Most never need one made by hand -- Claude
// registers itself -- but a controller with open registration off, or an
// administrator who wants a client they can name and revoke, makes one here
// and types its ID into Claude's connector form.
func runMCPClients(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "mcp-clients", "OAuth clients for /mcp. A secret exists in plaintext exactly once, when it is made.", []*subcommand{
		{"list", "", "Every client, and how many connections each holds", mcpClientsList},
		{"create", "--name <n>", "Make a client and print its ID, and its secret once", mcpClientsCreate},
		{"rotate-secret", "<client-id>", "Replace a confidential client's secret", mcpClientsRotate},
		{"revoke", "<client-id>", "Revoke a client and end its connections", mcpClientsRevoke},
	}, args)
}

type mcpClientItem struct {
	ID           string     `json:"id"`
	ClientID     string     `json:"client_id"`
	Kind         string     `json:"kind"`
	Name         string     `json:"name"`
	RedirectURIs []string   `json:"redirect_uris"`
	Confidential bool       `json:"confidential"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
	Connections  int        `json:"connections"`
	// ClientSecret comes back once, from create and rotate-secret.
	ClientSecret string `json:"client_secret"`
}

func mcpClientsList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies mcp-clients list", "List the OAuth clients that may ask for an MCP connection. Never a secret.")
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
	var out listResponse[mcpClientItem]
	raw, err := client.get(ctx, "/mcp-clients", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No MCP clients yet. Claude registers one when it connects; make one by hand with: zoomies mcp-clients create --name Claude")
		return nil
	}
	rows := make([][]string, 0, len(out.Items))
	for _, c := range out.Items {
		state := "active"
		if c.RevokedAt != nil {
			state = p.paint(colourDim, "revoked")
		}
		kind := c.Kind
		if c.Confidential {
			kind += ", secret"
		}
		rows = append(rows, []string{c.Name, c.ID, kind, strconv.Itoa(c.Connections), state, p.relTimePtr(c.LastUsedAt)})
	}
	p.table([]string{"name", "id", "kind", "connections", "state", "last used"}, rows)
	return nil
}

func mcpClientsCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies mcp-clients create --name <name>",
		"Make an OAuth client for an MCP client such as Claude, to be given its ID -- and secret -- by hand.")
	cf := registerClientFlags(fs, true)
	name := fs.String("name", "", "what people see on the consent screen, e.g. Claude (required)")
	redirects := &listValue{}
	fs.Var(redirects, "redirect-uri", "where the client is sent back to (repeatable); defaults to https://claude.ai/api/mcp/auth_callback")
	secret := fs.Bool("secret", false, "make it confidential: it proves a secret at the token endpoint as well as PKCE")
	fs.example(
		"zoomies mcp-clients create --name Claude --secret",
		"zoomies mcp-clients create --name 'Claude Code' --redirect-uri http://localhost/callback",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return usagef("mcp-clients create", "needs --name; it is what people see when they are asked to approve it")
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	body := map[string]any{"name": *name, "confidential": *secret}
	if len(*redirects) > 0 {
		body["redirect_uris"] = []string(*redirects)
	}
	var c mcpClientItem
	raw, err := client.post(ctx, "/mcp-clients", nil, body, &c)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	printMCPClient(e, p, c)
	return nil
}

func printMCPClient(e *env, p *printer, c mcpClientItem) {
	pairs := [][2]string{{"name", c.Name}, {"client id", c.ClientID}}
	if c.ClientSecret != "" {
		pairs = append(pairs, [2]string{"client secret", c.ClientSecret})
	}
	pairs = append(pairs, [2]string{"redirect uris", strings.Join(c.RedirectURIs, " ")})
	p.keyValues(pairs)
	fmt.Fprintln(e.out, "\nIn Claude's Add custom connector dialog, put these under Advanced settings as the OAuth Client ID and Client Secret.")
	if c.ClientSecret != "" {
		fmt.Fprintln(e.out, "This is the only time the secret is shown.")
	}
}

func mcpClientsRotate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies mcp-clients rotate-secret <client-id>", "Replace a confidential client's secret. The old one stops working immediately.")
	cf := registerClientFlags(fs, true)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a client ID, as shown by `zoomies mcp-clients list`")
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
	var c mcpClientItem
	raw, err := client.post(ctx, "/mcp-clients/"+url.PathEscape(id)+"/secret", nil, nil, &c)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	printMCPClient(e, p, c)
	return nil
}

func mcpClientsRevoke(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies mcp-clients revoke <client-id>", "Revoke a client. Every connection made with it ends immediately.")
	cf := registerClientFlags(fs, false)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a client ID, as shown by `zoomies mcp-clients list`")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	if _, err := client.del(ctx, "/mcp-clients/"+url.PathEscape(id), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Revoked MCP client %s and ended its connections.\n", id)
	return nil
}

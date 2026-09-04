package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// runTokens is `zoomies tokens ...`: the credentials that let this CLI, and
// anything else, talk to the API.
func runTokens(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "tokens", "API tokens. The value exists in plaintext exactly once, at creation.", []*subcommand{
		{"list", "", "Every token's metadata; never its value", tokensList},
		{"create", "--name <n> --role <r>", "Mint a token and print it once", tokensCreate},
		{"revoke", "<token-id>", "Revoke a token immediately", tokensRevoke},
	}, args)
}

func tokensList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies tokens list", "List API tokens. Metadata only -- the value is not stored.")
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

	var out listResponse[tokenItem]
	raw, err := client.get(ctx, "/tokens", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No API tokens. Mint one with: zoomies tokens create --name my-laptop --role operator")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, t := range out.Items {
		state := "active"
		switch {
		case t.Revoked:
			state = p.paint(colourDim, "revoked")
		case t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()):
			state = p.paint(colourRed, "expired")
		}
		expires := "never"
		if t.ExpiresAt != nil {
			expires = p.relTime(*t.ExpiresAt)
		}
		rows = append(rows, []string{
			t.Name,
			t.ID,
			t.Role,
			dash(t.Prefix),
			dash(strings.Join(t.Scopes, ",")),
			state,
			expires,
			p.relTimePtr(t.LastUsedAt),
		})
	}
	p.table([]string{"name", "id", "role", "prefix", "scopes", "state", "expires", "last used"}, rows)
	return nil
}

func tokensCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies tokens create --name <name> --role <viewer|operator|admin>",
		"Mint an API token. It is printed once and only its hash is kept.")
	cf := registerClientFlags(fs, true)
	name := fs.String("name", "", "what this token is for, e.g. my-laptop or prometheus (required)")
	role := fs.String("role", "viewer", "viewer, operator or admin")
	scopes := &listValue{}
	fs.Var(scopes, "scope", "narrow the token within its role, e.g. runners:read (repeatable)")
	expiresIn := fs.Duration("expires-in", 0, "how long the token lasts; zero means never, which is worth avoiding")
	fs.example(
		"zoomies tokens create --name my-laptop --role operator --expires-in 2160h",
		"zoomies tokens create --name prometheus --role viewer --scope metrics:read",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*name) == "" {
		return usagef("tokens create", "needs --name; a token nobody can identify is a token nobody dares revoke")
	}
	if !validRole(*role) {
		return usagef("tokens create", "--role %q is not a role; use viewer, operator or admin", *role)
	}

	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	body := map[string]any{"name": *name, "role": *role}
	if len(*scopes) > 0 {
		body["scopes"] = []string(*scopes)
	}
	if *expiresIn > 0 {
		body["expires_in"] = expiresIn.String()
	}

	var token tokenItem
	raw, err := client.post(ctx, "/tokens", nil, body, &token)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}

	fmt.Fprintf(e.out, "%s\n\n", token.Token)
	p.keyValues([][2]string{
		{"name", token.Name},
		{"id", token.ID},
		{"role", token.Role},
		{"expires", map[bool]string{true: "never", false: p.relTimePtr(token.ExpiresAt)}[token.ExpiresAt == nil]},
	})
	fmt.Fprintln(e.out, "\nThis is the only time the token is shown. To use it from here:")
	fmt.Fprintf(e.out, "  export ZOOMIES_TOKEN=%s\n", token.Token)
	return nil
}

func tokensRevoke(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies tokens revoke <token-id>", "Revoke a token. It stops working immediately.")
	cf := registerClientFlags(fs, false)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a token ID, as shown by `zoomies tokens list`")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	if _, err := client.del(ctx, "/tokens/"+url.PathEscape(id), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Revoked token %s.\n", id)
	return nil
}

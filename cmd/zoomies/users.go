package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// runUsers is `zoomies users ...`. The API refuses to leave an instance with no
// enabled administrator, so this command does not have to police that itself --
// it just has to report the refusal clearly when it comes.
func runUsers(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "users", "User accounts.", []*subcommand{
		{"list", "", "Every account and its role", usersList},
		{"create", "--username <n> --role <r>", "Create an account", usersCreate},
		{"delete", "<user-id>", "Delete an account", usersDelete},
	}, args)
}

func usersList(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies users list", "List the accounts that can sign in.")
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

	var out listResponse[userItem]
	raw, err := client.get(ctx, "/users", nil, &out)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	if len(out.Items) == 0 {
		p.note("No users. This instance still needs its first administrator; open the UI to create one.")
		return nil
	}

	rows := make([][]string, 0, len(out.Items))
	for _, u := range out.Items {
		state := "enabled"
		if u.Disabled {
			state = p.paint(colourDim, "disabled")
		}
		if u.MustChangePassword {
			state += p.paint(colourYellow, " (must change password)")
		}
		rows = append(rows, []string{
			u.Username,
			u.ID,
			u.Role,
			dash(u.DisplayName),
			dash(u.Email),
			state,
			p.relTimePtr(u.LastLoginAt),
		})
	}
	p.table([]string{"username", "id", "role", "name", "email", "state", "last login"}, rows)
	return nil
}

func usersCreate(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies users create --username <name> --role <viewer|operator|admin>",
		"Create an account. Omit --password for one that will sign in through single sign-on.")
	cf := registerClientFlags(fs, true)
	username := fs.String("username", "", "the account's login name (required)")
	password := fs.String("password", "", "an initial password of at least 12 characters; omit for an SSO-only account")
	email := fs.String("email", "", "the account's email address")
	displayName := fs.String("display-name", "", "how the account's name is shown")
	role := fs.String("role", "viewer", "viewer, operator or admin")
	fs.example(
		"zoomies users create --username alex --role operator --email alex@example.com",
		"zoomies users create --username ci-readonly --role viewer",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" {
		return usagef("users create", "needs --username")
	}
	if !validRole(*role) {
		return usagef("users create", "--role %q is not a role; use viewer, operator or admin", *role)
	}

	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}

	body := map[string]any{"username": *username, "role": *role}
	if *password != "" {
		body["password"] = *password
	}
	if *email != "" {
		body["email"] = *email
	}
	if *displayName != "" {
		body["display_name"] = *displayName
	}

	var user userItem
	raw, err := client.post(ctx, "/users", nil, body, &user)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	fmt.Fprintf(e.out, "Created %s (%s) as %s.\n", user.Username, user.ID, user.Role)
	if *password == "" {
		fmt.Fprintln(e.out, "No password was set, so this account can only sign in through single sign-on.")
	}
	return nil
}

func usersDelete(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies users delete <user-id>",
		"Delete an account. Refused if it would leave the instance with no enabled administrator.")
	cf := registerClientFlags(fs, false)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a user ID, as shown by `zoomies users list`")
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	if _, err := client.del(ctx, "/users/"+url.PathEscape(id), nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Deleted user %s.\n", id)
	return nil
}

func validRole(role string) bool {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "viewer", "operator", "admin":
		return true
	}
	return false
}

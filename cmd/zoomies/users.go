package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"golang.org/x/term"
)

// runUsers is `zoomies users ...`. The API refuses to leave an instance with no
// enabled administrator, so this command does not have to police that itself --
// it just has to report the refusal clearly when it comes.
func runUsers(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "users", "User accounts.", []*subcommand{
		{"list", "", "Every account and its role", usersList},
		{"create", "--username <n> --role <r>", "Create an account", usersCreate},
		{"passwd", "<user-id>", "Set an account's password", usersPasswd},
		{"reset-two-step", "<user-id>", "Turn off an account's two-step verification", usersResetTwoStep},
		{"delete", "<user-id>", "Delete an account", usersDelete},
	}, args)
}

// usersPasswd is the administrator's reset, from a terminal.
//
// The password is read from the terminal, or from standard input when it is
// piped in; there is deliberately no --password flag. A password in argv is a
// password in /proc/<pid>/cmdline, which every account on the host can read,
// and in the operator's shell history afterwards.
func usersPasswd(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies users passwd <user-id>",
		"Set an account's password. Every session it had ends, and its owner is asked to choose their own at the next sign-in.")
	cf := registerClientFlags(fs, false)
	fs.example(
		"zoomies users passwd usr_7f3a",
		`printf '%s' "$new" | zoomies users passwd usr_7f3a`,
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	id, err := fs.oneArg("a user ID, as shown by `zoomies users list`")
	if err != nil {
		return err
	}
	password, err := readSecret(e, "New password: ")
	if err != nil {
		return err
	}
	if password == "" {
		return usagef("users passwd", "no password was given; type one at the prompt or pipe one in")
	}

	client, err := cf.client()
	if err != nil {
		return err
	}
	body := map[string]any{"new_password": password}
	if _, err := client.post(ctx, "/users/"+url.PathEscape(id)+"/password", nil, body, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Set a new password for %s. Their sessions have ended, and they choose their own at the next sign-in.\n", id)
	return nil
}

// readSecret reads one line, without echoing it when there is a terminal to
// turn the echo off on, and from standard input otherwise -- which is what
// makes the piped form work in a script.
func readSecret(e *env, prompt string) (string, error) {
	if f, ok := e.in.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(e.err, prompt)
		typed, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(e.err)
		if err != nil {
			return "", fmt.Errorf("reading the password: %w", err)
		}
		return strings.TrimSpace(string(typed)), nil
	}
	line, err := bufio.NewReader(e.in).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("reading the password from standard input: %w", err)
	}
	return strings.TrimSpace(line), nil
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
	fs := newFlagSet(e, "zoomies users create --username <name> --role <viewer|operator|admin|platform>",
		"Create an account. Omit --password for one that will sign in through single sign-on.")
	cf := registerClientFlags(fs, true)
	username := fs.String("username", "", "the account's login name (required)")
	password := fs.String("password", "", "an initial password of at least 12 characters; omit for an SSO-only account")
	email := fs.String("email", "", "the account's email address")
	displayName := fs.String("display-name", "", "how the account's name is shown")
	role := fs.String("role", "viewer", "viewer, operator, admin or platform")
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

// usersResetTwoStep is the administrator's answer to a lost phone: the
// account's authenticator secret and recovery codes are removed and its
// sessions end, so its owner signs in with the password alone -- or, where the
// instance requires two-step, sets it up again at that sign-in.
func usersResetTwoStep(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies users reset-two-step <user-id>",
		"Turn off two-step verification for an account whose owner has lost their authenticator and their recovery codes. Its sessions end, and the reset is audited.")
	cf := registerClientFlags(fs, false)
	fs.example("zoomies users reset-two-step usr_7f3a")
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
	if _, err := client.del(ctx, "/users/"+url.PathEscape(id)+"/two-step", nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Reset two-step verification for %s. Their sessions have ended; they sign in with their password next time.\n", id)
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
	case "viewer", "operator", "admin", "platform":
		return true
	}
	return false
}

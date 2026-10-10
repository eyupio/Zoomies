package proxmoxsetup

import (
	"context"
	"fmt"
	"strings"
)

// VerifyAccess proves, on the Proxmox host, that the token setup has just made
// can do everything the provider will ask of it, before the controller is told
// the connection exists.
//
// check is the same preflight the Check button runs, acting as the token, and
// returns one line per thing that stops the provider working. If it finds
// something, the one repair that is safe to make unasked is made, an explicit
// grant on the node itself, and it is run again. Nothing broader is granted:
// a role that quietly widened itself to make a check pass would be worse than
// a check that failed. What still fails is returned with its reasons, and the
// setup stops there, so a provider that could not work is never saved.
func VerifyAccess(ctx context.Context, h Host, tokenID, role, node string, check func(context.Context) []string) error {
	problems := check(ctx)
	if len(problems) == 0 {
		return nil
	}
	user, _, _ := strings.Cut(tokenID, "!")
	path := "/nodes/" + node
	// Best effort: whether it helps is what the second check says.
	_, _ = h.Run(ctx, "pveum", "acl", "modify", path, "--users", user, "--roles", role)
	_, _ = h.Run(ctx, "pveum", "acl", "modify", path, "--tokens", tokenID, "--roles", role)
	if problems = check(ctx); len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("proxmox setup: the new API token cannot do what the provider needs, so nothing was saved:\n  - %s\n"+
		"See what the token may do with: pveum user permissions '%s' --path %s\n"+
		"Fix the first item, then rerun the setup command; the token and gateway are reused",
		strings.Join(problems, "\n  - "), tokenID, path)
}

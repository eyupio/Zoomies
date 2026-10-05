package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/eyupio/zoomies/internal/controller"
)

// The refusals that keep a host's settings and a pool's requirements from
// being edited into contradiction.
//
// Both are 409 rather than 422, for the reason ErrConfirmationRequired is:
// the request is well formed and it is the rest of the fleet that decides. An
// operator shrinking a host before they delete the pool that used it is right,
// so neither refusal is final -- each says what would be stranded, and each
// says how to mean it anyway.
//
// The sentence names the pool, the machine and the shortfall, because those
// three are the whole decision. "That would strand a pool" sends an operator
// to compare two forms on two pages by eye, which is the work this check
// exists to do for them.

const strandingConfirm = "send it again with confirm=true to save it anyway"

// strandingByHand is what the same refusal says to a suggestion being applied. The
// apply route never sends confirm, so the sentence above would send its caller to a
// flag it cannot set; a suggestion is not applied past this check, and the person
// who means it makes the change themselves.
const strandingByHand = "make the change yourself if you mean it, because a suggestion is never applied past this check"

type remedyApplyKey struct{}

// forRemedy marks a request as the update a suggestion is being applied through.
func forRemedy(ctx context.Context) context.Context {
	return context.WithValue(ctx, remedyApplyKey{}, true)
}

// strandingEnding is how a stranding refusal ends for this request.
func strandingEnding(r *http.Request) string {
	if r.Context().Value(remedyApplyKey{}) != nil {
		return strandingByHand
	}
	return strandingConfirm
}

// hostStrandingRefusal answers a host edit that would leave pools homeless.
func hostStrandingRefusal(host string, stranded []controller.Stranding, ending string) string {
	if len(stranded) == 1 {
		s := stranded[0]
		return fmt.Sprintf("saving %s as described would leave pool %s with nowhere to run: %s, "+
			"and no other host in this fleet could run it. Lower what %s holds back or the size of its runners, change what %s asks for, "+
			"or %s.", host, s.Pool, s.Reason, host, s.Pool, ending)
	}
	parts := make([]string, 0, len(stranded))
	names := make([]string, 0, len(stranded))
	for _, s := range stranded {
		parts = append(parts, s.Pool+" ("+s.Reason+")")
		names = append(names, s.Pool)
	}
	return fmt.Sprintf("saving %s as described would leave %d pools with nowhere to run, and no other host in this "+
		"fleet could run them: %s. Lower what %s holds back or the size of its runners, change what %s ask for, or %s.",
		host, len(stranded), strings.Join(parts, "; "), host, strings.Join(names, " and "), ending)
}

// poolStrandingRefusal answers the same edit made from the pool's side. The
// host it names is one the pool fits on today, which is the machine the new
// figures have to be read against: a count of hosts is not something an
// operator can go and look at.
func poolStrandingRefusal(s controller.Stranding, ending string) string {
	return fmt.Sprintf("saving %s as described would leave it with nowhere to run: %s %s, and no other host in "+
		"this fleet could run it either. Ask for less, adjust %s to match, or %s.",
		s.Pool, s.Host, s.Reason, s.Host, ending)
}

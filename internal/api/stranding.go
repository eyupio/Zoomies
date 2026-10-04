package api

import (
	"fmt"
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

// hostStrandingRefusal answers a host edit that would leave pools homeless.
func hostStrandingRefusal(host string, stranded []controller.Stranding) string {
	if len(stranded) == 1 {
		s := stranded[0]
		return fmt.Sprintf("saving %s as described would leave pool %s with nowhere to run: %s, "+
			"and no other host in this fleet could run it. Lower what %s holds back or the size of its runners, change what %s asks for, "+
			"or %s.", host, s.Pool, s.Reason, host, s.Pool, strandingConfirm)
	}
	parts := make([]string, 0, len(stranded))
	names := make([]string, 0, len(stranded))
	for _, s := range stranded {
		parts = append(parts, s.Pool+" ("+s.Reason+")")
		names = append(names, s.Pool)
	}
	return fmt.Sprintf("saving %s as described would leave %d pools with nowhere to run, and no other host in this "+
		"fleet could run them: %s. Lower what %s holds back or the size of its runners, change what %s ask for, or %s.",
		host, len(stranded), strings.Join(parts, "; "), host, strings.Join(names, " and "), strandingConfirm)
}

// poolStrandingRefusal answers the same edit made from the pool's side. The
// host it names is one the pool fits on today, which is the machine the new
// figures have to be read against: a count of hosts is not something an
// operator can go and look at.
func poolStrandingRefusal(s controller.Stranding) string {
	return fmt.Sprintf("saving %s as described would leave it with nowhere to run: %s %s, and no other host in "+
		"this fleet could run it either. Ask for less, adjust %s to match, or %s.",
		s.Pool, s.Host, s.Reason, s.Host, strandingConfirm)
}

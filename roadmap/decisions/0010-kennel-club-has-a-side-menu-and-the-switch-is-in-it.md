# 0010: Kennel Club has a side menu, and its on/off switch is in it

**Status**: accepted. Taken on 7 October 2026 on the owner's instruction, after
the owner said that AI Context was hard to find, that turning Kennel Club on meant
leaving for Settings, and that the section needed a menu like Settings' with the
switch on the page.

## Context

The section's pages were reached by buttons in page headers: the Overview had one
for the repositories and one for AI Context. AI Context moved under Kennel Club
without a navigation entry of its own, so it was one button among several on a page
about something else, and people did not find it.

Turning Kennel Club on meant Settings, Configuration, finding `kennel.enabled`
among the eighty or so settings, and coming back.

## Decision

A side menu, in the shape Settings' has: a registry of pages, a rail at the left
where there is room, and a row above the page where there is not. It lists
**Overview**, **Repositories** and **AI Context**, each with an address of its own,
and marks the one on screen. A repository's own page is under Repositories. The AI
Context setup wizard has no menu: it is a task with a way back, and not a place.

Two groups, because the pages are governed by different things. The switch heads
**Standards**, which is what it turns off. **AI Context** is in a group of its own,
because nothing in it reads `kennel.enabled` and it keeps working with Kennel Club
off; a row for it under the switch would say the opposite.

The switch is the `kennel.enabled` setting itself, saved the same way and audited
the same way as in Settings, from the page it governs:

* only an administrator can press it, and anybody else sees the state and who can
  change it, instead of a control that answers 403;
* turning it off asks first and says what it does, and turning it on does not,
  because the page that explains itself when Kennel Club is off already says what it
  reads;
* it shows what the controller says and not what was asked for, so a change that was
  refused, or accepted and then overruled by the environment, puts it back and says
  why;
* it follows the event stream, and asks again when the stream comes up or says it
  lost its place, so a change made in another tab is seen;
* the answer to a change is adopted when it arrives, and every page of the section
  asks again because of it. A page that waited for the stream's frame would be wrong
  exactly when the connection is the problem, and a repository page that is open when
  Kennel Club is turned off says so, as it would have on arrival, and does not go on
  showing what it read.

The Overview's header buttons for the repositories and AI Context are gone, because
the menu has them, and "Turn on in Settings" is a button that does what the switch
does, in the place that says what turning it on means.

On a phone the row wraps and does not scroll, with the switch on a line of its own,
so no page of the section can be pushed out of sight; and under a coarse pointer the
rows take `--z-control-touch`, as the rest of the product's small controls do.

## Consequences

The switch is the fleet's, not a repository's. The per-repository Track switch
([0006](0006-kennel-club-tracks-repositories-one-at-a-time.md)) is separate, and
comes later.

The controller sends `kennel.summary` after a reconcile pass and only when the
document differs from the one it last sent, and sends nothing while nobody is
connected. So a switch follows a change made elsewhere within about a pass, and a
page that has just loaded can, correctly, miss a flip and a flip back between two
passes. This is how every consumer of the summary behaves; the tests open the page
first and wait for each change.

What is left from the same request, and is not in this change: the Overview's
cards linking to the repositories they count, and a state on the AI Context row,
such as how many repositories are set up.

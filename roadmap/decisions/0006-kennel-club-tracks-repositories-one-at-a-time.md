# 0006: Kennel Club tracks repositories one at a time

**Status**: accepted. Taken on 7 October 2026 on the owner's instruction, in
answer to the question whether Kennel Club should be able to look at some
repositories and not others. The two things it first left open were settled by
the owner the same day, by question: the split of roles under *Decision* (an
administrator stops tracking, an operator starts it again) stands, and an
untracked repository is **not** pruned after ninety days (see *Consequences*).

## Context

[The plan](../kennel-club.md) made Kennel Club one fleet-wide switch
(`kennel.enabled`), one scope setting (`kennel.scope`: the repositories the
fleet has served, or every repository the App can see) and a list of checks to
turn off for everyone (`kennel.disabled_checks`). It closed the door on anything
finer in a sentence: *a per-repository exception is a waiver, not
configuration.*

A waiver is the right answer to *this finding is acceptable here*. It has a
reason, an owner and an end, and it covers one finding. It is the wrong answer
to *this repository is not something Kennel Club should be looking at at all*:
a sandbox, a fork of somebody else's project, an archived repository that still
has a stray job. Waiving each finding on such a repository is a decision per
finding, and the repository still costs requests from the GitHub budget and a
place in every count.

What is built when this is taken (#661, #669, #670) has no per-repository state
beyond its waivers: a row in `kennel_repositories` exists for every repository
the fleet has served, and every one is read and evaluated.

## Decision

Give every repository a tracking state, **tracked by default**. A repository
that is not tracked is not read from GitHub, not evaluated, raises no finding
and no problem, and is counted apart from the Overview's totals as *not
tracked*. It stays listed, with a filter for it, and its page still shows what
the fleet knows about it, because that comes from the fleet's own records and
not from GitHub. Its waivers are kept and do nothing until it is tracked again.

Record who stopped tracking it, when and why (a reason of the same length as a
waiver's, and no end date, because tracking is a state and not a decision to be
renewed) and show that beside the switch. Stopping is an administrator's
decision, because it silences errors; starting again is an operator's, because it
only makes Kennel Club stricter. Waivers stay, for a single finding.

## Consequences

One new table in migration `0082`, `kennel_untracked`, with a row for each
untracked repository (who, name, when, why) and no row meaning tracked. It is
additive, so `TestKennelMigrationsOnlyAddTables` still holds, and a controller
that never uses the switch behaves exactly as before.

Every place that counts has to say what it does with an untracked repository:
the Overview's totals and its "counts are a minimum" wording (an untracked
repository is a choice, not an unread one), the `kennel.exposure` problem
(cleared on stopping, raised again on starting), the metrics, the MCP tools and
the `kennel.updated` frame. The API gains `PUT /kennel/repositories/{id}/tracking`,
two RBAC actions and two audit actions, `kennel.untrack` with the reason and
`kennel.track`.

It supersedes the plan's sentence about exceptions, and the plan is changed to
say so. It rules out a silent exclusion: an untracked repository is always listed
and always counted somewhere. It does not settle exclusion by pattern; a fleet of
hundreds of repositories may want one, and that is a decision for when somebody
has the problem. A bulk action in the list is the most it promises.

A repository row is pruned ninety days after it was last served, and it used to
take its untracking with it, so a repository that was quiet for a quarter came back
tracked, and was read, the day somebody next pushed to it. The owner decided that
an untracked row outlives that: the prune leaves a repository that is not tracked
alone, and forgets it like any other once it is tracked again. A decision somebody
took is not undone by a quiet quarter. The cost is a row for an archived
repository that stays until somebody tracks it, and it is a small one.

How it is built follows from that and from what an untracked repository must not
be: the row is put back to what it was before anything evaluated it (no findings,
counts or coverage, `pending`, due for nothing), so there is nothing for a count, a
problem or a severity filter to remember is stale. Its waivers and the fleet's
record of runs already read are kept. A request to stop or start it again that
changes nothing is answered and not audited, and does not replace who made the
first decision. Rechecking it, and making or ending a waiver on it, are refused:
there are no findings to read again or call acceptable.

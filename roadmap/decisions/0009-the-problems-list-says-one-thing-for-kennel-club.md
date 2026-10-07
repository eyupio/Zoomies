# 0009: The problems list says one thing for Kennel Club

**Status**: accepted. Taken on 7 October 2026 on the owner's instruction, on
seeing three repositories in the drawer at once.

## Context

`kennel.exposure` was raised once for every repository with an open error among its
exposure findings. Three exposed repositories made three entries, each naming its
repository, with the worst finding's title and "1 other exposure error is open".

That was wrong in two ways. The drawer repeated what Kennel Club shows, the
findings, their evidence and the waive button, and grew with the fleet. And the
title was misleading. A public repository running jobs on the fleet is only a
warning; it becomes an error when a second finding is open beside it, a weak pool
or code from a fork that ran. The entry led with the first, now raised to an error,
and counted the cause as "another", so it read as if running a public repository
on your own runners were the fault.

## Decision

One problem for the fleet, whatever the number of repositories. Its code stays
`kennel.exposure`; its target kind is `kennel` and it has no ID. The title is the
count ("Kennel Club: 3 repositories have an exposure error open"), the detail says
what the two causes are, and nothing in it names a repository. The link opens
Kennel Club's repositories narrowed to those with an error, which is where each is
named and where a finding is waived with a reason.

`kennel.unavailable` is unchanged: it is about an installation, there are few, and
the reason differs for each.

## Consequences

The drawer dismisses and snoozes by code and target. With one target, one
dismissal covers every exposed repository until the errors have all cleared, and a
repository that becomes exposed while the entry is dismissed does not announce
itself again. Kennel Club's Overview counts them regardless, and a waiver stays the
decision about one repository; what is given up is muting a single one from the
drawer.

The link filters on `severity=error`, which matches the count today because the
exposure checks are the only ones that can be an error. If another area gains an
error check, the entry must be narrowed by area as well, or the list will show more
repositories than the title counts.

The wording of the finding itself, a public repository running jobs on the fleet,
is not changed here. It is Kennel Club's page that explains why it is an error, and
that wording is a separate question.

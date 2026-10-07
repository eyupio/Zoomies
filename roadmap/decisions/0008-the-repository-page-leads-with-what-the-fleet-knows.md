# 0008: The repository page leads with what the fleet knows

**Status**: accepted. Taken on 7 October 2026 on the owner's instruction, in
answer to which per-repository runtime data the page should show.

## Context

The repository page shows what Kennel Club concluded: findings, how far each
source could be read, and waivers. It shows nothing the fleet knows about the
repository itself, although the fleet knows a great deal, and the owner expected
the page to carry it.

Most of it is already behind documented routes:

* `GET /jobs/stats?repo=` answers with counts of jobs and how they ended, and
  queue wait and run duration at the median and the 95th percentile. It groups by
  pool or host, so it also says which pools and hosts ran the repository's jobs.
* `GET /jobs?repo=&unmatched=true` lists the jobs waiting right now for a label no
  pool serves.
* `GET /usage?group_by=repository` has history and peak concurrency, but no cost
  for a repository, and it reads every managed job in the window.

Some of it is not there. Runner minutes per repository need a new query (the
session table has no repository column). Nothing aggregates how much of a
repository's CPU time was throttled. The history of unserved-label events is held
only inside the Kennel facts. Prometheus has no repository label, and adding one
is a cardinality decision of its own.

## Decision

Add an **Overview** tab to the repository page and make it the default. It shows,
for the last 7 days and the last 30, the jobs the fleet ran for the repository
(how many, and how they ended), their queue wait and run duration at the median
and the 95th percentile, the pools and hosts that ran them with links, and the
jobs waiting now for a label no pool serves. Hosted jobs are left out. It is built
from the existing routes above, with no new store query, no schema change and no
new route.

## Consequences

The window is as long as jobs are kept: 30 days by default, so the oldest edge of
the 30-day window is partial, and the page says it covers what the fleet still
holds.

A job records its repository by name and Kennel's row is keyed by GitHub's ID, so
after a rename the numbers count only under the current name. The page does not
pretend otherwise.

The page never shows cost, because there is none per repository. Runner minutes,
the throttled share, a history chart and GitHub's own metadata about the
repository (default branch, fork, archived, language, last push) are each a later
decision; the last extends what Kennel stores, the first two need new queries and
an index review.

Untracked repositories (decision 0006) still have this tab, because none of it
comes from GitHub.

## As built

The tabs are the repository page's own sections, `Overview` and `CI`, and each
has an address: `/kennel/repositories/{id}` is the Overview and
`/kennel/repositories/{id}/ci` is what Kennel Club concluded. A link about
findings -- the Overview's list of repositories to open first, and a problem in
the drawer -- opens `/ci`; an unknown section is replaced by the Overview.

Four things in the routes were not what the first reading said:

* `GET /jobs/stats` names a job with no recorded pool or host `unknown`, in words,
  rather than leaving the group empty. The page shows that row as *Not recorded*
  and links it to nothing, because `unknown` is not an ID.
* A success rate is succeeded over succeeded plus failed. A cancelled or skipped job
  has no verdict, and a repository whose jobs all had none shows a dash, where one
  that has only failures shows 0%.
* The waiting jobs are `GET /jobs?repo=&unmatched=true`, and the link beside them
  opens the queue with the same two filters. The queue's `q` is a free-text search,
  so it names the repository with `repo`.
* The page reloads itself on its Refresh button, on `resync` and when the stream
  comes back, and hands the Overview one number that moves when it does. The
  Overview listens to nothing of its own: it did at first, and every Refresh asked
  the jobs API twice.

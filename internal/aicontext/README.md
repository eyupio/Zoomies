# AI context foundations

This package validates and stores commit-pinned repository context. It is
independent of the fleet scheduler, GitHub credentials and the HTTP transport.
Repomix generation and the managed setup workflow will produce this contract.

`RepositoryKey` includes GitHub host, Zoomies installation ID and GitHub
repository ID. Names are display metadata. `Snapshot.Match` checks that identity,
source branch, commit and configuration hash against trusted ingestion inputs;
byte hashes alone do not establish publication authority.

`Decode` bounds encoded JSON and verifies file paths, text sizes and content
hashes.
A snapshot may also list files it does not carry (`omitted`: `too_large`,
`over_budget` or `flagged`) instead of the generator refusing the whole run. Such
an entry has a path, a size in Git and a reason, and is held to the same standard
as carried content: `Validate` checks the path and that the reason fits the size,
`CheckSnapshotFiles` repeats the exclusions, and ingestion checks the trusted
tree for a regular file of exactly that size. Readers see the omission in the
overview, in a read of that path and in `omitted_total` on every page, so a
partial context never reads as a complete one. A snapshot with nothing omitted
encodes exactly as before. Generator secret scanning is still required: credential-path exclusions
are defence in depth, not proof that source contains no secrets.

`DiskStorage` owns private, digest-addressed blobs through `os.Root`. Writes
publish atomically; callers must still implement total storage quotas, retention,
backup inclusion and selection of the current snapshot. It is not wired into the
controller yet, and a hash ID is not a download permission.

`Read` pages the original UTF-8 text with byte offsets, avoiding per-line JSON
overhead. The budget bounds source bytes, not the whole protocol response or
model tokens; the API must account for its envelope and escape expansion too.

Access configuration lives in `internal/store/queries_ai_context.go` and is
enforced through `internal/auth/ai_context.go`. A caller needs repository
membership; an MCP caller additionally needs explicit consent for that particular
connection. Existing fleet roles and connections receive no source access by
migration. Only a signed-in owner can choose repositories for their connection.

See root `handoff.md` for the implementation checkpoint and remaining work.

`PlanSetupFiles` previews owned configuration, assistant instructions and the
optional GitHub-rendered Markdown README badge. It preserves user text and line
endings, returns original blob SHAs for publication preconditions, and refuses
custom configuration or edited/ambiguous managed sections. Exact retries return
no changes. Callers must fetch regular files at one trusted commit, check live
write permissions and compare the base commit before publication. A blob SHA is
not proof that a GitHub contents response was a regular file rather than a
symlink. Workflow templates, setup PR persistence and verified enablement remain
separate work; this planner performs no repository writes or source grants.

`PlanManagedSetup` adds the pinned workflow and integrity-locked Repomix npm
files. Generation uses only bounded regular blobs from the trusted source
commit, runs no repository build/install scripts, suppresses source-bearing
logs, and preserves exact original bytes after checking Repomix's scan. The
publication job validates the artifact and moves only the owned generated branch
without force. GitHub Enterprise is refused explicitly because these artifact
actions need a separately verified Enterprise template.

Setup previews and PR creation live in `internal/controller/ai_context_setup.go`
and the admin-only `/ai-context/repositories/{id}/setup` endpoints. A durable
reviewed proposal freezes configuration before GitHub writes, leases one
publisher and reconciles a lost response against the same complete commit/PR.
Original file modes are preserved. Saved setup state and PR links appear on
bounded administrative pages. Both-mode verified ingestion and authorised compact REST/MCP retrieval are now
wired through the controller. Workflow repair/upgrade proposals, repository-only
transient reads, uploads and live assistant acceptance remain follow-on work.

`FilePage`, `ReadPage` and `SearchResult` bound the fully escaped JSON envelope.
Pages repeat immutable commit/snapshot identity once, preserve UTF-8 source byte
offsets and expose truncation explicitly. `ReadPage` accepts up to six unique
source files and shares the encoded budget across them; each excerpt can be
continued independently. The HTTP caller must pin continuation commits and
check explicit caller grants before and after live verification.

# AI context foundations

This package validates and stores commit-pinned repository context. It is
independent of the fleet scheduler, GitHub credentials and the HTTP transport.
Repomix generation and the managed setup workflow will produce this contract.

`RepositoryKey` includes GitHub host, Zoomies installation ID and GitHub
repository ID. Names are display metadata. `Snapshot.Match` checks that identity,
source branch, commit and configuration hash against trusted ingestion inputs;
byte hashes alone do not establish publication authority.

`Decode` bounds encoded JSON and verifies file paths, text sizes and content
hashes. Generator secret scanning is still required: credential-path exclusions
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

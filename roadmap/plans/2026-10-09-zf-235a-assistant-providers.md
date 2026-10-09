# ZF-235 first pull request: the assistant's providers (slices 7a and 7b) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the controller a model it can talk to, chosen and tested by an administrator on an Assistant settings page, with a fake provider the demo fleet ships with, the OpenAI-compatible, Anthropic and OpenAI adapters, sealed keys, and a dial rule that keeps a local model local.

**Architecture:** A new pure-ish package `internal/assistant` holds the provider interface (`Chat` streaming with tool calls, usage and cancellation; `Check`), the fake, the contract tests every adapter passes, and the dialer that enforces local-only mode by address class. Adapters live under `internal/assistant/provider/` and speak their APIs over hand-rolled `net/http`, as the Docker and Proxmox clients do. The store gains `assistant_providers` with the key sealed by the instance key through its own setter; the API gains `/assistant/providers` for administrators; the UI gains an Assistant page of provider cards in the Controller group. The panel, tools, redaction, limits and the model-drafted work are later slices.

**Tech Stack:** Go 1.27 standard library (`net/http`, `encoding/json`, `bufio` for SSE), SQLite migration, `api/openapi.yaml` with both generated clients, Svelte 5 under `web/`, Node's test runner for UI units.

**Spec:** `roadmap/agent-readiness.md` section 9 (9.1, 9.2, 9.4, 9.6's local-only sentence, 9.11 rows 7a and 7b). `ROADMAP.md` ZF-235; gate row 17.

## Global Constraints

- British spelling in prose; no em dash anywhere (`internal/docs/no_em_dash_test.go`); comments say why; commit messages are imperative sentences with no prefix; no model identifiers in anything pushed.
- No new Go module or npm package: the adapters are hand-rolled `net/http` (CLAUDE.md, Dependencies). `go mod tidy` leaves `go.mod` and `go.sum` unchanged.
- "Nothing else may import a provider package but `internal/assistant`" (9.2): a test enforces it.
- The store is the only place SQL is written; `internal/api` is transport only; every `*.updated` event is a `GET` shape (no event in this slice).
- The key is never in a response: `key_configured` says whether one is sealed, as `credentials_configured` does for a provider.
- `api/openapi.yaml` is edited first; `go run internal/api/gen_openapi.go` and `make openapi` regenerate the two clients; the spec test in `internal/api/api_test.go` holds every route to an `x-zoomies-role`.
- The app shell stays under 200 KB gzipped; the Assistant panel is loaded as the other settings panels are.
- Before every push: `make build-nogui`, `make fmt`, `make lint`, `go test ./internal/assistant/... ./internal/store ./internal/api ./internal/config ./internal/controller ./internal/docs`, `cd web && npm run test:unit`, `python3 -m mkdocs build --strict`.

## Decisions the spec left open

- **Admin only, no step-up yet.** 9.9's step-up is slice 7h. Until it lands these routes need `auth.RoleAdmin` through two new actions, `assistant.read` and `assistant.write`, and the settings page is listed with a lock for everyone else.
- **The egress rule is one platform setting**, `assistant.allow_private_provider` (`ZOOMIES_ASSISTANT_ALLOW_PRIVATE_PROVIDER`, bool, off). On, a provider's base URL may name this machine or a private network and nothing else may; `security.allow_private_egress` is not consulted for providers, so a local model never needs the blanket switch. Off, saving such a URL is refused as `egress.private_target` with a fix naming the assistant switch. `docs/security.md` section 6 gets the toggle's cost.
- **Local-only mode is `assistant.local_only`** (`ZOOMIES_ASSISTANT_LOCAL_ONLY`, bool, off), the switch 9.4 lists below the cards. On, the dialer refuses any connection whose resolved address is public, after resolution, so a hostname that resolves to a public address is refused even when its name looks private. Off, the dialer dials what the URL says. The two settings are independent: local-only is about where traffic may go, the egress switch is about whether a private address may be saved.
- **Check is `Check` plus a one-token completion** (9.3 step 2), run by `POST /assistant/providers/{id}/check` and stored on the row as `last_check` JSON: `ok`, `model`, `latency_ms`, `usage_reported`, `error`, `checked_at`. The settings page's Test calls it; a draft is checked through `POST /assistant/providers/check` with the form's values, so a key need not be saved to be tested.
- **The fake provider** answers every chat with a fixed sentence that quotes the last user message, reports usage, honours cancellation, and supports one canned tool call when a tool named `echo` is offered, which is what later slices' tests need. It is kind `fake`, hidden from the kinds a person can create unless the demo seeded it.
- **The demo** seeds one provider, "Demo model (built in)", kind `fake`, default, enabled, with a passed check, and `announceDemo` says the assistant answers from a built-in model that talks to nothing.
- **Chat is streamed** by every adapter; a non-streaming request is the same call with the stream read to its end. One request shape, one response shape, so later slices never branch on the adapter.

## Review Focus

1. A provider whose base URL has a path (`https://host/v1`, `https://host/openai/`) must be dialled at the path the operator gave with the API's own suffix appended once; a doubled or dropped `/v1` is the commonest OpenAI-compatible misconfiguration. Pinned in Task 4 by a test per shape.
2. A key must never reach a log, an error message or a response: a check that fails with a 401 reports "the provider refused the key" and not the request. Pinned in Task 4 (adapter errors) and Task 6 (the view).
3. Local-only mode on, a base URL naming a hostname that resolves to a public address: the dial is refused after resolution, not admitted on the name. Pinned in Task 2.
4. A stream that ends without a terminal event (connection cut, server crash): `Chat` returns the error on the stream, not a hang, and cancellation through `ctx` ends the read within the request's lifetime. Pinned in Task 3 (contract) and run against every adapter in Task 4.
5. A second provider set as default makes the first not default in the same write: at most one default row exists after any sequence of writes, and deleting the default leaves none rather than an arbitrary one. Pinned in Task 5.

---

### Task 1: the provider interface, the fake, and the contract tests

**Files:**
- Create: `internal/assistant/provider.go` (the types and the interface), `internal/assistant/fake.go`, `internal/assistant/contract.go` (`RunContractTests`), `internal/assistant/fake_test.go`, `internal/assistant/purity_test.go`

**Interfaces:**
- Produces:
  ```go
  package assistant
  type Role string // "system", "user", "assistant", "tool"
  type Message struct { Role Role; Content string; ToolCalls []ToolCall; ToolCallID string }
  type Tool struct { Name, Description string; Parameters json.RawMessage }
  type ToolCall struct { ID, Name string; Arguments json.RawMessage }
  type Request struct { Model string; System string; Messages []Message; Tools []Tool; MaxTokens int }
  type Usage struct { InputTokens, OutputTokens int; Reported bool }
  type Event struct { Delta string; ToolCall *ToolCall; Usage *Usage; Done bool; Err error }
  type Stream interface { Next(ctx context.Context) (Event, bool) ; Close() error }
  type CheckResult struct { Model string; Latency time.Duration; UsageReported bool }
  type Provider interface {
      Chat(ctx context.Context, req Request) (Stream, error)
      Check(ctx context.Context) (CheckResult, error)
  }
  func NewFake(opts FakeOptions) *Fake   // FakeOptions{Model string; Reply func(last string) string; Fail error}
  func RunContractTests(t *testing.T, name string, open func(t *testing.T) Provider)
  ```
- Consumes: nothing.

- [ ] **Step 1: Write the failing tests** in `fake_test.go`: `TestTheFakeAnswersFromTheLastUserMessage` (a two-message request streams deltas that join to `Reply(last)`, then one `Usage` with `Reported: true`, then `Done`), `TestTheFakeCallsTheEchoToolWhenItIsOffered` (a request with a tool named `echo` yields one `ToolCall{Name: "echo"}` whose arguments are `{"text": <last user message>}`), `TestTheFakeStopsWhenTheContextIsCancelled` (cancel between two `Next` calls; the next event carries `context.Canceled`), `TestTheFakePassesTheContract` (calls `RunContractTests(t, "fake", ...)`). In `purity_test.go`: `TestTheAssistantPackageImportsOnlyWhatItMayImport` (every non-test file of `internal/assistant` imports only the standard library) and `TestNothingButTheAssistantImportsAnAdapter` (no package outside `internal/assistant/...` imports `internal/assistant/provider/...`; walk `internal` and `cmd` with `go/parser`, as `internal/kennel/offline`'s purity test does).
- [ ] **Step 2: Run** `go test ./internal/assistant/ -run 'Fake|Imports'`; expected FAIL: undefined `NewFake`, `RunContractTests`.
- [ ] **Step 3: Write `provider.go`, `fake.go`, `contract.go`.** `RunContractTests` runs, as subtests: a chat streams at least one delta and ends with `Done`; usage, when reported, arrives before `Done`; a cancelled context ends the stream with `context.Canceled` and `Next` returns `false` afterwards; `Close` after `Done` returns nil; `Check` returns a non-empty `Model`; a tool offered and a prompt of exactly `Call the echo tool with "ping"` yields a `ToolCall` named `echo` (the fake's canned behaviour; a real adapter's contract run uses a fake server that answers the same way, Task 4).
- [ ] **Step 4: Run** `go test ./internal/assistant/`; expected PASS.
- [ ] **Step 5: Commit** `Define what a model provider is to the assistant, with a fake that obeys it`.

### Task 2: the dialer and the two settings

**Files:**
- Create: `internal/assistant/dial.go`, `internal/assistant/dial_test.go`
- Modify: `internal/config/config.go` (an `Assistant` struct on `Config`: `AllowPrivateProvider bool yaml:"allow_private_provider"`, `LocalOnly bool yaml:"local_only"`), `internal/config/settings.go` (two registry rows, `Scope: ScopePlatform`, `Live: true`, keys `assistant.allow_private_provider` and `assistant.local_only`), `internal/config/egress.go` (`CheckProviderURL(raw string, allowPrivate bool) *Finding`: as `CheckOutboundURL` with the fix naming `assistant.allow_private_provider`), `internal/config/egress_test.go`, `internal/config/settings_test.go` if a count or table there enumerates keys

**Interfaces:**
- Produces: `func NewDialer(localOnly bool) func(ctx context.Context, network, address string) (net.Conn, error)`; `func IsLocalAddress(ip netip.Addr) bool` (loopback, link-local, RFC 1918, ULA, and the ranges `egress.go` names; exported so a test and a view can say "local"); `config.CheckProviderURL`.
- Consumes: Task 1 nothing; the `config` package's `privateTarget` is unexported, so `IsLocalAddress` is the assistant's own table over `netip` predicates and the same reserved prefixes, kept in one place with a comment saying which file it mirrors.

- [ ] **Step 1: Write the failing tests.** `dial_test.go`: `TestLocalOnlyRefusesAnAddressThatResolvesToAPublicAddress` (a custom `net.Resolver` is not needed: dial `"93.184.216.34:443"` as a literal and `"public.example:443"` through a resolver stub injected by a package-level `lookupIP` variable that returns `93.184.216.34`; both refused with an error containing `local-only`); `TestLocalOnlyDialsLoopbackAndPrivate` (an `httptest.Server` on 127.0.0.1 is reached); `TestWithoutLocalOnlyTheDialerDialsWhatItIsGiven` (the stub resolver is consulted and the public address passes to `net.Dialer`, asserted by a refused connect to a closed port rather than a local-only error). `egress_test.go`: `TestCheckProviderURLNamesTheAssistantSwitch` (loopback refused with `Fix` containing `assistant.allow_private_provider`; allowed with the switch; a public URL passes either way).
- [ ] **Step 2: Run** `go test ./internal/assistant/ ./internal/config/ -run 'LocalOnly|ProviderURL|Dialer'`; expected FAIL.
- [ ] **Step 3: Implement** `dial.go` (resolve with `lookupIP`, refuse when any resolved address is not local, then dial the chosen address with a `net.Dialer` whose timeout is 10 seconds) and the config rows. The registry summaries follow the voice of the neighbouring rows and say what each switch costs.
- [ ] **Step 4: Run** the two packages' tests; expected PASS. Run `go test ./internal/config/`; expected PASS (the registry's own tests cover the new rows).
- [ ] **Step 5: Commit** `Keep a local model local: the assistant's dialer and its two switches`.

### Task 3: the SSE reader and the stream shared by the adapters

**Files:**
- Create: `internal/assistant/provider/sse.go` (`ReadEvents(r io.Reader) iter.Seq2[ServerEvent, error]` where `ServerEvent{Name, Data string}`), `internal/assistant/provider/sse_test.go`, `internal/assistant/provider/stream.go` (`NewStream(resp *http.Response, decode func(ServerEvent) ([]assistant.Event, error)) assistant.Stream`), `internal/assistant/provider/stream_test.go`, `internal/assistant/provider/errors.go` (`StatusError(resp *http.Response) error`: 401 and 403 become "the provider refused the key"; 404 "no model named X" when the body names it; 429 "rate limited"; others the status and the body's `error.message` where present; never the request)

**Interfaces:**
- Produces: the three above, package `provider` under `internal/assistant/provider/`.
- Consumes: `assistant.Event`, `assistant.Stream` from Task 1.

- [ ] **Step 1: Write the failing tests.** `sse_test.go`: `TestReadEventsJoinsMultiLineDataAndSkipsComments`, `TestReadEventsStopsAtEOFWithTheUnterminatedEvent` (a final event without a blank line is still yielded). `stream_test.go`: `TestAStreamCutBeforeItsTerminalEventReturnsAnError` (a server that writes two deltas then closes; the third `Next` yields `Err` not nil and not `Done`), `TestCancellingTheContextEndsTheStream` (a server that never finishes; cancel; `Next` returns within a second with `context.Canceled`), `TestCloseDrainsNothingAndReleasesTheBody`. `errors_test.go`: `TestStatusErrorsNameTheCauseAndNotTheRequest` (a table over 401, 404, 429, 500 with a body carrying `Authorization` text that must not appear in the error).
- [ ] **Step 2: Run** `go test ./internal/assistant/provider/`; expected FAIL.
- [ ] **Step 3: Implement** the three files. `ReadEvents` uses `bufio.Scanner` with a 1 MB line limit; `NewStream` reads events in the caller's goroutine on `Next`, so cancellation is `resp.Body.Close()` triggered by `context.AfterFunc`.
- [ ] **Step 4: Run** the package tests; expected PASS.
- [ ] **Step 5: Commit** `Read a server-sent event stream the way every model API writes one`.

### Task 4: the OpenAI-compatible, Anthropic and OpenAI adapters

**Files:**
- Create: `internal/assistant/provider/openai.go` (`NewOpenAICompatible(cfg Config) assistant.Provider`; `Config{BaseURL, APIKey, Model string; Client *http.Client}`), `internal/assistant/provider/openai_test.go`, `internal/assistant/provider/anthropic.go` (`NewAnthropic(cfg Config) assistant.Provider`), `internal/assistant/provider/anthropic_test.go`, `internal/assistant/provider/fakeserver_test.go` (one fake server per wire protocol, answering the contract's prompts, used by both adapters' `RunContractTests` runs), `internal/assistant/open.go` (`Kind` string constants `KindFake`, `KindOpenAICompatible`, `KindAnthropic`, `KindOpenAI`, and `Open`, the one constructor the controller calls; `internal/assistant` is the one package 9.2 lets import the adapters), `internal/assistant/open_test.go`

**Interfaces:**
- Produces: `assistant.Open(kind Kind, cfg OpenConfig) (Provider, error)` with `OpenConfig{BaseURL, APIKey, Model string; LocalOnly bool}`; it builds the `http.Client` with `NewDialer(LocalOnly)` and a 60 second overall timeout for `Check`, none for `Chat` (the context bounds it).
- Consumes: Tasks 1 to 3.

- [ ] **Step 1: Write the failing tests.** `openai_test.go`: `TestOpenAICompatibleAppendsChatCompletionsOnceWhateverThePathGiven` (a table: `http://h`, `http://h/`, `http://h/v1`, `http://h/v1/`, `http://h/openai` each produce exactly one request to `<base>/chat/completions` where `<base>` is the URL given without a trailing slash, and `http://h` gets `/v1` added because that is the convention the local servers share); `TestOpenAICompatibleStreamsDeltasToolCallsAndUsage` (a fake server sends `delta.content`, then `delta.tool_calls` fragments across two chunks that join to one call, then a final chunk with `usage`, then `[DONE]`); `TestOpenAICompatibleSendsTheKeyOnlyWhenItHasOne` (no `Authorization` header without a key, `Bearer` with); `TestOpenAICompatibleCheckListsModelsThenCompletesOneToken` (`GET /models` then one `POST /chat/completions` with `max_tokens: 1`; the result's `Model` is the configured model, `UsageReported` follows the response); `TestOpenAICompatiblePassesTheContract`. `anthropic_test.go`: `TestAnthropicSendsTheMessagesRequestWithItsHeaders` (`x-api-key`, `anthropic-version: 2023-06-01`, `system` top-level, `tools` with `input_schema`, `stream: true`), `TestAnthropicStreamsContentBlockEventsIntoDeltasToolCallsAndUsage` (the events in the reference: `message_start` carries input usage, `content_block_start` of type `tool_use` with `content_block_delta` of `input_json_delta` fragments joining to arguments, `message_delta` carries output usage and `stop_reason`, `message_stop` is `Done`), `TestAnthropicPassesTheContract`. `open_test.go` in `internal/assistant`: `TestOpenRefusesAnUnknownKindAndNamesTheOnesItKnows`, `TestOpenGivesTheOpenAIKindTheHostedBaseURL` (`KindOpenAI` with an empty base URL dials `https://api.openai.com/v1`; `KindAnthropic` with an empty base URL dials `https://api.anthropic.com`).
- [ ] **Step 2: Run** `go test ./internal/assistant/...`; expected FAIL.
- [ ] **Step 3: Implement** the adapters and `Open`. Request bodies are the two APIs' documented shapes: OpenAI-compatible `{"model","messages","tools":[{"type":"function","function":{...}}],"stream":true,"stream_options":{"include_usage":true},"max_tokens"}`; Anthropic `{"model","max_tokens","system","messages","tools":[{"name","description","input_schema"}],"stream":true}` with tool results as `tool_result` content blocks. `Check` for Anthropic is one `POST /v1/messages` with `max_tokens: 1` (there is no list-models route worth a round trip). Errors go through `StatusError`.
- [ ] **Step 4: Run** `go test ./internal/assistant/...`; expected PASS.
- [ ] **Step 5: Commit** `Talk to an OpenAI-compatible server, Anthropic and OpenAI through one adapter interface`.

### Task 5: the store: `assistant_providers`

**Files:**
- Create: `internal/store/migrations/0089_assistant_providers.sql`, `internal/store/queries_assistant_providers.go`, `internal/store/assistant_providers_test.go`
- Modify: `internal/store/ids.go` (`PrefixAssistantProvider = "asp"`), `internal/store/CLAUDE.md`'s sentinel list only if a new sentinel is added (none is)

**Interfaces:**
- Produces:
  ```go
  type AssistantProvider struct {
      ID, Name, Kind, BaseURL, Model string
      KeyEnc []byte            // sealed; never rendered
      Enabled, IsDefault bool
      LastCheck []byte         // JSON as the API stores it, nil when never checked
      LastCheckedAt *time.Time
      CreatedAt, UpdatedAt time.Time
  }
  func (s *Store) ListAssistantProviders(ctx) ([]AssistantProvider, error)   // name order
  func (s *Store) GetAssistantProvider(ctx, id) (*AssistantProvider, error)   // ErrNotFound
  func (s *Store) CreateAssistantProvider(ctx, p *AssistantProvider) error    // ErrConflict on a name in use
  func (s *Store) UpdateAssistantProvider(ctx, p *AssistantProvider) error    // name, kind, base URL, model, enabled; not the key, not default
  func (s *Store) SetAssistantProviderKey(ctx, id string, enc []byte) error   // its own writer, as SetProviderCredentials is
  func (s *Store) SetDefaultAssistantProvider(ctx, id string) error           // one UPDATE clears the rest and sets this one, in the writer's transaction
  func (s *Store) SetAssistantProviderCheck(ctx, id string, result []byte, at time.Time) error
  func (s *Store) DeleteAssistantProvider(ctx, id string) error
  ```
- Consumes: nothing from earlier tasks.

- [ ] **Step 1: Write the failing tests.** `TestAssistantProvidersRoundTripWithTheKeySealedApart`, `TestANameInUseIsAConflict`, `TestSettingADefaultClearsTheOldOne` (Review Focus 5: two rows, set each default in turn, count `IsDefault` rows is one each time), `TestDeletingTheDefaultLeavesNoDefault`, `TestUpdateLeavesTheKeyAndTheDefaultAlone`.
- [ ] **Step 2: Run** `go test ./internal/store/ -run AssistantProvider`; expected FAIL (no table).
- [ ] **Step 3: Write** the migration (`id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, base_url TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', key_enc BLOB, enabled INTEGER NOT NULL DEFAULT 1, is_default INTEGER NOT NULL DEFAULT 0, last_check TEXT, last_checked_at INTEGER, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL`, plus a partial unique index on `is_default WHERE is_default = 1`) and the queries, through `s.exec` and the reader as the provider queries do.
- [ ] **Step 4: Run** `go test ./internal/store/`; expected PASS.
- [ ] **Step 5: Commit** `Keep the assistant's providers in the database, with the key sealed apart`.

### Task 6: the API: `/assistant/providers`

**Files:**
- Modify: `api/openapi.yaml` (tag `assistant`; schemas `AssistantProvider`, `AssistantProviderInput`, `AssistantProviderCheck`, `AssistantProviderKinds`; routes `GET,POST /assistant/providers`, `POST /assistant/providers/check` (a draft), `GET /assistant/providers/kinds`, `GET,PATCH,DELETE /assistant/providers/{id}`, `POST /assistant/providers/{id}/check`, `POST /assistant/providers/{id}/default`; all `x-zoomies-role: admin` but `GET /assistant/providers/kinds`, which is `viewer` so the rail can say what exists), `internal/api/openapi_spec.go` (generated), `web/src/lib/api/schema.d.ts` (generated), `internal/auth/rbac.go` (`ActionAssistantRead`, `ActionAssistantWrite`, both `store.RoleAdmin`), `internal/api/router.go`, `internal/controller/views.go` (`AssistantProviderView`), `internal/controller/assistant.go` (`func (c *Controller) OpenAssistantProvider(ctx, p *store.AssistantProvider) (assistant.Provider, error)`: opens the key with `c.key`, calls `assistant.Open` with the live `LocalOnly`; `func (c *Controller) CheckAssistantProvider(ctx, p) (AssistantProviderCheck, error)`: `Check` then a one-token chat, timed), `docs/api-surface.md` (the new routes in its table)
- Create: `internal/api/handlers_assistant.go`, `internal/api/handlers_assistant_test.go`, `internal/controller/assistant_test.go`

**Interfaces:**
- Produces: the routes above; `controller.AssistantProviderView{ID, Name, Kind, BaseURL, Model string; KeyConfigured, Enabled, IsDefault, Local bool; LastCheck *AssistantProviderCheck; CreatedAt, UpdatedAt time.Time}` with JSON names in snake case; `AssistantProviderCheck{OK bool; Model string; LatencyMS int64; UsageReported bool; Error string; CheckedAt time.Time}`.
- Consumes: Tasks 1, 2, 4, 5.

- [ ] **Step 1: Write the failing tests.** `handlers_assistant_test.go`: `TestAssistantProvidersAreAdminOnly` (viewer and operator get 403 on list and create; the kinds route answers a viewer), `TestCreatingAProviderSealsTheKeyAndNeverReturnsIt` (the response has `key_configured: true` and no field whose value is the key; the row's `KeyEnc` opens to it), `TestAPrivateBaseURLIsRefusedUntilTheAssistantSwitchIsOn` (422 with code `egress.private_target` and the fix naming `assistant.allow_private_provider`; 201 with the switch set through the harness's config option), `TestPatchingWithoutAKeyKeepsTheOldOne`, `TestCheckingADraftUsesTheFormsKeyAndStoresNothing` (against a fake server through `provider.Config`? No: the handler goes through `assistant.Open` with the draft's base URL pointing at an `httptest.Server` speaking the OpenAI-compatible protocol from Task 4's fake server, which `internal/api` tests may import from a test-only helper exported by `internal/assistant/assistanttest`), `TestCheckingAProviderRecordsTheResultOnTheRow` (`last_check.ok`, `model`, `latency_ms`, `usage_reported`), `TestACheckThatFailsNamesTheCauseAndNotTheKey` (Review Focus 2: the fake server answers 401; `last_check.error` is "the provider refused the key"), `TestSettingTheDefaultIsAudited` (an audit row with action `assistant.provider.default`), `TestDeletingAProviderNeedsItsNameInTheBody` (the typed-name pattern: `DELETE` with `{"name": "..."}`; a wrong name is 409, as the provider delete does). `controller/assistant_test.go`: `TestOpeningAProviderHonoursLocalOnly`.
- [ ] **Step 2: Run** `go test ./internal/api/ -run Assistant ./internal/controller/ -run Assistant`; expected FAIL.
- [ ] **Step 3: Edit the spec, regenerate** (`go run internal/api/gen_openapi.go` from the root, `make openapi`), **write the handlers, the actions, the routes, the view and the controller seams.** The create and patch handlers validate: name 1 to 80 characters, kind one of the kinds a person may create (`fake` only when `ZOOMIES_SEED_DEMO` seeded one, read from the config), base URL an `http` or `https` URL for `openai_compatible` and optional for the hosted kinds, model non-empty, and `CheckProviderURL` with the live switch. Audit actions: `assistant.provider.create`, `.update`, `.delete`, `.check`, `.default`, `.key` (a key set or replaced).
- [ ] **Step 4: Run** `go test ./internal/api/ ./internal/controller/ ./internal/auth/ ./internal/docs/`; expected PASS (the spec test holds every route to a role, `docs/api-surface.md`'s test holds the table to the spec).
- [ ] **Step 5: Commit** `Let an administrator configure, test and choose the assistant's providers over the API`.

### Task 7: the Assistant settings page

**Files:**
- Create: `web/src/lib/settings/AssistantPanel.svelte` (the page: cards, the add form, the two switches read and written through the existing settings client), `web/src/lib/settings/AssistantProviderCard.svelte`, `web/src/lib/settings/AssistantProviderForm.svelte`, `web/src/lib/settings/assistant.ts` (the API calls, typed from `schema.d.ts`, and `checkSummary(check): string` that renders a check in one sentence), `web/unit/assistant-check-summary.test.ts`, `web/e2e/assistant-settings.spec.ts`
- Modify: `web/src/lib/settings/pages.ts` (page `assistant`, label "Assistant", Controller group, `needs: 'admin'`, icon `MessageSquare` from `@lucide/svelte`, description "Which model answers in the assistant, and what it may be told."), `web/src/routes/Settings.svelte` (mount the panel)

**Interfaces:**
- Consumes: Task 6's routes and shapes through `$lib/api/client`.
- Produces: nothing a later task in this plan consumes.

- [ ] **Step 1: Write the failing tests.** `assistant-check-summary.test.ts`: a passed check with usage reads `Answered as gpt-4o in 312 ms; usage reported`; one without usage ends `; usage not reported`; a failed one reads the error; never checked reads `Never tested`. `assistant-settings.spec.ts` (Playwright, against the demo-seeded binary as the kennel spec runs): the page lists the demo's built-in provider as default with its passed check; adding an OpenAI-compatible provider with a loopback URL shows the egress refusal with the switch's name in the form's error; the key field clears after save and the card says "set, never shown"; Remove asks for the name and refuses a wrong one.
- [ ] **Step 2: Run** `cd web && npm run test:unit`; expected FAIL on the new unit (the e2e runs in Step 4 against the built binary).
- [ ] **Step 3: Write the page.** Cards follow `ProviderCard.svelte`'s layout and badges; the form follows `BackupRemoteForm.svelte`; the delete goes through `ConfirmDialog` with the typed name; the key input has `autocomplete="off"` and is `type="password"`; status uses the information colour for "checked" and the warning colour for a failed check, never a status colour of a runner's; the lock for non-admins is the rail's own behaviour.
- [ ] **Step 4: Run** `cd web && npm run test:unit`, then `make build VERSION=dev` from the root and the Playwright spec with the local config (`web/playwright.local.config.ts`, deleted after); expected PASS. `make lint` for the UI lint.
- [ ] **Step 5: Commit** `Add the Assistant settings page, where an administrator chooses and tests the model`.

### Task 8: the demo, the docs and the record

**Files:**
- Modify: `internal/controller/seed.go` (one fake provider as the decisions say; the seeded `last_check` is a passed check), `internal/controller/seed_test.go`, `cmd/zoomies/demo.go` (`announceDemo` adds one line: the assistant answers from a built-in model that talks to nothing), `cmd/zoomies/demo_test.go` if the announcement is tested, `docs/security.md` (section 6: `assistant.allow_private_provider` and `assistant.local_only`, what each costs), `docs/settings.md` or wherever the settings reference lives (the two keys; find the page `internal/docs` holds the registry to), `docs/api-surface.md` (done in Task 6 if its test demanded it), `roadmap/progress.md` (ZF-235 row: `in_progress`, this pull request, slices 7a and 7b), `ROADMAP.md` (ZF-235: "Started, 9 October: 7a and 7b in #<n>"), `internal/docs/*_test.go` where a test enumerates settings pages or keys

- [ ] **Step 1: Write the failing tests.** `seed_test.go`: `TestTheDemoSeedsABuiltInAssistantProvider` (one row, kind `fake`, default, enabled, `LastCheck` with `ok: true`). Whichever `internal/docs` test holds the settings registry to its page fails until the page names the two keys; run it to see.
- [ ] **Step 2: Run** `go test ./internal/controller/ -run Demo ./internal/docs/`; expected FAIL.
- [ ] **Step 3: Write** the seed, the announcement line, the docs and the record.
- [ ] **Step 4: Run the whole gate** (Global Constraints), and `python3 -m mkdocs build --strict`; expected PASS.
- [ ] **Step 5: Commit** `Ship the demo with a built-in model, say what the assistant's switches cost, and record the first slice`; push; open the pull request as a draft with *How to verify*: `go test ./internal/assistant/... ./internal/api -run Assistant`, `zoomies demo` then Settings, Assistant.

---

## Self-review

- **Spec coverage.** 9.1's "reader of the same payloads" is the tool layer, slice 7d. 9.2: interface (Task 1), the three adapters and contract tests (Task 4), the import rule (Task 1's purity test), the scoped egress rule (Task 2 and 6). 9.3's first-run cards and probe: slice 7e, by decision. 9.4: the page (Task 7), the key write-only (Tasks 6 and 7), the local-only switch (Tasks 2 and 7); data classes and limits are 7c. 9.6's local-only sentence (Task 2, the resolution test). 9.11 rows 7a (interface, fake, demo, settings page, sealed credentials: Tasks 1, 5, 6, 7, 8) and 7b (adapters, local-only, egress rule: Tasks 2, 4, 6).
- **Types.** `assistant.Provider`, `Stream`, `Event`, `Request` are defined in Task 1 and used unchanged in 3, 4, 6. `OpenConfig` is Task 4's; the controller's seam in Task 6 builds it. The store's `AssistantProvider` is Task 5's; the view in Task 6 maps it.
- **Review Focus.** 1 in Task 4; 2 in Tasks 3 and 6; 3 in Task 2; 4 in Tasks 3 and 4; 5 in Task 5.
- **Proportion.** Eight tasks for two slices of an XL package; no task carries a body the tests do not determine.

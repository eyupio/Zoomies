# UI and conversion audit: the path to a first job

Audited at commit `0b4734c` on 1 October 2026, from the built binary (`make build`), a fresh
auth-on controller, the demo-seeded fleet, the repository's own fake GitHub, and the
`zoomies.sh` site built with `mkdocs build`. Desktop is 1440×900 at 1× and mobile is 375×812 at
2× with touch.

The first commit on this branch is the audit alone. The commits after it implement the
findings that need no product decision, and every finding below opens with a **Status** line
saying what became of it. The *Problem* text is left as it was written, describing the product
at `0b4734c`, so a reviewer can see what the change answers to.

**This branch was reconciled with main after #563 merged.** That pull request implemented most of
the same fixes from the other audit, ahead of this one, and the two disagreed in places. Where both
changed the same thing, **main's version stands** and this branch's was dropped; what is left here is
only what main lacks. A Status line says which it is: *on main (#563)* is somebody else's change that
this audit's finding is answered by, and *here* is a change this branch makes. What was left for a
decision, and why, is the list under *What is still yours to decide*.

## Relation to `UI_AUDIT.md`

**A second audit of the same commit sits beside this one.** [`UI_AUDIT.md`](UI_AUDIT.md) was written
separately, at the same commit, and merged first, so this file has a name of its own. The two were
written without sight of each other and **use the same finding IDs for different findings**: `C1`
here is the Connect GitHub dead end, and `C1` there is the checklist deleting itself. An ID in this
file means this file. Its fixes (#563) merged while this branch was open, and a third audit of the
same commit, [`FUNNEL_AUDIT.md`](FUNNEL_AUDIT.md), merged after it with its own fixes for the docs
site (#559); a Status line below that cites #559 is answered there. Where the first two overlap, this
is where each of the other file's findings stands now:

| `UI_AUDIT.md` | What it found | Where it stands |
| --- | --- | --- |
| C1 | The checklist deletes itself when any other job arrives | **Fixed on main (#563).** This branch found the same thing and fixed it the same way; its version was dropped. The regression test is main's (`connect.spec.ts`) |
| C2 | The sign-up form buries its own submit button | **Fixed for desktop on main (#563).** Here: the lede names the role the account gets (N2) |
| C3 | Connect GitHub refuses the home lab | **Fixed on main (#563):** the dialog offers *Create the App anyway*. This branch's inline address field was set aside for it (C1 here) |
| C4 | No first-job moment | **Done on main (#563)** (C2 and N12 here), less the prefilled GitHub link |
| C5 | Nobody can see the product work before the install marathon | **Done on main (#563):** `zoomies demo` and `install.sh --demo` (C4 here) |
| C6 | The landing page's CTA is clipped on a phone and the page scrolls sideways | **Fixed on main (#563, #559) for phones. Here:** the same scroll between 720 and 1366px, and the comparison table, which still ended 28px past a 375px screen (C3 here) |
| H1 | The Overview is a dashboard of zeros | **In part on main (#563):** dashes for the timings. The composition is left to decide (C6 here) |
| H2 | Creating the first pool takes five screens | **Copy only on main (#563).** Here: a controller with no connection gets a prompt instead of the wizard (H5 here) |
| H3 | The installer asks up to 18 questions | Not touched; one question moves earlier (H11 here) |
| H4, H5, H6 | Quick start order, landing hierarchy, hero | **Done on main (#559):** the Quick Start's order, the headline and the hero's buttons (H2 and H3 here) |
| H7 | "What is qualified" | Not touched: copy |
| H8, H9 | Phone text and tap sizes, lists below summaries | Not touched (H12 here, left to decide) |
| H10 | The sign-up asks for an email | **Fixed on main (#563)** (H11 here) |
| H11 | The Installations page opens with a warning and two identical buttons | **In part here** (H8 here): the warning waits for a connection, the two buttons remain |
| H12, H13 | Twelve sections from minute zero, nothing measures the funnel | Not touched |
| N1 | The events empty state blames a filter | **Fixed on main (#563)** |
| N2 | Duplicate "Other runners" switch, and a Refresh button | Not touched (the duplicate is left to decide, C6 here) |
| N3 | The Connect dialog clips its second tab and orphans a step | **In part on main (#565):** the tabs fit, with the same two labels this branch chose. The stepper still wraps |
| N4 | The Connect dialog front-loads advanced options | **In part here** (H6 here): they are behind a fold, the disabled "Continue" is as main has it |
| N5, N8, N11 | Mermaid CDN, expandable rows in a grid, bottom-bar docs | Not touched |
| N12 | The NEW pill is three lines on a phone | **Gone on main (#559):** the pill left the hero (N9 here) |
| N6, N7, N9 | Docs contrast, definition-list markup, an unnamed progress bar | **Fixed on main (#563)** |
| N10 | A dead band in the checklist's first row | **Fixed on main (#563)** (N10 here) |

## Read this first

**There is no paywall, trial, billing or upsell in this codebase, so there is nothing of that
kind to audit.** The brief assumes a SaaS funnel. Zoomies is AGPL-3.0 and self-hosted: no
accounts on a server of ours, no tiers, no pricing. I searched the web UI, the API, the Go
packages, the site and the docs for `paywall`, `free trial`, `pricing`, `billing`, `premium`,
`upsell`, `upgrade to`, `licence key`, `entitlement`, `plan limit`, `sponsor` and `donate`. The
only hits are GitHub's own pricing quoted on `docs/costs.md` and *software* upgrades
(`web/src/lib/state/upgrade.svelte.ts`, the agent-upgrade hints in `HostCard.svelte`). This is
policy, not an omission: `SUPPORT.md` L5 says "no paid tier", and `ROADMAP.md` L380 and L478–482
(delivery rule 15) say no page may name a plan, a tier or a hosted service. I have not invented
findings to fit the template, and none of my fixes asks for one; where a suggestion could brush
against that rule I say so (C4).

What "start a free trial" means here, and what I audited instead:

> **zoomies.sh → `curl … | sh` → first account → connect GitHub → first pool → first job on your own runner.**

The conversion event is the first job. The "free trial" equivalent is the zero-commitment demo
fleet (`ZOOMIES_SEED_DEMO=true`), which exists and is almost invisible (C4).

**Who walks which path.** Per `docs/quickstart.md` §2, a *native* (systemd) install finishes in
the terminal: administrator, GitHub App and first pool. The two *container* installs (Docker
Compose, which is "the default whenever you have a `compose` command", and single Docker) "move
the last three steps to the browser". So Bootstrap → the Overview checklist → Connect GitHub → the
pool wizard is the **default path for anyone with Docker**, and the fallback for a native install
whenever its owner skips GitHub in the terminal (C1). That is why most of this audit is about those
four screens.

### Your four specific checks

| Check | Answer |
| --- | --- |
| Upgrade or paywall modals before value | **None exist.** The checklist dismisses itself after the first job (`FirstRun.svelte` L142–145) and nothing is asked of the user afterwards. |
| Several simultaneous upsell touchpoints (nagware) | **None.** The nearest analogue is a first-run Overview that stacks competing prompts, some of which contradict each other (C6). |
| Broken or cramped mobile layouts | **Yes, in three places:** the landing page's install command and comparison table (C3), the Connect GitHub dialog (H6), and the first-run form (H1). The *app* is otherwise very clean: no sideways scroll on any of ~35 captured routes at 375px (one data-dependent exception, N3). |
| Onboarding answers collected but never used | **Yes, two:** the installer's four GitHub answers are discarded on "Skip" and asked again in the browser, and the first-run Email field feeds nothing (H11). |

### What is already good, so nobody breaks it

- The **Login page** (`Login.svelte`): black brand panel, a real value proposition, three facts, instance host chip. It is the best page in the product, and better than Bootstrap and the landing hero (see H1, H2).
- The **Connect dialog's "done" state** verifies the credentials before it claims success.
- The installer's startup banner and its boxed **setup-token block**; the controller *refusing* auth-off behind a public address; the safe defaults generally.
- The **populated Overview** is the product's strongest screen (activity matrix, sparkline tiles, plain-language scheduler reasons). The problem is that a new user cannot get there; they see the empty version (C6).
- Contrast is excellent almost everywhere. Across 15 app routes at both widths my opacity-aware measure found **one** failing pattern: the dimmed checklist steps, three text runs (H9). Focus rings, skip links, reduced motion and 16px mobile inputs are all handled.

### How to read the findings

Severity: **Critical** blocks conversion or misleads someone at the moment of decision. **High impact** costs real friction or trust. **Nice to have** is polish. Numbers are measured against the real binary unless marked *(from source)*. Line numbers are at `0b4734c`.

| ID | Finding | Funnel step | Viewport | Status |
| --- | --- | --- | --- | --- |
| C1 | Connect GitHub dead-ends on a path the installer itself offers | Activation | both | Fixed on main (#563); the inline address field was set aside |
| C2 | Nothing gets the user to a running job; the last step is the wrong tool | Activation | both | Fixed on main (#563); prefilled link not done |
| C3 | Landing page broken at 375px: install command and proof table | Landing | mobile | In part on main (#563, #559); here: the table and the laptop-width scroll |
| C4 | No way to try it without a server, root and a GitHub org | Landing | both | Fixed on main (#563, #559); recording and hosted demo left to decide |
| C5 | App asks for write on code and workflows before any value | Activation | both | Fixed here, **needs sign-off** |
| C6 | First-run Overview buries its one job and contradicts itself | First run | both | In part on main (#563); here: one primary, contrast. Composition left to decide |
| H1–H12 | See below | | | Fixed here: H4 (the measure and the tables), H6, H8, H9. In part here: H1, H5, H11. On main: H2 and most of H3 (#559), the rest of H1, H5's subtitle and H11's email (#563), the grid in H4 (#559). Left to decide: H7, H10, H12 |
| N1–N12 | See below | | | Fixed here: N1, N2, N3. In part here: N7. On main: N10, N12 (#563), N9 is moot (#559). Left to decide: N4, N5, N6, N8, N11 |

---

## 1. Critical

### C1 · The Connect GitHub dialog is a dead end on a path the installer itself offers

- **Status:** **Fixed on main (#563, its C3); this branch adds the deep link.** For a loopback address the dialog now says what polling costs, links the setting and the tunnel page, and offers *Create the App anyway* as an explicit tick, which settles the question this audit left open (the installer offers it, so the dialog should). **Here:** the checklist's button opens the dialog directly (`/installations?connect=1`), where it was a navigation to a page whose own buttons were the second and third click of the same decision. An earlier version of this branch put a field in the dialog that saved `server.external_url` and said how to restart; it was dropped for #563's, which keeps the setting on its own page, and it is in this branch's history before the merge with main. **What is left:** with no external URL at all (a default Compose deployment) the dialog still stops, because there is no webhook address to give GitHub, and the way out is a link to the setting and a restart the UI cannot perform. Whether the dialog should take the address itself, for an account with the Platform role, is a decision.
- **Pass:** First-time user
- **Where:** `web/src/lib/installations/ConnectDialog.svelte` L486–487 (`notReachable`), L855–881 (the amber panel), L1317–1325 (footer primary disabled). The promise it breaks is in `internal/installer/manifest.go` L333 (`"Skip GitHub for now -- connect it later in the browser"`). Reached from `FirstRun.svelte` L189 → `/installations`. Desktop 1440 and mobile 375 (at 375 the amber panel is about 40% of the visible dialog).
- **Problem:** A loopback listener, the installer's default, derives `server.external_url` as `http://localhost:8080` (`installer.go` L735–757; the dialog's own comment at L478–485 says the same). On that default the terminal installer notices, warns that GitHub cannot reach it, and offers three answers: enter a reachable address (the default), create the App anyway, or "connect it later in the browser". The same wall meets anyone who starts `zoomies controller` by hand with defaults. The browser then greets that person, on the single call to action of the first-run checklist, with: *"Set `server.external_url` to the address GitHub can reach, restart the controller, and come back."* The primary button is disabled. There is no field, no link to Settings, no hint how to restart, and the three unusable form fields are still drawn underneath. The fix lives on a different page and needs a restart the UI cannot perform. The terminal had three exits; the browser has none. A new operator is asked to leave the product, edit config, restart a service nobody named and find their way back, *before seeing a single runner*. It is the point where I would expect the most evaluations to end. (I have no analytics; that is a judgement, not a measurement.)
- **Fix:**
  1. In the blocked state replace the paragraph with an inline field, "Public address of this controller", prefilled with `location.origin` when that is not loopback (the browser knows the address the operator actually typed; `Login.svelte` L206–215 already argues it is "always right"). Primary button: "Save and continue", which writes `server.external_url` through the same call the Settings page makes (`ConfigurationPanel.svelte` L293–299, `save` → `updateSettings`).
  2. The setting is restart-bound (`internal/config/settings.go` L193–196), so after saving show the restart command for the detected deployment (`sudo systemctl restart zoomies`, or `docker compose restart zoomies`; this needs a new `deployment` field on `/meta`) and reuse `lib/settings/RestartWait.svelte` to poll `/healthz`, reload, and reopen the dialog on step 1.
  3. Give the dialog the installer's other two exits. "Create it anyway, I'll fix the webhook URL on GitHub" is a legitimate answer, not a dangerous one: without a reachable address the fleet still works by polling and "reacts in tens of seconds rather than instantly" (`docs/configuration.md#serverexternal_url`), so a hard block is stricter than the product needs. And link the guidance that already exists, which the dialog never mentions: the Cloudflare Tunnel pattern in `docs/home-lab.md` L36–46. The quickstart's step 3 does not mention the external URL at all; add one sentence there too.
  4. Surface the same state on the checklist: when `external_url` is loopback, the "Connect GitHub" row reads "First, set your public address" with a button to the same inline field, so the dead end is never one click deep. A deep link already exists (`ProblemItem.svelte` L145: `/settings/configuration?setting=…`) if you want a fast first step.

### C2 · Nothing gets the user to a running job, and the last checklist step points at the wrong tool

- **Status:** **Fixed on main (#563, its C4 and C1); nothing is added here.** Once a pool exists the checklist's last step, and the pool's own page, carry a complete `workflow_dispatch` file with the pool's label in it, a copy button and the steps; the first job of the fleet's own gives way to a summary of how long it waited and ran; the Quick start's §5 is a whole file; and the checklist counts this fleet's own jobs only, so a queued job or one on somebody else's runner no longer dismisses it. This branch had its own version of each, and they were dropped in favour of those. "Rewrite workflows" stays a secondary button beside the file, as #563 has it. **Not done, by either:** the "Open in GitHub" deep link (`/new/<branch>?filename=&value=`): GitHub's behaviour for it could not be confirmed from here, and a broken link at the first meaningful action is worse than none.
- **Pass:** First-time user
- **Where:** `web/src/lib/overview/FirstRun.svelte` L255–282 (step "Point a workflow at it", action "Rewrite workflows" → `/migrate`), `web/src/lib/migrate/MigrateWizard.svelte` (five steps). There is no "test", "smoke" or "dispatch" affordance anywhere in `web/src` (searched). The docs' version of this step is `docs/quickstart.md` §5. Desktop and mobile.
- **Problem:** The checklist's own header promises "…a job running on your own runner". Every step up to the last happens inside Zoomies; the last happens outside it: find a repo, edit a workflow, commit, push, return. The quickstart's sample for that step runs `make test`, which only works in a repository that already has a Makefile, so even the docs' "hello world" is not self-contained. In the product the only help is a `runs-on:` line and a button, and that button opens the **migration wizard**, whose five steps end in pull requests opened on the user's repositories. For an evaluator with no workflows to migrate, that is the wrong tool, and "open PRs across my org" is a much scarier ask than "run hello-world". **Measured** on the manual-App tab (the only path testable without github.com): **10 clicks and 4 required fields from the Overview to a created pool**, then nothing in-product that starts a job. On the manifest tab I count about 12–14 clicks and two round trips to github.com *(from source)*. The user finishes Zoomies' half and is handed the start of their own.
- **Fix:** Add a "Run a test job" affordance that needs no migration, shown once `hasPool`:
  1. Under the existing `runs-on` line, render a minimal copyable workflow (`workflow_dispatch`, one job with `runs-on: <the pool's runsOn()>` that prints `hostname`).
  2. Beside it, "Open in GitHub": `https://github.com/<target>/<repo>/new/<branch>?filename=.github/workflows/zoomies-test.yml&value=<urlencoded yaml>`. GitHub's new-file editor takes `filename` and `value`; verify against the current UI, and keep the YAML under the URL limit (≈8 KB; a minimal workflow is well under 0.5 KB). `<target>` is `installation.target` already; for an org target add a repository picker fed by the installation, or a free-text `owner/repo`. Use a self-contained sample (`echo`, `hostname`, `uname -a`) rather than `make test`, and fix the quickstart's §5 the same way.
  3. When the first `job.updated` for this fleet arrives, the existing `hasJobs` effect (L142–145) silently dismisses the panel. Make it a moment instead: one toast, "Your first job ran on your own runner in 14s", linking to that job, with the next step (add a second host, turn on elastic CPU). That is the only legitimate "post-value prompt" this product needs, and it is a next step, not an upsell.
  4. Demote "Rewrite workflows" to a quiet link: "Moving existing workflows? Use the migration wizard."

### C3 · The mobile landing page is broken in the two places that matter most: the install command and the proof table

- **Status:** **Fixed on main (#563 and #559) for the install command and for phones; here, the two things they left.** The audit blamed the comparison table for the layout viewport growing to 399px. Bisecting with injected CSS shows that hiding the table, the install box or both left `innerWidth` at 399; the cause was the hero's decorative glow (`inset: -3rem -2rem 30%`, 423px wide, 24px past the gutters), and #563 reached the same diagnosis. Its fix stops the glow at the page edge **below 720px only**, so with main's stylesheet alone the page still scrolled sideways by 24px at 768, 1024 and 1280px (1px at 1366). #559 put the Zoomies column first, which spares the one the table exists to show, but the table still ended 28px past a 375px screen and 83px past a 320px one, so its last column ("A few static runners") was cut off with nothing to say there was more. **Here:** the glow stops at the hero's sides at every width, and up to 480px the table scrolls inside its own box, so every column is there and the page does not move. Re-measured on the reconciled branch at nine widths from 320 to 1920px: no sideways scroll at any of them in either theme. The install command is main's (it wraps at a space, and #559 moves the copy button below the text as a 44px target); this branch's version of both was dropped. **The guard suggested under Fix is vacuous:** in a mobile-emulated context `scrollWidth <= innerWidth` reads 399 <= 399 at baseline and passes, so a site check should assert `innerWidth === 375`. Checked in Chromium only; Firefox and WebKit are not available here.
- **Pass:** Designer
- **Where:** `docs/index.md` L26–30 (hero command) and L290–303 (`.zoomies-compare`); `docs/stylesheets/zoomies.css` L897–935 (`.zoomies-install`), L1037–1044 (compare table), L1201–1232 (the only phone block; it touches neither). Viewport 375×812.
- **Problem:** *Measured.* (a) The command is 421px of monospace in a 343px box with 4.4em of right padding for the copy chip. First paint reads `curl -fsSL https://zoomies.sh/i` and then the copy button sits **on top of** the remaining characters. The CSS comment (L906–907) says the tail "scrolls clear of it"; nothing tells a visitor it scrolls. They cannot read the one line the page exists to get them to run. (b) The comparison table is 385px wide in a 343px column. The layout viewport grows to 399px (`window.innerWidth` 399 against a 375px visual viewport; the full-page capture is **798px wide, not 750**), so the page pans sideways or renders zoomed out, and **the Zoomies column, the one the table exists to sell, is the part cut off** (header and "one comman…" clipped at the right edge). *Correction: the table is cut off, but it is not what widened the layout viewport; see Status.* Everything else on the page measured clean, which makes these two stand out: they are the first thing and the best-persuading thing.
- **Fix:**
  1. Command: at `@media (max-width: 30em)` set `.md-typeset .zoomies-install pre > code { white-space: pre-wrap; overflow-wrap: anywhere; font-size: 0.7rem; padding-right: 1.1em }` and move the chip below the text as a full-width labelled button: `.md-typeset .zoomies-install .md-code__nav { position: static; transform: none; width: 100%; margin-top: .5rem }`. Two wrapped lines of command beat one clipped line.
  2. Table: `.md-typeset .zoomies-compare .md-typeset__table { display: block; max-width: 100%; overflow-x: auto }` as the safety net, and at ≤ 30em `.zoomies-compare table th, .zoomies-compare table td { padding: .5em .55em; font-size: .68rem }` and, because the Zoomies-against-ARC contrast is the point, hide the middle column on phones (`.zoomies-compare th:nth-child(3), .zoomies-compare td:nth-child(3) { display: none }`). Three columns fit 343px with room to spare (min-content is 385px with four; you need to save ≥ 42px).
  3. Add a guard to the site build: a Playwright check that `document.documentElement.scrollWidth <= window.innerWidth` for `/` at 375px (`web/tests/mobile.spec.ts` already does this for the app). *Correction: in a mobile-emulated context that check passes at baseline (399 <= 399); assert `innerWidth === 375` instead.*

### C4 · There is no way to try Zoomies without a server, root access and a GitHub organisation, and the demo that would fix it is buried

- **Status:** **Fixed on main (#563, #559).** `zoomies demo` runs a throwaway controller with the seeded fleet on `127.0.0.1`, deleted on Ctrl-C, and `install.sh --demo` runs it from a temporary directory with no sudo and no prompts; the hero now has a second button, "Try the demo — no GitHub needed", directly under the install command. An earlier version of this branch had a demo page with one pasted block and a test that held the block to the controller's own validation; it was dropped in favour of the command. **Not done:** the recorded walkthrough, and a **hosted** public demo, which delivery rule 15 (`ROADMAP.md`) rules out unless the owner changes it.
- **Pass:** First-time user
- **Where:** `docs/index.md` L26–34 (hero: only the install command and doc links), L239 (the *only* mention on the home page, mid-paragraph in "Moving what you have now"), L326–350 (closing CTA); `docs/quickstart.md` (no mention at all); `docs/ui.md` L20–22 and L581–583; seeded by `internal/controller/seed.go` L123–. Both viewports.
- **Problem:** To see anything real a visitor must: run an installer with root on a Linux box (it escalates with `sudo`), answer eight or more prompts, create a GitHub App on an organisation (with the permissions in C5), *then* push a workflow. The thing that answers "is the UI as good as the screenshot?" without any of that already exists: a seeded fleet of two pools, hosts, a dozen runners and a morning of jobs, **no GitHub required**. It is the very data behind the hero screenshot. It is mentioned in one sentence and offered nowhere. For a self-hosted tool, this *is* the free trial, and the funnel hides it.
- **Fix:**
  1. Hero: add a secondary button next to the command: **"Try the demo fleet (no GitHub needed)"** → new `docs/demo.md` (link it from `quickstart.md` step 1 and the closing CTA too).
  2. `docs/demo.md` is one copy-paste block. I ran this against the built binary and it works with no Docker present (it logs a "docker backend not available" warning and serves the seeded fleet; `ZOOMIES_EXTERNAL_URL` must stay unset, because auth-off with an external URL is refused by `config/validate.go` L595–602):
     ```sh
     curl -fsSL https://zoomies.sh/install.sh | sh -s -- --no-init
     ZOOMIES_SEED_DEMO=true ZOOMIES_DISABLE_AUTH=true ZOOMIES_BIND=127.0.0.1:8080 \
       ZOOMIES_ENCRYPTION_KEY=$(openssl rand -base64 32) ZOOMIES_DB_PATH=/tmp/zoomies-demo.db \
       zoomies controller
     ```
     Then "open http://localhost:8080". Add a test in `internal/docs` that parses the block and runs the variables it sets through `config.Validate` (the repo already holds the docs to the code this way), so a renamed setting or a new refusal breaks the build instead of the demo.
  3. Add a 60-second recorded walkthrough of the same fleet (MP4 or animated WebP, with the captions burned in) under the hero screenshot. I am deliberately **not** proposing a hosted public instance: delivery rule 15 (`ROADMAP.md` L478–482) says no page names a hosted service, so that is the owner's call, not an audit recommendation. A recording is inside the rule.

### C5 · The GitHub App asks for write access to repository contents and workflows before the user has seen any value

- **Status:** **Fixed here.** A manifest asks for the runner permissions alone unless the request says `migration: true` (`github.ManifestOptions.Migration`, `POST /installations/manifest`, the generated client). The dialog has an unticked "Also let Zoomies open migration pull requests" box and the permission list under it changes as it is ticked; the installer asks the same question (default no) and prints the permissions it will ask for. Health checks ignore the three migration scopes, so an App without them is not reported unhealthy. The migration steps already name which permissions are missing and link to the App's permissions page, so nothing was added there; the hint now says the App was created without the option. Docs: the README, `migration.md`, `api-surface.md` and `security.md` say what is true now; the quick start's step 3 lists the runner permissions alone and explains the question, and the home page no longer claims the App can write to code unless asked. The tab that connects an App you already have is now *Existing App*, and the API message that sends an operator to it says so. **This changes what an App can do, so it is flagged for explicit sign-off in the pull request**, with the failure modes: an existing App keeps whatever it already has, an operator who declines and later migrates has to approve the extra permissions as the owner of the account, and the migration wizard reports that clearly rather than failing part-way.
- **Pass:** First-time user
- **Where:** `web/src/lib/installations/ConnectDialog.svelte` L451–463 (the permission list, shown at step 1 of the manifest path). The same set is requested by the terminal installer, and `docs/index.md` L186–188 and `docs/quickstart.md` L115–142 call it "exactly the permissions it needs and no more". Desktop and mobile.
- **Problem:** To run runners the App needs `organization_self_hosted_runners: write`, `actions: read` and `metadata: read`. The manifest *also* requests `contents: write`, `pull_requests: write` and `workflows: write`, for the optional migration wizard (L459–461). To a security reviewer, "can write code and change CI workflows in every repository it is installed on" is the line that sends an evaluation to a security review, and it is shown at the moment a visitor decides whether to trust the project with their organisation. The code comment (L445–449) is candid that this is a trade: adding scopes later needs an owner's approval, so everyone pays for one feature up front. The docs are candid too: right under the "exactly… no more" sentence, `docs/quickstart.md` says *"If you never migrate anything, remove them on the App's Permissions & events page"*. So the fix for an unneeded write scope is currently "delete it by hand after creating the App", the dialog does not say so, and it presents all eight as required.
- **Fix:**
  1. Default the manifest to the runner-only set. Add an unchecked checkbox on step 1: **"Also let Zoomies open migration pull requests (adds contents, pull_requests and workflows write)"**, with the one-line reason.
  2. Plumb it through: a boolean on the manifest request in `api/openapi.yaml`, an option on `github.ManifestOptions`, the same prompt in `askGitHubTarget` (`manifest.go` L356), and regenerate the client (`make openapi`).
  3. In `MigrateWizard.svelte` L531, when the installation lacks the scopes, say which and offer "Grant migration access", which opens the App's permission page on GitHub (the verify call already returns `missing_permissions`, as `ConnectDialog.svelte` L765 shows).
  4. Change the claim on the home page and in the quickstart to say what is requested *by default*, and move the "remove them by hand" advice out of the docs and into the product (the checkbox makes it unnecessary).

### C6 · The first-run Overview buries its one job and contradicts itself

- **Status:** **In part on main (#563, its H1, N1 and N10); here, one primary and contrast.** On main the three timing tiles say `--` until there is something to time, the events panel blames a filter only when a filter is what holds lines back, and a done step draws no blank action cell. **Here:** only the step the operator is on gets the primary button (a controller with no host of its own showed two at once), and the steps that cannot start yet no longer fail contrast (H9). This branch had gone further, and the composition was set aside for #563's: a fleet with no pool was the checklist and nothing else, **900px tall at 1440 against 2,302, and 975px at 375 against about 3,900.** On the reconciled branch a fresh controller's Overview is 2,276px at 1440 and 4,003px at 375, with the dashboard under the checklist. **Left to decide:** whether a fleet with no pool should see the checklist alone, and the second "Other runners" switch (the existing Overview spec asserts both on purpose). The Pools panel's "Create a pool" button no longer leads into a refusal: the wizard it opens stops at a neutral "Connect GitHub first" (H5).
- **Pass:** Designer and First-time user
- **Where:** `web/src/routes/Overview.svelte` L124–143; `lib/overview/FirstRun.svelte` L189; `lib/overview/PoolUtilisation.svelte` L134–147; `lib/overview/FleetMetrics.svelte` L190–191, L206–211, L266–296; `lib/overview/EventsFeed.svelte` L58, L150–155; `lib/overview/ActiveJobs.svelte` L140 with `Overview.svelte` L109; `lib/insights/HostCapacityMap.svelte`. Route `/` immediately after creating the account. Desktop and mobile.
- **Problem:** *Measured, fresh install, no jobs.* The page is **2,302px tall at 1440** and about 3,900px at 375. The checklist is the top 560px (24%). Under it are 1,740px of zeros and empty panels:
  - Six metric tiles reading `0`, `0ms` and `p95 0ms`. The code's own comment (`FleetMetrics.svelte` L190–191) says *"a median of 0ms is a claim, and -- is the truth"*; the tiles do not honour it. A never-used fleet advertising a 0ms median queue wait is a false claim.
  - A **Pools panel with a "Create a pool" button** that leads to a wizard which dead-ends at step 2 ("No GitHub connection yet"). The comment directly above it (`PoolUtilisation.svelte` L140–144) says repeating this button "would send an operator with no installation into a wizard that refuses on its first screen", and then renders it, gated only on the *role*. The checklist a few hundred pixels above carefully says "After GitHub is connected."
  - A "Recent events" empty state reading *"Nothing in the kinds you are watching: 2 kinds of event are switched off for this browser. Choose above turns them back on."* to someone who has no events. Two of twelve kinds are hidden by default, so `everything` is false (L58) and the friendly first-run copy at L152–154 (*"Once there is a pool and a host, this is where the fleet says what it has been doing"*) is unreachable.
  - Two identical **"Other runners" switches**, one in the page header and one in the Active jobs panel, 1,000px apart, bound to the same preference.
  - A 600px capacity chart with seven legend chips, two segmented controls and a time slider, for one idle host.
  - And the checklist's own primary button is a 24px `sm` button parked at the far right of a 1,160px card, about 900px from the label it belongs to.
  On a phone, a Refresh button and an "Other runners" switch sit *above* the checklist.
- **Fix:**
  1. While `setupPending` (already computed at `Overview.svelte` L63 and passed to `ProblemsSummary`), render **only** `<FirstRun>` plus one muted line, "Your fleet's live numbers appear here after the first job." Wrap `FleetMetrics`, the three panels and `HostCapacityMap` in `{#if !setupPending}` exactly as `FleetActivity` already is at L125. Hide Refresh and the switch in the same state.
  2. In `FirstRun.svelte`, make the current step's button `size="md"` and place it directly under that step's description (a `.why` + button stack), not in a far-right grid cell. Keep the others as quiet text.
  3. `PoolUtilisation.svelte` L145: gate the button on `installations > 0`, or remove it.
  4. `FleetMetrics.svelte` L206–211: map a zero sample count to `undefined` so the tiles render `--` (the formatter and the convention from commit `349abef` already exist).
  5. `EventsFeed.svelte` L58: define `everything` as "the user has not filtered", not "nothing is hidden by default", so a fresh browser gets the helpful copy.
  6. Delete one of the two "Other runners" switches.

---

## 2. High impact

### H1 · The Bootstrap screen, the very first screen of the product, is the weakest page in it

- **Status:** **Partly fixed, mostly on main (#563, its C2 and H10).** The card leads with the 72px mark, the email field is gone, the button ends at 875px of 900 at 1440×900, and the setup token has a help icon listing where the line is for `zoomies logs`, Compose, a started container, systemd and a PaaS. **Here:** the lede names the role the account actually gets, Platform, where it said "the admin role" (N2). **Left for a decision:** the split layout, dropping confirm-password (a security-UX call), a `#setup=` URL fragment (#563 left it for a threat review, and so do I), and cutting the "Then: …" paragraph, which an existing spec asserts is there.
- **Pass:** Designer and First-time user
- **Where:** `web/src/routes/Bootstrap.svelte` L180–199 (lockup and two paragraphs), L222 (token hint), L307–351 (confirm and email); `lib/components/Logo.svelte` L44. Route `/` with no account. Desktop and mobile.
- **Problem:** The card is **1,300px tall**. The lockup is a **298×298px black tile** (`Logo.svelte` L44: `max(220, size×3.1)` with `size={96}`; 261px on a phone). That size is deliberate brand policy ("first run" is one of the screens the pack gives the full lockup, `Logo.svelte` L7–9), so the problem is not the logo but that it is stacked *above* the form, followed by eight lines of preamble (the lede plus "Then: connect a GitHub App, …") before the first field. The Create button sits at **y=1,171, below the fold at 1440×900**; on a 375×812 phone the first field starts near y=650 and the button near y=1,190. The component's own comment (L360–365) says the only person who sees this "wants the form and nothing to read on the way to it". Meanwhile the **Login page is gorgeous** (brand panel, headline, facts), so the two pre-auth screens look like different products, and the weaker one is the first impression. The form asks for five things, one of which sends the user *out of the browser*: a setup token to be found in a log with `docker compose logs zoomies | grep 'setup token'` (compose wording shown to everyone, including PaaS and marketplace users, `docs/paas.md`, `docs/marketplace.md`); then username; password; confirm-password *plus* a show-password eye (redundant by design); and an email nothing uses (H11).
- **Fix:** Give Bootstrap the Login page's split layout (`Login.svelte` L403–666: black brand panel, form column) instead of a single stacked card. The lockup keeps the room the brand pack gives it, and the form starts at the top of the right-hand column. On a phone, use Login's own band (`--lockup: 13.75rem`, the 220px floor) with the form rising over it as a sheet. That also removes the "two different products" inconsistency. Cut the lede to one sentence and delete "Then: …" (the checklist says it next). Drop the confirm field and the email field, leaving **three fields**. Remove the log hunt for installer users: the container installer already extracts the token (`internal/installer/container.go` L920–950), so print `https://host/#setup=<token>` and have `Bootstrap.svelte` read the URL *fragment* (it never reaches the server or a proxy's access log), collapsing the token field behind "I have a setup token" otherwise. Make the hint neutral ("printed in the controller's log at startup, on the line beginning `setup token`") with the compose command in a `<details>`. Target: the submit button is visible at 1440×900 and within one scroll at 375.

### H2 · The landing headline spends the biggest type on a pun and the dimmest colour on the product name

- **Status:** **Fixed on main (#559).** The headline now reads "GitHub Actions runners on machines you own.", the wording this finding suggested, with the product's name in the primary colour and a lede that says what Zoomies does. This branch changed nothing here.
- **Pass:** Designer
- **Where:** `docs/index.md` L18 (`# Give your GitHub Actions runners the Zoomies.`); `docs/stylesheets/zoomies.css` L841–845 (`.quiet`); compare `web/src/routes/Login.svelte` L619. Desktop and mobile.
- **Problem:** The H1 is a joke that needs "the zoomies" as dog slang to land, and the second line, the only part that names the product, is deliberately set in the *quietest* text colour (comment at L829). It says nothing about the outcome. A first-time visitor reads 72px of whimsy, then must read the lede to learn it is CI infrastructure. The contrast is fine (line two is 4.45:1 light and 5.75:1 dark at 72px); the *hierarchy* is wrong. The better headline **is already in your product**: the Login panel says **"GitHub Actions runners on machines you own."**
- **Fix:** H1 → "GitHub Actions runners on machines you own." Lede → "A fresh runner for every job, autoscaled across your own hosts. No Kubernetes, no database server." Remove `.quiet` (or apply it to the second *clause*, not the name). Under the command add one line of proof chips: `AGPL-3.0 · one binary · SQLite · about 5 minutes`. Keep "Give your CI the Zoomies." exactly where it already is, as the closing CTA, where the brand voice earns its place.

### H3 · The header and hero offer six equal-weight exits and no persistent install action

- **Status:** **In part on main (#559); the rest is left for a decision.** The hero has a primary "Get started" and a second button for the demo, the NEW pill is out of it, the chips are three ("See it running", "Compared with ARC", "What it costs") and the proof line under it is the project's own CI. **Left:** an Install button and a Docs link in the header, a sticky install bar once the command scrolls away, and the repository counts the header shows ("v1.3.4 ★52 ⑂43"), which are marketing positioning.
- **Pass:** Designer
- **Where:** `docs/index.md` L9–11 (`hide: navigation, toc`), L16 (the "NEW" pill), L34 (five "Popular" chips); `mkdocs.yml` L47–58 (no `navigation.tabs`); `overrides/partials/source.html` and `hooks/source.py` (header repository stats). Desktop 1440.
- **Problem:** *Measured.* The header has a logo, a theme toggle, search and a repository link, with **no nav items (0 `.md-tabs`) and no button**. The first screen offers: the NEW pill (which leaves for a sub-feature page, "Elastic CPU zoomies", before the visitor knows what Zoomies is), the command, five "Popular" chips (one is "Private hosts with Tailcat", a term nobody outside the project can parse), search, and the repo link. The first `.md-button` on the page is at y=1,864 and "Install Zoomies" is at **y=7,120 of 7,518**. Scroll past the command at y≈500 and the next install offer is a repeat of the command at y≈3,800 (inside "Five minutes to a running fleet"), then the button at y=7,120. The only number the header shows is **"v1.3.4 ★52 ⑂43"**, as built today: small adoption advertised on every page.
- **Fix:** Add a right-aligned primary **Install** button and a **Docs** link to the header (`overrides/main.html`, header block). Add a slim sticky bar once the hero command scrolls out of view (`curl … | sh  [Copy]`). Cut "Popular:" to two chips, "Quick start" and "Try the demo" (C4). Move the NEW pill below the fold or drop it. Gate the ★/⑂ counts behind a threshold in `hooks/source.py` until they help, and lead with the proof you do have: *"This repository's own CI runs on Zoomies"* (`docs/index.md` L270–277) as a one-line badge under the command.

### H4 · Landing typography and layout: 172-character lines, an orphaned card, three table widths

- **Status:** **Fixed, split with main.** **Here:** prose directly under the hero is held to `72ch` (the site's root is 20px, so the audit's `46rem` would have been about 123 characters), which takes the first line from 172 characters to 95; and the three tables are real tables as wide as the column (983, 1,288 and 656px became 1,288px each). **On main (#559):** the feature grid is three across from 768px with the last card taking the row, so nine cards tile with no orphan. **Not done:** trimming each card to 25 words, which is copy.
- **Pass:** Designer
- **Where:** `docs/stylesheets/zoomies.css` L428–430 (`.md-grid { max-width: 66rem }`), L964–969 (`.zoomies-grid`), tables; `docs/index.md`. Desktop 1440.
- **Problem:** *Measured.* Body paragraphs are 1,288px wide, **172 characters on the first line** (aim for 60–80). The feature grid is described in the CSS as "a three-across grid", but `repeat(auto-fit, minmax(15rem, 1fr))` makes it **four across**, so nine cards leave a **lone orphan ("Safe defaults") with a void beside it** before the full-width tenth. The page's three tables are 983, 1,288 and 656px wide. Feature cards run 23–64 words (average about 42). The page is 1,979 words and 7,518px for a visitor who decides in seconds.
- **Fix:** `.md-content__inner:has(.zoomies-hero) > :is(p, ul, ol) { max-width: 46rem }` (leave tables, grids and screenshots full-width). `.zoomies-grid { grid-template-columns: repeat(3, minmax(0, 1fr)) }` at ≥ 60em, two at ≥ 45em, one below (9 = 3×3, no orphan). `.md-typeset table:not([class]) { width: 100% }` for all three tables. Trim each card to ≤ 25 words and let the existing "How it works →" links carry the detail.

### H5 · The pool wizard misdescribes itself, dead-ends, then makes a user click through defaults

- **Status:** **Partly fixed.** The subtitle no longer promises seven steps: that is #563's (its H2). **Here:** a controller with no installation gets a "Connect GitHub first" empty state with the button instead of the wizard (no more dead end on step two), and on a phone the `Wizard` footer is sticky above the bottom bar. **Left for a decision:** removing the mode step and rendering the Automatic path as one screen is a redesign of the most important form.
- **Pass:** First-time user
- **Where:** `web/src/routes/PoolWizard.svelte` L30 (subtitle) and L4–6 (comment); `lib/pools/PoolVocabulary.svelte` L328 (`SIMPLE_STEP_IDS`); `lib/pools/StepMode.svelte`; `lib/pools/PoolWizardForm.svelte` L1024; `lib/components/Wizard.svelte` L195–201. Desktop and mobile.
- **Problem:** The page subtitle reads *"Seven steps: who the runners register with, what labels they answer to, which hosts they land on, how they run, how much machine each one gets, how many there are, and what the controller makes of it."* The stepper beside it says **"Step 1 of 5"** (the Automatic path: Setup, Target, Labels, Docker, Review). The first user-facing sentence of the most important form describes a larger job than the one in front of them, in 37 words. Step one is a fork ("how much do you want to decide?") with a four-paragraph explainer, a decision before value. A user who arrives from the Overview's Pools button with no GitHub connection spends a click and reads step 2 to discover "No GitHub connection yet" (`StepTarget.svelte` L123–126). Measured: every field has a default (a name is prefilled), yet it takes **5 button presses** to create the pool. On a phone, "Next" is about 800px of explainer below the choice cards, because the footer is not sticky.
- **Fix:** Subtitle → "Name it, label it, create it. Everything else has a default you can change later." Delete the `mode` step on creation: render the Automatic form as **one screen** (name, installation, label preview, "Docker in jobs" as a toggle) with **Create pool** as the primary and a quiet "Advanced options" link that switches to `ADVANCED_STEP_IDS`. Guard the route: if there are no installations, render the existing "No GitHub connection yet" EmptyState *instead of* the wizard. Under `max-width: 768px` make the `Wizard.svelte` footer `position: sticky; bottom: calc(var(--z-space-16) + var(--z-safe-bottom))`, the same clearance `AppFooter.svelte` L115–116 reserves for the fixed bottom bar.

### H6 · The Connect dialog's buttons do not say what they do, the checklist adds a redundant click, and everyone is asked Enterprise questions

- **Status:** **Fixed here, with one item left for a decision.** The footer's primary on step two is "Create the App on GitHub" and is the button that leaves, so there is one primary where there were two; "Exchange the code" is "Use this code" and appears where a code is actually pasted; step one's footer reads "Continue" (disabled while #563's "Create the App anyway" tick is unticked); App name and API base URL sit behind "Advanced: a custom App name, GitHub Enterprise" (open when either has a value or an error); the checklist opens the dialog directly. The tabs are "New App" and "Existing App" on main too (#565, the same two labels, with the longer wording moved into the tab list's accessible name). **Not done:** collapsing the two primaries on an empty Installations page — Hosts has the same header-plus-empty-state pattern, so that is a product-wide convention to change in one go or not at all.
- **Pass:** First-time user and Designer
- **Where:** `ConnectDialog.svelte` L1324 ("Continue to GitHub"), L998 ("Create the App on GitHub"), L1334 ("Exchange the code"), L912–979 (the fields), L743–750 (tabs); `FirstRun.svelte` L189; `routes/Installations.svelte` L219–223 and L249–253; `lib/components/Tabs.svelte` L126, L140. Desktop and mobile.
- **Problem:** (i) **"Continue to GitHub" does not go to GitHub.** It advances to step 2, where the in-body "Create the App on GitHub" actually leaves, while the footer primary reads **"Exchange the code"** and is disabled until a code exists: two primaries on one screen, one of them jargon for a step the user never performs (it runs itself on return). (ii) The checklist's "Connect GitHub" does not open the dialog; it navigates to `/installations`, which shows **two more primary "Connect GitHub" buttons** (header and empty state). It is click 1 and click 2 of the measured path. (iii) Step 1 asks every user for org-or-repo, the login, **"App name (optional)"** and **"API base URL" (optional; GitHub Enterprise Server)**; the terminal installer asks the same four (`manifest.go` L356–420). (iv) At 375px the second tab, "Use an App you already have", is **clipped mid-word with no scroll cue**.
- **Fix:** `FirstRun.svelte` L189 → `href="/installations?connect=1"`, and open the dialog in `Installations.svelte` when `router.param('connect')` is set (the same effect already opens it for `code` and `installation_id`, L131–134). When the list is empty, render a single primary, not two. Rename the footers: step 0 "Next: create the App on GitHub"; step 1's footer primary *is* the in-body action ("Open GitHub to create the App"), and "Exchange the code" becomes "Try again" inside the existing fallback `<details>`. Fold App name and API base URL into a collapsed "Advanced (GitHub Enterprise, custom name)". Shorten the tabs to "New App" and "Existing App", or use `Segmented` at ≤ 480px.

### H7 · Data grids cut the one thing that tells rows apart

- **Status:** **Left for a decision.** Which columns a data grid shows by default, and a new `identity` column rule in `DataGrid`, change a shared component and what every operator sees.
- **Pass:** Designer
- **Where:** `web/src/routes/Pools.svelte` L372–455 (13 default-visible columns); `routes/Workflows.svelte` L384–447 (9 columns); `lib/components/DataGrid.svelte` L30–37 (declared widths are *shares*; "the grid never scrolls sideways") and L524/L551 (`priority: 'wide'` only drops columns below 1180px); `hiddenByDefault` is used **once** in the whole app (`routes/Jobs.svelte` L300). Desktop 1440 and 820.
- **Problem:** *Measured on the demo fleet.* At 1440 the Pools table's two rows read `zoomies-demo-li…` and `zoomies-demo-li…`: **the suffix that differs (`x64` against `arm64`) is exactly the part clipped.** Status reads `Enal`, the risk pill `2 r`, and headers read `R…`, `LA…`, `T…`, `B…`, `P…`, `I…`, `LIF…`, `DO…`, `S…`, `AC…`. On Workflows the headers are `R…`, `REP…`, `WO…`, `BRAN…`, `QUE…` beside `QUEU…` (queue wait, or queued?). It is not an empty-state quirk: the *empty* Pools table shows the same truncated headers over "No pools yet", so a new user meets it before they have any data.
- **Fix:** In `Pools.svelte` set `hiddenByDefault: true` on `target`, `backend`, `platform`, `idle_timeout`, `ephemeral` and `docker_mode`, leaving Name, Risk, Labels, Runners, Queued, Status and Actions (about 165px each at 1440). Add an `identity: true` column flag to `DataGrid` so an identifier column's declared width is a *floor* the share arithmetic cannot go below. Truncate names in the middle (`head…tail`: first 14 and last 8 characters, full text in `title`) so the suffix survives. Apply the same default-visible audit to Workflows (default-hide Branch).

### H8 · Unmet prerequisites are rendered as errors or alerts, and some have no button

- **Status:** **Fixed.** Migrate with no installation is a neutral "Connect GitHub to migrate" empty state with the button, and the Installations page draws the webhook panel only once there is a connection.
- **Pass:** First-time user and Designer
- **Where:** `lib/migrate/MigrateWizard.svelte` L531–534 (`<ErrorState title="No installation to migrate">`); `routes/Installations.svelte` L317 (`<WebhookHealth>` under the empty state); the wizard's step 2 (H5). Desktop and mobile.
- **Problem:** Red means failure in this product ("operators learn them"). A user who simply has not done step 2 is shown **danger red** on Migrate (`ErrorState` is `role="alert"` with a warning triangle), with **no action**: "Connect one on the Installations page first." is plain text, not a link. Every other empty page (Pools, Installations) uses a neutral `EmptyState` with a button. On the Installations page, under a one-action empty state, a second diagnostic panel appears: an **amber** "No webhook has been received, so the controller is polling GitHub instead…", a "Check reachability" button and an empty deliveries list, for someone who has connected nothing.
- **Fix:** Use `EmptyState` for every "do X first" state. Migrate → title "Connect GitHub to migrate", `<Button href="/installations?connect=1">Connect GitHub</Button>`. Render `<WebhookHealth>` only when `installations.length > 0` (`Installations.svelte` L317).

### H9 · The checklist's explanatory text fails contrast exactly where it explains what to do next

- **Status:** **Fixed here.** The opacity is gone; a step that is not yet possible is shown by a muted title and a dashed marker. Measured on a fresh controller from the reconciled branch at 1440 and 375: no text run below its contrast threshold on the Overview.
- **Pass:** Designer
- **Where:** `web/src/lib/overview/FirstRun.svelte` L336–338 (`li.waiting .body { opacity: 0.72 }`), L371–378 (`.why`, 12px). Desktop and mobile.
- **Problem:** *Measured, and it matches the hand calculation.* The effective colour of the dimmed steps' 12px copy is **3.1:1** on the `--z-accent-subtle` ground (AA needs 4.5:1; `tokens.css` L74–77 claims body copy "should be past 7:1"). It is the only failing text run on the first-run path, and it is the text for steps 3 and 4, the ones the user must read next.
- **Fix:** Delete the `opacity`. Signal "not yet" with the title in `--z-text-muted`, the existing `.blocked` text and a dashed marker border. Keep `.why` at full `--z-text-muted`, which is about 5.5:1 on that ground.

### H10 · Twelve destinations from minute zero, three of which matter

- **Status:** **Left for a decision.** Navigation structure.
- **Pass:** First-time user
- **Where:** `web/src/lib/shell/sections.ts` L51–71 (`SECTIONS`), `Nav.svelte`, `NavMenu.svelte`. Desktop and mobile.
- **Problem:** A fresh install shows 12 nav items in four groups. The setup path runs through Installations (9th), Hosts (7th), Pools (2nd) and Workflows (5th), scattered across three of the four groups, while Usage, Audit, Providers, Queue, Runners and Migrate are inert until a job exists. The checklist is the only guide, and it lives on one page; open Pools and it is gone.
- **Fix:** Until `hasJobs`, pin a **"Setup · 2/4"** item at the top of the rail and the phone's bottom bar (links to `/`, progress from the same stores `FirstRun` reads) and collapse the not-yet-useful sections into a "More" group. They stay reachable by the `g` chords and ⌘K, so nothing is hidden from anyone who knows where to look.

### H11 · Answers the product collects and then throws away

- **Status:** **Fixed in part here, in part on main (#563, its H10).** (a) `checkWebhookReachable` now runs before the four GitHub questions, so nobody is asked them and then told to skip; keeping answers across a skip needs somewhere to store them and is left for a decision. (b) The Email field is gone from the first-run form on main; it stays in Settings → Users.
- **Pass:** First-time user
- **Where:** (a) `internal/installer/manifest.go` L133 (`askGitHubTarget`: four prompts) *before* L142 (`checkWebhookReachable`) and the "Skip" at L333, then `ConnectDialog.svelte` L156–162 (every field starts empty); (b) `web/src/routes/Bootstrap.svelte` L335–351 (Email: "Used only to identify the account").
- **Problem:** (a) On the default loopback install the installer asks up to **four GitHub questions**, *then* tells the user GitHub cannot reach them and offers "Skip". Skip drops all four answers and the browser asks them again from a blank form. The reachability check reads only `p.ExternalURL`; it could have come first. (b) The first-run form collects an email "used only to identify the account". The username already does that, no feature reads it, and the product sends no email at all (no mail or SMTP code anywhere in `internal/`; `UsersPanel.svelte` L635 says passwords "are not emailed"). A field is a promise; this one is not kept. *Credit where due:* the pool wizard's Automatic/Advanced choice **is** respected on edit (`StepMode.svelte` docblock), the checklist's `runs-on` is derived from the real pool's labels (`FirstRun.svelte` L97–104), and Add-a-host prefills everything.
- **Fix:** (a) Move `checkWebhookReachable` above `askGitHubTarget`; whatever was answered before a Skip should be saved (answer file or a `pending_github` settings row) so the dialog prefills target, type, API base and App name. (b) Delete Email from Bootstrap; keep it in Settings → Users, where OIDC fills it, or ship a feature that uses it first.

### H12 · On a phone the product is a very long scroll with sub-24px controls

- **Status:** **Left for a decision.** A phone layout for the data pages is a redesign, not a fix.
- **Pass:** Designer
- **Where:** All `web/src/routes/*` at 375. *Measured on the demo fleet:* Overview 5,584px, Hosts 6,512px, Usage 5,512px, Pool detail 5,161px. On Runners the first runner is about 2.3 screens down (six tiles, a lifecycle panel and the filters come first). Controls under the WCAG 2.2 AA 24px minimum before exceptions: row checkboxes 15×15, column-resize handles 12×40, header icon buttons 32×20, the top-bar logo link 22×22, tile links 136×20 (50 such controls on Runners, 40 on Overview).
- **Problem:** `web/playwright.config.ts` calls read-only phone monitoring "a stated requirement". The answer to "is anything wrong?" is one scroll away; "which runner?" is three. Tiny targets make the rows you do reach hard to hit.
- **Fix:** On `viewport.phone`, collapse the tile grid into a single horizontally scrolling strip, render the list *before* the lifecycle panel and put the filters behind a "Filter" button. Give grid checkboxes and tile links a 44px hit area via a padded `::after` (the look does not change; `--z-control-touch` already exists), and hide resize handles on coarse pointers.

---

## 3. Nice to have

### N1 · The Hosts page greets a new user with an "Expired" join token they never made

- **Status:** **Fixed.** A spent or expired token the controller minted for itself (`created_by` `system`) is not listed; a live one still is.
- **Pass:** First-time user · **Where:** `internal/controller/agents.go` L2000 (mints a one-minute `"system"` token for the embedded agent), `web/src/lib/hosts/JoinTokenList.svelte` L110; `/hosts`, both viewports.
- **Problem:** A minute after start, every embedded install shows a row: `zoojoin_…` · **Expired** · created by `system` · "2m ago". It reads like something failed.
- **Fix:** Filter `created_by === 'system'` out of the table, or label it "Embedded agent (used)".

### N2 · Bootstrap promises "the admin role"; the account is `Platform`

- **Status:** **Fixed.** The lede says "the highest role (Platform)".
- **Pass:** First-time user · **Where:** `Bootstrap.svelte` L192–193 against `lib/roles.ts` L27–36; the phone nav sheet shows "Platform".
- **Fix:** Say "the highest role, Platform" in the lede, or create `admin` and promote on demand.

### N3 · Settings → Configuration is 18,249px long and overflows at 375 with a long value

- **Status:** **Fixed.** `CopyButton` now lets its value truncate (`min-width: 0` on the chip and the value, with the full text as a tooltip). Measured with a 207-character database path: no sideways scroll at 375 or 1440. The jump bar and collapsed groups are not done.
- **Pass:** Designer · **Where:** `lib/settings/ConfigurationPanel.svelte`; `lib/components/CopyButton.svelte` L90–97 (`.value` is `nowrap` with no `min-width: 0` on its parents). `/settings/configuration`.
- **Problem:** *Measured.* 18,249px at 1440 and 29,078px at 375. With a 95-character database path, the copy chip ends 41px past the screen and the page scrolls sideways (the default `/var/lib/zoomies/zoomies.db` is a third as long and should fit; this is data-dependent). It is also the page C1 sends people to.
- **Fix:** A sticky jump/search bar; collapse every group except the one named by `?setting=`; `min-width: 0` down the `.copy` chain and `.value { max-width: 100% }`.

### N4 · The landing diagram depends on a third-party CDN and degrades to raw source

- **Status:** **Left for a decision.** Pre-rendering the diagram changes the site's build.
- **Pass:** Designer · **Where:** `docs/index.md` L70–80 (Mermaid); Material loads it from `unpkg.com`.
- **Problem:** Behind my sandbox's TLS-intercepting proxy the load failed (`ERR_CERT_AUTHORITY_INVALID`) and "How it works" rendered the raw `flowchart LR …` block. Plenty of CI owners sit behind such proxies. On an unrestricted network this is fine, so I rate it polish.
- **Fix:** Pre-render this one diagram to inline SVG in the site workflow (`mmdc`) and keep runtime Mermaid for the deeper docs.

### N5 · Between 768 and 1180px the navigation is an unlabelled icon rail

- **Status:** **Left for a decision.** Navigation structure.
- **Pass:** Designer · **Where:** `tokens.css` L298–299 (the only two breakpoints), `Nav.svelte`. 820px.
- **Problem:** Twelve icons, no labels, for a first-time visitor on an iPad; the Pools table at the same width again clips names.
- **Fix:** Label the rail at ≥ 900px, or show the tile-grid sheet at tablet width.

### N6 · Add a host: the warning that matters is styled as a hint, and six decisions share screen one

- **Status:** **Left for a decision.** `Field`'s `notice` is deliberately not a status colour (its own comment says why), and collapsing the add-host fields is a redesign.
- **Pass:** Designer · **Where:** `lib/hosts/AddHostFlow.svelte` L434 ("Only this machine answers on a loopback address…"); `/hosts/new`.
- **Problem:** The most likely failure (a loopback controller address handed to a *different* machine) is the same grey as the helper text beneath it. "Tailcat" is link-blue but is not a link. Capacity, Labels and Token-validity are prefilled yet take screen space.
- **Fix:** Use the warning tone for loopback; collapse Capacity, Labels and Token validity under "Advanced".

### N7 · The pool wizard reuses the green "idle" status colour for "no host is standing by"

- **Status:** **Fixed in part.** The room is a neutral tone while there is no host, so the idle green no longer says "a runner is standing by" about a machine that is not there. Whether the red check should block "Create pool" is left for a decision.
- **Pass:** Designer · **Where:** `lib/pools/PoolRoom.svelte` L121 and L208–210 (`--z-idle-*`). `CLAUDE.md`: status colours are "a fixed mapping… do not reuse them for anything else".
- **Problem:** On a controller with no agent, the review step shows a **red** "No connected host can run this pool" box, a **green** "No host is standing by" box, and an enabled "Create pool". Red that does not block, plus green for an unmet condition, is a mixed message.
- **Fix:** A neutral tone for the empty room; downgrade the red check to warning, or disable "Create pool" until acknowledged.

### N8 · Empty Workflows opens on the "Running" filter

- **Status:** **Left for a decision.** The default filter of a data page.
- **Pass:** First-time user · **Where:** `routes/Workflows.svelte` (the default state filter); `/workflows`.
- **Problem:** A fleet that has never run a job opens on the Running chip with three rows of controls (chips, search plus five dropdowns plus two date pickers, three toggles) above an empty table, and a paragraph explaining what a quiet fleet is.
- **Fix:** Default to "All" while `hasJobs` is false, and show the empty state with the "Run a test job" step from C2.

### N9 · Small contrast and size misses on the landing page

- **Status:** **Moot.** #559 rewrote the hero: the NEW chip and the "Popular:" label this finding measured are no longer on the page, and #563's lighter grey token covers the site's quiet text. This branch's change to the chip's size was dropped with it.
- **Pass:** Designer · **Where:** `docs/stylesheets/zoomies.css` L936–945 (`.popular`) and L811–819 (the "NEW" chip).
- **Problem:** *Measured.* The "Popular:" label is **4.45:1** in the light theme (needs 4.5), and the "NEW" chip's inner label is 11.2px.
- **Fix:** Use `--md-default-fg-color--light` for the label (about 5.9:1) and a 12px minimum for the chip.

### N10 · "Done" checklist rows reserve a dead action cell

- **Status:** **Fixed on main (#563, its N10).** A done step draws no action cell.
- **Pass:** Designer · **Where:** `FirstRun.svelte` L167 (`<div class="action"></div>`) with the mobile rule that stacks `.action` under the body.
- **Problem:** On a phone each completed step carries about 50px of blank space.
- **Fix:** Do not render the empty `.action`, or `display: none` it at ≤ 768px.

### N11 · The Docker Compose path asks for three hand-derived values

- **Status:** **Left for a decision.** A new script and a Compose entry point.
- **Pass:** First-time user · **Where:** `.env.example` (`ZOOMIES_EXTERNAL_URL`, `ZOOMIES_ENCRYPTION_KEY` via `openssl rand -base64 32`, `DOCKER_GID` via `stat -c '%g' /var/run/docker.sock`) *(from source)*.
- **Problem:** Compose "refuses to start until the three values marked (required) are set". The installer fills them; a person following the manual path hits three lookups before their first `up`.
- **Fix:** Ship a tiny `./init-env.sh` (or `docker compose run --rm zoomies init-env`) that writes all three, and link it from `docs/compose.md`.

### N12 · No post-value moment

- **Status:** **Fixed on main (#563)** with its C4: the first job of the fleet's own gives way to a summary of how long it waited and ran.
- **Pass:** First-time user · **Where:** `FirstRun.svelte` L142–145 (silent self-dismissal).
- **Problem:** The best moment in the product, the first job running on your own runner, is marked by a panel *disappearing*. There is no upsell here, and none is needed. A next step is.
- **Fix:** See C2 step 3.

---

## Appendix

### Artefacts I measured and deliberately discarded

I list these so nobody spends time on them.

- **Mid-page bottom navigation in full-page mobile screenshots**: a Playwright stitching artefact of `position: fixed`, not a bug.
- **Raw Mermaid text** on the landing page: my sandbox's TLS-intercepting proxy; reported only as N4.
- **The amber "2" on the bell** in the first-run captures: caused by *my* use of the `process` backend as root, not by a default install. I have not counted it against the product.
- **`ratio: 1` contrast hits on the landing page** (text colour scored equal to its background): I did not chase the cause, because the hero text I measured by hand (H2, N9) is fine.
- **Mobile `hOverflow` on the landing page** that came from Material's off-canvas drawer at `left: -242px`: normal; only the install command and the compare table (C3) are real.

### Method

- App routes were captured on three controllers, built from this commit: a fresh auth-on instance (Bootstrap → Login → first-run), the `ZOOMIES_SEED_DEMO` fleet, and the repository's own fake GitHub (`web/tests/support/serve-connect.mjs`) for the connect → pool → checklist walk.
- For every capture I recorded sideways overflow, targets under 24 and 44px, text under 12px and WCAG contrast (ancestor opacity included). *Result: no sideways scroll on any app route at 375px except N3; one failing contrast pattern (H9).*
- The site was built with `mkdocs build` from `docs/requirements.txt`.
- Click counts are literal Playwright clicks. A claim marked *(from source)* was not run.
- After the fixes, and again after the merge with main, the same measurements were repeated on the screens they touched, on a fresh controller built from the branch: the first-run Overview has no sideways scroll and no text below its contrast threshold at 1440 or 375 (it is 2,276px and 4,003px tall with #563's composition), the Configuration page fits at 375 with a 207-character database path, and the home page was measured at nine widths from 320 to 1920px in both themes, with #563's stylesheet alone and with this branch's.
- Screenshots are not committed (binary bloat); every finding names its route and viewport. To reproduce the first-run screens: `make build`, then `ZOOMIES_BIND=127.0.0.1:8080 ZOOMIES_DB_PATH=/tmp/z.db ZOOMIES_ENCRYPTION_KEY=$(openssl rand -base64 32) ./zoomies controller`, read the setup token from its log, and open the address at 1440×900 and 375×812.

### What the change tests

Each fix landed with a check at the layer it lives on, against the real binary where the behaviour is the browser's:

- **`web/tests/first-run.spec.ts`** (fresh auth-on controller): the checklist's button opens the connect dialog instead of a page that asks again; Migrate and the pool wizard say "connect GitHub first" neutrally with the button, and Installations shows no webhook fault before there is a connection. The two connect-dialog tests that #563 added are updated for the footer's new label.
- **`web/tests/github-setup.spec.ts`**: the migration box is unticked, the permission list changes as it is ticked, the request carries `migration`, and un-ticking after the manifest was built invalidates it; the first step keeps App name and API base URL behind a fold; both tabs are visible. `connect.spec.ts` follows the tab's new name.
- **Go:** the manifest asks for the runner permissions alone unless told otherwise (`TestManifestAsksForMigrationPermissionsOnlyWhenWanted`, `TestManifestGrantsWhatTheMigrationNeedsWhenAskedTo`); the API defaults `migration` to false (`TestManifestAsksForMigrationPermissionsOnlyWhenTheOperatorSaidSo`); the installer names the permissions it will ask for and asks the reachability question before the questions about the App (`TestTheReachabilityQuestionIsAskedBeforeTheQuestionsAboutTheApp`, pinned by reading the function, because the prompts cannot be driven from a test); and the message that points at the *Existing App* tab names it.
- **Docs:** `mkdocs build --strict`, with `sitemap.xml` and `llms.txt` generated, passes, and the layout was measured as described under *Method*; there is no test for the stylesheet.

### What is still yours to decide

Left alone on purpose. Each is a product, brand or information-architecture call rather than a defect, and each Status line above says why.

- **Copy and positioning:** the header's Install button, a sticky install bar and the repository counts (H3, in part).
- **Layout and navigation:** whether a fleet with no pool should see the checklist and nothing else (C6: 900px tall against 2,276px at 1440); Bootstrap's split layout and lockup, dropping confirm-password, a `#setup=` URL fragment, and cutting the "Then: …" paragraph (an existing spec asserts it is there) (H1); creating a pool on one screen (H5); data-grid default columns (H7); twelve destinations from minute zero (H10); a phone layout for the data pages (H12); the tablet rail (N5); the Workflows default filter (N8); the add-host form (N6).
- **Behaviour:** whether the connect dialog should take the missing external address itself for an account with the Platform role (C1: it was built and set aside for #563's approach); keeping the installer's GitHub answers across a skip (it needs somewhere to store them); the second *Other runners* switch; two primaries on an empty Installations page; whether a red "no host" check should block *Create pool*.
- **Assets and build:** a recorded walkthrough; a **hosted** public demo, which delivery rule 15 rules out unless the owner changes it; pre-rendering the landing diagram (N4); an `init-env` script for Compose (N11).
- **Not possible from here:** the *Open in GitHub* link that prefills the test workflow, because GitHub's current behaviour for it could not be confirmed offline.

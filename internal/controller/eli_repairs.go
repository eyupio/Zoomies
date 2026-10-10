package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/github"
	"github.com/eyupio/zoomies/internal/prrepair"
	"github.com/eyupio/zoomies/internal/store"
)

// repairHandles are the names a comment can call Eli by without any set-up.
var repairHandles = []string{"eli", "zoomies"}

// repairMention builds the pattern for a comment that starts a repair. The App's
// own slug is accepted as well: it is the one name GitHub autocompletes and
// links, so people who pick the bot from the suggestion list would otherwise
// type a command that silently does nothing.
func repairMention(appSlug string) *regexp.Regexp {
	names := make([]string, 0, len(repairHandles)+1)
	for _, h := range repairHandles {
		names = append(names, regexp.QuoteMeta(h))
	}
	if slug := strings.TrimSpace(appSlug); slug != "" {
		names = append(names, regexp.QuoteMeta(slug))
	}
	return regexp.MustCompile(`(?i)^@(` + strings.Join(names, "|") + `)(?:\[bot\])?\s+(fix|repair)\b(.*)$`)
}

func repairCommand(body, appSlug string) (string, bool) {
	mention := repairMention(appSlug)
	fenced := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			fenced = !fenced
			continue
		}
		if fenced || strings.HasPrefix(line, ">") {
			continue
		}
		if match := mention.FindStringSubmatch(line); len(match) > 0 {
			return strings.TrimSpace(match[2] + match[3]), true
		}
	}
	return "", false
}
func (c *Controller) repairPolicy(ctx context.Context, repo, installation string) (*store.EliRepairPolicy, error) {
	policies, err := c.st.EliRepairPolicies(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range policies {
		if p.Repo == repo && p.InstallationID == installation && p.Enabled {
			return &p, nil
		}
	}
	return nil, store.ErrNotFound
}

// This runs only after the normal webhook HMAC verification. Model work stays off the HTTP path.
func (c *Controller) enqueueRepairWebhook(ctx context.Context, inst *store.Installation, event string, body []byte) error {
	if inst == nil {
		return nil
	}
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return errMalformedDelivery
	}
	policy, err := c.repairPolicy(ctx, envelope.Repository.FullName, inst.ID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	r := &store.EliRepair{InstallationID: inst.ID, Repo: policy.Repo, ProviderID: policy.ProviderID}
	switch event {
	case "issue_comment":
		var p struct {
			Action string `json:"action"`
			Issue  struct {
				Number      int              `json:"number"`
				PullRequest *json.RawMessage `json:"pull_request"`
			} `json:"issue"`
			Comment struct {
				ID   int64  `json:"id"`
				Body string `json:"body"`
				User struct {
					ID    int64  `json:"id"`
					Login string `json:"login"`
					Type  string `json:"type"`
				} `json:"user"`
			} `json:"comment"`
			Sender struct {
				ID int64 `json:"id"`
			} `json:"sender"`
		}
		if err := json.Unmarshal(body, &p); err != nil {
			return errMalformedDelivery
		}
		command, ok := repairCommand(p.Comment.Body, inst.AppSlug)
		if !ok || p.Action != "created" || p.Issue.PullRequest == nil || p.Comment.ID <= 0 || p.Issue.Number <= 0 || p.Comment.User.Type != "User" || p.Sender.ID != p.Comment.User.ID {
			return nil
		}
		identities, err := c.st.EliIdentities(ctx)
		if err != nil {
			return err
		}
		for _, id := range identities {
			if id.GitHubUserID == p.Comment.User.ID && id.Confirmed {
				r.UserID = id.UserID
				break
			}
		}
		// Unlinked strangers cannot consume another user's provider or fill the repair budget.
		if r.UserID == "" {
			return nil
		}
		user, err := c.st.GetUser(ctx, r.UserID)
		if err != nil || user.Disabled {
			return nil
		}
		admissionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := c.ClientFor(admissionCtx, inst.ID)
		if err != nil {
			cancel()
			return err
		}
		client, supported := raw.(github.RepairClient)
		if !supported {
			cancel()
			return nil
		}
		actor, err := client.RepairActor(admissionCtx, r.Repo, p.Comment.User.Login)
		cancel()
		if err != nil {
			return err
		}
		if actor.ID != p.Comment.User.ID || !actor.CanWrite {
			return nil
		}
		r.PullNumber = p.Issue.Number
		r.GitHubUserID = p.Comment.User.ID
		r.GitHubLogin = p.Comment.User.Login
		r.Trigger = "mention"
		r.Instruction = command[:min(len(command), 4000)]
		r.DedupKey = fmt.Sprintf("comment:%s:%d", r.Repo, p.Comment.ID)
	case "workflow_job":
		if !policy.Automatic {
			return nil
		}
		e, err := github.ParseWorkflowJob(body)
		if err != nil {
			return errMalformedDelivery
		}
		if e.Action != "completed" || e.Conclusion != "failure" && e.Conclusion != "timed_out" || e.HeadSHA == "" || e.RunID <= 0 {
			return nil
		}
		ownCommit, err := c.st.IsEliRepairCommit(ctx, r.Repo, e.HeadSHA)
		if err != nil {
			return err
		}
		if ownCommit {
			return nil
		}
		// A failing default-branch run must not spend a PR's budget. GitHub's job
		// delivery has no PR association, so resolve the pinned run before admission.
		admissionCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		raw, err := c.ClientFor(admissionCtx, inst.ID)
		if err != nil {
			cancel()
			return err
		}
		client, supported := raw.(github.RepairClient)
		if !supported {
			cancel()
			return nil
		}
		pull, err := client.RepairPullForRun(admissionCtx, r.Repo, e.RunID)
		cancel()
		if errors.Is(err, github.ErrRepairUnsupported) {
			return nil
		}
		if err != nil {
			return err
		}
		if pull.HeadSHA != e.HeadSHA {
			return nil
		}
		r.PullNumber = pull.Number
		r.JobID = e.JobID
		r.RunID = e.RunID
		r.HeadSHA = e.HeadSHA
		r.Trigger = "automatic"
		r.Instruction = "Diagnose the failed PR job and fix its underlying code issue."
		r.DedupKey = "automatic:" + r.Repo + ":" + e.HeadSHA
	default:
		return nil
	}
	_, err = c.st.EnqueueEliRepair(ctx, r, policy.DailyLimit)
	if errors.Is(err, store.ErrConflict) {
		c.log.Info("Eli repair budget reached", "repo", r.Repo)
		return nil
	}
	return err
}

type repairModel struct {
	provider assistant.Provider
	model    string
}

func (m repairModel) Answer(ctx context.Context, messages []prrepair.Message) (string, error) {
	turns := make([]assistant.Message, 0, len(messages))
	for _, v := range messages {
		turns = append(turns, assistant.Message{Role: assistant.Role(v.Role), Content: v.Content})
	}
	stream, err := m.provider.Chat(ctx, assistant.Request{Model: m.model, System: prrepair.SystemPrompt, Messages: turns, MaxTokens: 12000})
	if err != nil {
		return "", fmt.Errorf("the configured provider could not start a repair response")
	}
	defer stream.Close()
	var out strings.Builder
	done := false
	for {
		ev, ok := stream.Next(ctx)
		if !ok {
			break
		}
		if ev.Err != nil {
			return "", fmt.Errorf("the configured provider stopped during the repair")
		}
		if ev.ToolCall != nil {
			return "", fmt.Errorf("the provider returned an unsupported tool call instead of a repair plan")
		}
		out.WriteString(ev.Delta)
		if out.Len() > prrepair.MaxReplyBytes {
			return "", fmt.Errorf("the model response exceeded the repair limit")
		}
		if ev.Done {
			done = true
			break
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if !done {
		return "", fmt.Errorf("the model response ended before completion")
	}
	return out.String(), nil
}
func (c *Controller) repairLoop(ctx context.Context) {
	if err := c.st.InterruptEliRepairs(ctx); err != nil {
		c.log.Error("could not recover Eli repairs", "error", err)
		return
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if c.mayAct() && !c.transferDraining.Load() {
			if row, err := c.st.NextEliRepair(ctx); err == nil {
				c.runEliRepair(ctx, row)
			}
			c.checkEliRepairs(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// repairHeading gives each stage of a repair its own face, so a thread of Eli's
// updates reads as one dog doing something rather than a log. The state is the
// repair's, not the text's: the wording of a message can change without the
// heading drifting.
func repairHeading(state string) string {
	switch state {
	case "working":
		return "### 🐕 Eli is sniffing around"
	case "checking":
		return "### 🦴 Eli fetched a fix"
	case "succeeded":
		return "### 🎾 Good dog: the checks passed"
	case "checks_failed":
		return "### 🐾 Eli's fix did not pass the checks"
	case "failed", "blocked":
		return "### 🐶 Eli got stuck"
	case "superseded", "unverified":
		return "### 🐕‍🦺 Eli lost the scent"
	}
	return "### 🐶 Eli"
}

func (c *Controller) repairNotice(ctx context.Context, client github.RepairClient, r *store.EliRepair, text string) {
	if r.PullNumber <= 0 {
		return
	}
	text = repairHeading(r.State) + "\n\n" + text + "\n\n<sub>Repair `" + r.ID + "`. Eli fetches fixes but never merges: that part stays with you. Ask again with `@eli fix`.</sub>\n<!-- zoomies-eli-repair:" + r.ID + " -->"
	id, err := client.RepairComment(ctx, r.Repo, r.PullNumber, r.CommentID, text)
	if err != nil {
		c.log.Warn("could not update Eli's PR comment", "repair", r.ID)
		return
	}
	r.CommentID = id
	_ = c.st.SaveEliRepair(ctx, r)
}

// Model deadlines stop work, but must not prevent recording its terminal outcome.
func (c *Controller) persistEliRepair(ctx context.Context, r *store.EliRepair) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return c.st.SaveEliRepair(persistCtx, r)
}

func (c *Controller) runEliRepair(parent context.Context, r *store.EliRepair) {
	ctx, cancel := context.WithTimeout(parent, 10*time.Minute)
	defer cancel()
	policy, err := c.repairPolicy(ctx, r.Repo, r.InstallationID)
	if err != nil || r.Trigger == "automatic" && !policy.Automatic {
		r.State = "blocked"
		r.Message = "Repository repairs are disabled or the installation changed."
		_ = c.persistEliRepair(ctx, r)
		return
	}
	raw, err := c.ClientFor(ctx, r.InstallationID)
	if err != nil {
		r.State = "blocked"
		r.Message = "The GitHub installation is unavailable."
		_ = c.persistEliRepair(ctx, r)
		return
	}
	installation, installationErr := c.st.GetInstallation(ctx, r.InstallationID)
	client, ok := raw.(github.RepairClient)
	if !ok || installationErr != nil {
		r.State = "blocked"
		r.Message = "This GitHub client does not support PR repair."
		_ = c.persistEliRepair(ctx, r)
		return
	}
	r.State = "working"
	r.Message = "Reading the PR and failed-job evidence."
	if err := c.persistEliRepair(ctx, r); err != nil {
		return
	}
	fail := func(message string) {
		r.State = "failed"
		r.Message = prrepair.Redact(message)
		_ = c.persistEliRepair(ctx, r)
		c.repairNotice(ctx, client, r, r.Message)
	}
	var pull github.RepairPull
	if r.Trigger == "automatic" {
		pull, err = client.RepairPullForRun(ctx, r.Repo, r.RunID)
	} else {
		pull, err = client.RepairPull(ctx, r.Repo, r.PullNumber)
	}
	if err != nil {
		fail("The PR is closed, from a fork, stale, or could not be read. No branch was changed.")
		return
	}
	r.PullNumber = pull.Number
	if r.HeadSHA != "" && r.HeadSHA != pull.HeadSHA {
		fail("The PR changed after this failure. No branch was changed.")
		return
	}
	r.HeadSHA = pull.HeadSHA
	var provider *store.AssistantProvider
	if r.Trigger == "mention" {
		actor, err := client.RepairActor(ctx, r.Repo, r.GitHubLogin)
		if err != nil || actor.ID != r.GitHubUserID || !actor.CanWrite {
			r.State = "blocked"
			r.Message = "The requester needs current repository write permission."
			_ = c.persistEliRepair(ctx, r)
			return
		}
		user, err := c.st.GetUser(ctx, r.UserID)
		if err != nil || user.Disabled {
			fail("The linked Zoomies account is disabled or unavailable.")
			return
		}
		identities, err := c.st.EliIdentities(ctx)
		linked := false
		if err == nil {
			for _, id := range identities {
				if id.UserID == r.UserID && id.GitHubUserID == r.GitHubUserID && id.Confirmed {
					linked = true
				}
			}
		}
		if !linked {
			fail("The GitHub account link was removed or changed.")
			return
		}
		provider, err = c.personalChatProvider(ctx, "", r.UserID)
		if err != nil {
			fail("The requester has no enabled personal default provider. Set one in Zoomies, Settings, Assistant, then ask again.")
			return
		}
	} else {
		provider, err = c.st.GetAssistantProvider(ctx, policy.ProviderID)
		if err != nil || !provider.Enabled || provider.OwnerID != "" {
			fail("The repository's installation provider is unavailable. No personal provider was used.")
			return
		}
	}
	if strings.HasPrefix(r.DedupKey, "ui:") && assistant.Subscription(assistant.Kind(provider.Kind)) && !r.RequesterAdmin {
		fail("The requesting credential needs administrator permission to run subscription tools.")
		return
	}
	r.ProviderID = provider.ID
	_ = c.persistEliRepair(ctx, r)
	c.repairNotice(ctx, client, r, "Investigating this PR using "+provider.Name+". The repair is limited to one commit and will leave normal review in place.")
	source, err := client.RepairSource(ctx, r.Repo, pull, policy.AllowWorkflows)
	if err != nil {
		fail("The PR source could not be read within Eli's repair limits.")
		return
	}
	failures, err := client.RepairFailures(ctx, r.Repo, pull, r.JobID)
	if err != nil {
		fail("Failed-job evidence could not be read. Check the App's Actions read permission.")
		return
	}
	if r.Trigger == "automatic" && len(failures) == 0 {
		fail("The failed job has no usable error evidence. No code was changed.")
		return
	}
	source.Snapshot.Failures = failures
	p, err := c.OpenAssistantProvider(provider, "")
	if err != nil {
		fail("The configured model provider could not be opened.")
		return
	}
	plan, err := prrepair.Generate(ctx, repairModel{provider: p, model: provider.Model}, source, source.Snapshot, r.Instruction, policy.AllowWorkflows)
	if err != nil {
		fail(err.Error())
		return
	}
	// Re-read both authorisation and credentials after the model's work, before the first repository write.
	latestPolicy, err := c.repairPolicy(ctx, r.Repo, r.InstallationID)
	if err != nil || !latestPolicy.AllowWorkflows && policy.AllowWorkflows {
		fail("Repository repair policy changed while Eli was working. No branch was changed.")
		return
	}
	latestInstallation, err := c.st.GetInstallation(ctx, r.InstallationID)
	if err != nil || !latestInstallation.UpdatedAt.Equal(installation.UpdatedAt) {
		fail("The GitHub installation changed while Eli was working. No branch was changed.")
		return
	}
	current, err := c.st.GetAssistantProvider(ctx, r.ProviderID)
	if err != nil || !current.Enabled || current.OwnerID != provider.OwnerID || !current.UpdatedAt.Equal(provider.UpdatedAt) {
		fail("The provider was removed or disabled while Eli was working.")
		return
	}
	if r.Trigger == "automatic" && (!latestPolicy.Automatic || latestPolicy.ProviderID != r.ProviderID) {
		fail("Automatic repair was disabled or its provider changed.")
		return
	}
	if r.Trigger == "mention" {
		actor, e := client.RepairActor(ctx, r.Repo, r.GitHubLogin)
		user, u := c.st.GetUser(ctx, r.UserID)
		identities, linkErr := c.st.EliIdentities(ctx)
		linked := false
		for _, id := range identities {
			if id.UserID == r.UserID && id.GitHubUserID == r.GitHubUserID && id.Confirmed {
				linked = true
			}
		}
		if e != nil || actor.ID != r.GitHubUserID || !actor.CanWrite || u != nil || user.Disabled || linkErr != nil || !linked {
			fail("The requester's access changed while Eli was working.")
			return
		}
	}
	if !c.mayAct() || c.transferDraining.Load() || ctx.Err() != nil {
		fail("The repair stopped before publication.")
		return
	}
	sha, err := client.CommitRepair(ctx, r.Repo, pull, plan)
	if err != nil {
		fail("The repair could not advance the PR branch. It may have changed, or the App may lack Contents write permission. No force push was attempted.")
		return
	}
	r.CommitSHA = sha
	published := c.Now()
	r.PublishedAt = &published
	r.State = "checking"
	r.Message = plan.Summary + " CI has not been verified yet."
	if err := c.persistEliRepair(ctx, r); err != nil {
		c.log.Error("could not record the published Eli repair", "repair", r.ID)
		return
	}
	c.repairNotice(ctx, client, r, "Pushed commit `"+sha+"`. "+plan.Summary+"\n\nWaiting for checks on this commit. No local tests were run by Eli.")
}
func (c *Controller) checkEliRepairs(ctx context.Context) {
	rows, err := c.st.CheckingEliRepairs(ctx)
	if err != nil {
		return
	}
	for _, r := range rows {
		if r.State != "checking" {
			continue
		}
		checkStarted := r.UpdatedAt
		if r.PublishedAt != nil {
			checkStarted = *r.PublishedAt
		} else {
			r.PublishedAt = &checkStarted
		}
		if c.Now().Sub(r.UpdatedAt) < 30*time.Second {
			continue
		}
		raw, e := c.ClientFor(ctx, r.InstallationID)
		if e != nil {
			if c.Now().Sub(checkStarted) >= time.Hour {
				r.State = "unverified"
				r.Message = "The GitHub installation is unavailable. Check the PR's CI results before accepting the repair."
				_ = c.st.SaveEliRepair(ctx, r)
			}
			continue
		}
		client, ok := raw.(github.RepairClient)
		if !ok {
			continue
		}
		checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		p, e := client.RepairPull(checkCtx, r.Repo, r.PullNumber)
		state := "waiting"
		if e == nil && p.HeadSHA != r.CommitSHA {
			state = "superseded"
		} else if e == nil {
			state, e = client.RepairChecks(checkCtx, r.Repo, r.CommitSHA)
		}
		cancel()
		if e != nil && c.Now().Sub(checkStarted) < time.Hour {
			continue
		}
		switch state {
		case "passed":
			r.State = "succeeded"
			r.Message = "The reported checks on Eli's commit passed. Normal PR review still applies."
		case "failed":
			r.State = "checks_failed"
			r.Message = "Checks on Eli's commit failed. Automatic repair will not repeat its own commit; a maintainer can request another attempt."
		case "superseded":
			r.State = "superseded"
			r.Message = "The PR has a newer commit. This repair's checks no longer describe its current head."
		default:
			if c.Now().Sub(checkStarted) < time.Hour {
				r.UpdatedAt = c.Now()
				_ = c.st.SaveEliRepair(ctx, r)
				continue
			}
			r.State = "unverified"
			r.Message = "No complete check result was observed within an hour. Inspect the PR's checks before accepting the fix."
		}
		_ = c.st.SaveEliRepair(ctx, r)
		c.repairNotice(ctx, client, r, r.Message)
	}
}

// RequestEliRepair is the same owner-scoped operation as a GitHub mention, reachable from the UI.
func (c *Controller) RequestEliRepair(ctx context.Context, userID, repo string, pull int, instruction string, requesterAdmin bool) (*store.EliRepair, error) {
	if pull < 1 || len(instruction) > 4000 {
		return nil, store.ErrConflict
	}
	inst, err := c.st.FindInstallationByTarget(ctx, repo)
	if err != nil {
		return nil, err
	}
	policy, err := c.repairPolicy(ctx, repo, inst.ID)
	if err != nil {
		return nil, err
	}
	identities, err := c.st.EliIdentities(ctx)
	if err != nil {
		return nil, err
	}
	var identity *store.EliIdentity
	for _, id := range identities {
		if id.UserID == userID && id.Confirmed {
			identity = &id
			break
		}
	}
	if identity == nil {
		return nil, store.ErrNotFound
	}
	raw, err := c.ClientFor(ctx, inst.ID)
	if err != nil {
		return nil, err
	}
	client, ok := raw.(github.RepairClient)
	if !ok {
		return nil, store.ErrConflict
	}
	actor, err := client.RepairActor(ctx, repo, identity.GitHubLogin)
	if err != nil || !actor.CanWrite || actor.ID != identity.GitHubUserID {
		return nil, store.ErrConflict
	}
	pr, err := client.RepairPull(ctx, repo, pull)
	if err != nil {
		return nil, store.ErrConflict
	}
	provider, err := c.personalChatProvider(ctx, "", userID)
	if err != nil {
		return nil, err
	}
	if assistant.Subscription(assistant.Kind(provider.Kind)) && !requesterAdmin {
		return nil, ErrAssistantSubscriptionRestricted
	}
	if instruction == "" {
		instruction = "Fix the underlying issue in this PR using the failed-job evidence."
	}
	row := &store.EliRepair{Repo: repo, InstallationID: inst.ID, PullNumber: pull, HeadSHA: pr.HeadSHA, UserID: userID, GitHubUserID: identity.GitHubUserID, GitHubLogin: identity.GitHubLogin, ProviderID: provider.ID, Trigger: "mention", RequesterAdmin: requesterAdmin, Instruction: instruction, DedupKey: fmt.Sprintf("ui:%s:%d:%s:%s", repo, pull, pr.HeadSHA, userID)}
	_, err = c.st.EnqueueEliRepair(ctx, row, policy.DailyLimit)
	return row, err
}

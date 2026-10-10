package controller

import (
	"context"
	"encoding/json"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/assistant/kinds"
	"github.com/eyupio/zoomies/internal/store"
)

// AssistantProviderCheck is what a check learned, as the row keeps it and
// the page shows it. Error is written for a person and never carries the
// request; the adapters' status errors see to that.
type AssistantProviderCheck struct {
	OK            bool      `json:"ok"`
	Model         string    `json:"model,omitempty"`
	LatencyMS     int64     `json:"latency_ms"`
	UsageReported bool      `json:"usage_reported"`
	Error         string    `json:"error,omitempty"`
	CheckedAt     time.Time `json:"checked_at"`
}

// assistantCheckTimeout bounds a check: a model that has not answered one
// token in this long is not one the page should wait on.
const assistantCheckTimeout = 60 * time.Second

// OpenAssistantProvider builds the provider for a row, with the key opened
// here, where the instance key is, and the live local-only switch applied.
// apiKey, when non-empty, is used instead of the row's: a draft being
// checked has a key the row does not.
func (c *Controller) OpenAssistantProvider(row *store.AssistantProvider, apiKey string) (assistant.Provider, error) {
	if apiKey == "" && len(row.KeyEnc) > 0 {
		opened, err := c.key.OpenString(row.KeyEnc)
		if err != nil {
			return nil, err
		}
		apiKey = opened
	}
	return kinds.Open(assistant.Kind(row.Kind), kinds.Config{
		BaseURL:   row.BaseURL,
		APIKey:    apiKey,
		Model:     row.Model,
		LocalOnly: c.cfg().Assistant.LocalOnly,
		// Empty is the provider's default; the kinds that cannot carry it
		// never hold one, because the API refuses it on them.
		ReasoningEffort: row.ReasoningEffort,
	})
}

// CheckAssistantProvider runs Check and a one-token completion, which is
// how the adapters' Check is defined, and reports the result rather than
// failing: a check that found the key wrong is an answer, not an error,
// and is what the page and the row keep.
func (c *Controller) CheckAssistantProvider(ctx context.Context, row *store.AssistantProvider) AssistantProviderCheck {
	return c.checkAssistantProvider(ctx, row, "")
}

func (c *Controller) checkAssistantProvider(ctx context.Context, row *store.AssistantProvider, apiKey string) AssistantProviderCheck {
	out := AssistantProviderCheck{CheckedAt: c.Now()}
	p, err := c.OpenAssistantProvider(row, apiKey)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, assistantCheckTimeout)
	defer cancel()
	res, err := p.Check(ctx)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.OK, out.Model, out.LatencyMS, out.UsageReported = true, res.Model, res.Latency.Milliseconds(), res.UsageReported
	return out
}

// CheckAssistantDraft checks a provider that is not saved, with the key the
// form holds, and stores nothing.
func (c *Controller) CheckAssistantDraft(ctx context.Context, draft *store.AssistantProvider, apiKey string) AssistantProviderCheck {
	return c.checkAssistantProvider(ctx, draft, apiKey)
}

// RecordAssistantProviderCheck keeps a check on its row, so the page shows
// the last result after a reload and not only in the tab that pressed Test.
func (c *Controller) RecordAssistantProviderCheck(ctx context.Context, id string, check AssistantProviderCheck) error {
	raw, err := json.Marshal(check)
	if err != nil {
		return err
	}
	return c.st.SetAssistantProviderCheck(ctx, id, raw, check.CheckedAt)
}

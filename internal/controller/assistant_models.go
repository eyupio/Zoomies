package controller

import (
	"context"
	"errors"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/store"
)

// assistantModelsTimeout bounds the request for a list of models. A provider
// that has not listed them in this long is not one a form should wait on.
const assistantModelsTimeout = 30 * time.Second

// ErrAssistantCannotList is a provider that has no list of models to give.
var ErrAssistantCannotList = errors.New("this provider cannot list its models; type the model's name")

// ListAssistantModels asks a provider, as a form has it, which models it
// serves. Nothing is stored. The key is the form's, or the saved row's when the
// box is blank, exactly as a draft check chooses it.
func (c *Controller) ListAssistantModels(ctx context.Context, draft *store.AssistantProvider, apiKey string) ([]string, error) {
	p, err := c.OpenAssistantProvider(draft, apiKey)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, assistantModelsTimeout)
	defer cancel()
	return listModels(ctx, p)
}

// listModels is the part that does not depend on a controller: ModelLister is
// optional, so a provider without it is a different answer from one that failed.
func listModels(ctx context.Context, p assistant.Provider) ([]string, error) {
	lister, ok := p.(assistant.ModelLister)
	if !ok {
		return nil, ErrAssistantCannotList
	}
	return lister.Models(ctx)
}

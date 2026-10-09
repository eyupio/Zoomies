package controller

import (
	"context"
	"errors"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant"
)

// A provider that cannot list its models is not one that failed to: the page
// falls back to a box to type into for the first, and shows the error for the
// second, so the two must not be the same error.
func TestAProviderWithoutAModelListSaysSoAndOneWithAListGivesIt(t *testing.T) {
	fake := assistant.NewFake(assistant.FakeOptions{Model: "demo"})

	// Embedding the interface and not the type hides Models, which is what a
	// provider that never implemented it looks like.
	_, err := listModels(context.Background(), struct{ assistant.Provider }{fake})
	if !errors.Is(err, ErrAssistantCannotList) {
		t.Errorf("a provider without Models: %v", err)
	}

	got, err := listModels(context.Background(), fake)
	if err != nil || len(got) != 2 || got[0] != "demo" {
		t.Errorf("a provider with Models: %v, %v", got, err)
	}

	failing := assistant.NewFake(assistant.FakeOptions{Model: "demo", Fail: errors.New("down")})
	if _, err := listModels(context.Background(), failing); err == nil || errors.Is(err, ErrAssistantCannotList) {
		t.Errorf("a provider that failed: %v", err)
	}
}

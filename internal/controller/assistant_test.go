package controller

import (
	"context"
	"strings"
	"testing"

	"github.com/eyupio/zoomies/internal/assistant/assistanttest"
	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/cryptox"
	"github.com/eyupio/zoomies/internal/store"
)

// The controller is where the live config and the instance key meet, so it
// is where local-only mode is applied to a provider being opened: with it on,
// a provider that resolves to a public address never gets a connection.
func TestOpeningAProviderHonoursLocalOnly(t *testing.T) {
	h := newHarness(t)
	key, err := cryptox.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	c, err := New(Options{Store: h.st, Config: config.Default(), Key: key})
	if err != nil {
		t.Fatal(err)
	}
	srv := assistanttest.NewOpenAI(t)
	row := &store.AssistantProvider{Name: "local", Kind: "openai_compatible", BaseURL: srv.URL + "/v1", Model: "m"}
	if err := c.st.CreateAssistantProvider(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	check := c.CheckAssistantProvider(context.Background(), row)
	if !check.OK || check.Model != "m" {
		t.Fatalf("a loopback provider with local-only off: %+v", check)
	}
	c.UpdateConfig(func(cfg *config.Config) { cfg.Assistant.LocalOnly = true })
	// A literal public address keeps this assertion independent of external DNS.
	public := &store.AssistantProvider{Name: "hosted", Kind: "openai", BaseURL: "https://8.8.8.8/v1", Model: "m"}
	check = c.CheckAssistantProvider(context.Background(), public)
	if check.OK || !strings.Contains(check.Error, "local-only") {
		t.Errorf("a hosted provider with local-only on: %+v", check)
	}
}

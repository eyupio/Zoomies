// Package kinds opens a provider by its kind. It is the one package that
// imports the adapters (purity_test.go in internal/assistant holds it to
// that), so a model's wire protocol is reachable from one place and the
// dialer's local-only promise is applied here and nowhere else.
package kinds

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/assistant"
	"github.com/eyupio/zoomies/internal/assistant/provider"
)

// Config is what Open needs to build a provider.
type Config struct {
	BaseURL   string
	APIKey    string
	Model     string
	LocalOnly bool
}

// Open builds the provider for a kind.
func Open(kind assistant.Kind, cfg Config) (assistant.Provider, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		base = assistant.DefaultBaseURL(kind)
	}
	// A model API never needs a redirect, and Go's client would carry a
	// custom header such as x-api-key across hosts, so none is followed.
	client := &http.Client{
		Transport: newTransport(cfg.LocalOnly),
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return fmt.Errorf("the provider answered with a redirect to %s, which is not followed: give the address the API answers at", req.URL.Host)
		},
	}
	pc := provider.Config{BaseURL: base, APIKey: cfg.APIKey, Model: cfg.Model, Client: client}
	switch kind {
	case assistant.KindFake:
		return assistant.NewFake(assistant.FakeOptions{Model: cfg.Model}), nil
	case assistant.KindOpenAICompatible:
		if base == "" {
			return nil, errors.New("an OpenAI-compatible provider needs a base URL: the address its server listens on, such as http://localhost:11434/v1")
		}
		return provider.NewOpenAICompatible(pc), nil
	case assistant.KindOpenAI:
		return provider.NewOpenAICompatible(pc), nil
	case assistant.KindAnthropic:
		return provider.NewAnthropic(pc), nil
	case assistant.KindClaudeCode:
		// Claude Code dials Anthropic itself, outside the dialer this package
		// applies, so the one promise local-only makes cannot be kept for it.
		if cfg.LocalOnly {
			return nil, errors.New("the Claude Code provider sends what is asked to Anthropic, which is not on this machine or its network, and Local models only is on")
		}
		return provider.NewClaudeCode(pc), nil
	}
	return nil, fmt.Errorf("unknown provider kind %q; the kinds are %s, %s, %s and %s", kind,
		assistant.KindOpenAICompatible, assistant.KindAnthropic, assistant.KindOpenAI, assistant.KindClaudeCode)
}

// newTransport is an http.Transport with the assistant's dialer. In
// local-only mode no proxy is consulted either: a proxy is the one way a
// request to a local name could leave the machine, so the mode closes it.
func newTransport(localOnly bool) *http.Transport {
	t := &http.Transport{
		DialContext:           assistant.NewDialer(localOnly),
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 60 * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if localOnly {
		t.Proxy = nil
	}
	return t
}

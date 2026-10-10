package provider

import "net/http"

// Config is what an adapter needs: where, with what key, which model, and
// the client to use, which is where the assistant's dialer lives.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	// ReasoningEffort is sent as reasoning_effort on every chat-completion
	// request when set, the check included, so a model that does not take it
	// says so under Test and not in the middle of a conversation.
	ReasoningEffort string
	Client          *http.Client
	// Command is the executable a kind that runs one is to run, in place of
	// looking for it. Empty for every kind that speaks over the network.
	Command string
}

func (c Config) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

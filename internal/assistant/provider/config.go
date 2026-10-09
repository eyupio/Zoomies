package provider

import "net/http"

// Config is what an adapter needs: where, with what key, which model, and
// the client to use, which is where the assistant's dialer lives.
type Config struct {
	BaseURL string
	APIKey  string
	Model   string
	Client  *http.Client
}

func (c Config) client() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return http.DefaultClient
}

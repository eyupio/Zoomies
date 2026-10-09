package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// StatusError turns a response that is not a success into an error written
// for the person who will read it on the settings page. The request is
// never part of it: its headers hold the key, and a 401's body often quotes
// the key back, so the two statuses that mean "the key" say only that.
func StatusError(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("the provider refused the key (HTTP %d)", resp.StatusCode)
	case http.StatusNotFound:
		if m := modelName.FindSubmatch(body); m != nil {
			return fmt.Errorf("no model named %s at the provider", m[1])
		}
		return fmt.Errorf("the provider answered 404: check the base URL and the model name")
	case http.StatusTooManyRequests:
		return fmt.Errorf("the provider rate limited the request (HTTP 429); try again shortly")
	}
	if msg := errorMessage(body); msg != "" {
		return fmt.Errorf("the provider answered %d: %s", resp.StatusCode, msg)
	}
	return fmt.Errorf("the provider answered %d", resp.StatusCode)
}

// modelName finds the quoted model in the sentences both APIs write for an
// unknown model, so the error can say which name was wrong.
var modelName = regexp.MustCompile(`[mM]odel[: ]+['"\x60]?([A-Za-z0-9._:/-]+)['"\x60]?`)

// errorMessage reads the message both APIs put at error.message, and nothing
// when the body is not that shape.
func errorMessage(body []byte) string {
	var env struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return ""
	}
	return env.Error.Message
}

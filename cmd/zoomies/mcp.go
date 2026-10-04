package main

import (
	"context"
	"encoding/json"
	"io"
	"net/url"

	"github.com/eyupio/zoomies/internal/mcp"
)

// `zoomies mcp` is the fleet offered to a coding agent over the Model Context
// Protocol. It is deliberately one more API client and nothing else: it speaks
// MCP on its own standard input and output to the agent that launched it, and
// calls the controller's REST API with a token, exactly as the rest of the CLI
// does. So it adds no authority -- what an agent can see or do is what the
// token's role already allows, and the controller cannot tell it apart from
// `zoomies jobs list`.
//
// The protocol and the tools live in internal/mcp, which the controller's own
// /mcp endpoint serves as well; this is the stdio transport, for an agent that
// would rather launch a process than reach the controller over HTTP.

// runMCP is `zoomies mcp`.
func runMCP(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies mcp [--allow-actions]",
		"Serve this fleet to a coding agent over the Model Context Protocol, on standard\n"+
			"input and output. The agent's MCP configuration launches this command; it is\n"+
			"not something to run by hand.\n\n"+
			"It reads through the REST API with the same URL and token as the rest of the\n"+
			"CLI, so a viewer token is enough and is what to give it. Without --allow-actions\n"+
			"it offers read-only tools and nothing else.")
	cf := registerClientFlags(fs, false)
	allowActions := fs.Bool("allow-actions", false, "also offer rerun_job and drain_runner; the token's role must permit them too")
	fs.example(
		"zoomies mcp --url https://zoomies.example.com",
		"claude mcp add zoomies -e ZOOMIES_URL=https://zoomies.example.com -e ZOOMIES_TOKEN=zoo_... -- zoomies mcp",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	return mcp.New(mcpAPI{client}, mcp.Options{
		Offer: func(string) bool { return *allowActions },
		Refusal: func(tool string) string {
			return tool + " changes the fleet, and this server was started read-only; restart it with `zoomies mcp --allow-actions` and a token whose role permits it"
		},
	}).ServeStdio(ctx, e.in, e.out)
}

// mcpAPI is the CLI's client as the MCP tools see it: the same URL, token and
// timeout every other command uses, and the same refusals.
type mcpAPI struct{ c *apiClient }

func (a mcpAPI) Call(ctx context.Context, method, path string, q url.Values) ([]byte, error) {
	return a.c.do(ctx, method, path, q, nil, nil)
}

func (a mcpAPI) CallBody(ctx context.Context, method, path string, q url.Values, body []byte) ([]byte, error) {
	return a.c.do(ctx, method, path, q, json.RawMessage(body), nil)
}

func (a mcpAPI) Stream(ctx context.Context, path, accept string) (io.ReadCloser, error) {
	resp, err := a.c.stream(ctx, path, nil, accept)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

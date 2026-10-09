# Sentinel journal

Short notes on security findings and the fixes made for them. One entry per
pull request, newest first.

## 2026-10-08: MCP discovery responses no-store without an external URL

When `security.mcp_oauth` is on and `server.external_url` is empty, the OAuth
discovery documents served at `/.well-known/oauth-protected-resource[/mcp]`
and `/.well-known/oauth-authorization-server` fill their issuer and endpoint
addresses from each request's `Host` header. The responses carried
`Cache-Control: public, max-age=300`. A shared HTTP cache keyed on URL alone
could then hand one caller's Host-derived answer back to another, sending a
later client to a forged authorisation server.

The config validator refuses this combination at startup when the controller
binds publicly, but allows it on loopback where the operator may be running
behind a reverse proxy that forwards whatever Host the client sent.

Fix: switch those three responses to `Cache-Control: no-store` whenever
`server.external_url` is empty. The cacheable path is unchanged when it is
set, so the issuer and endpoints are then a fixed address and sharing the
cached answer is safe.

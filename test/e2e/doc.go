// Package e2e holds the Docker-based end-to-end test. It is behind the "e2e"
// build tag and additionally skips itself unless the GitHub credentials it
// needs are present, so `go test ./...` on a laptop or in CI never tries to
// talk to GitHub.
//
// See README.md in this directory for what it asserts and how to run it.
package e2e

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// runExport is `zoomies export --installation ID`.
//
// The archive is written by the controller, through the same route the API
// documents, so the CLI holds no second idea of what "everything about an
// installation" means.
func runExport(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies export --installation <installation-id> [--passphrase-file FILE] [--out FILE]",
		"Write everything about one installation -- its pools, runners, jobs, deliveries, scaling events, runner sessions "+
			"and the audit rows that name them -- as one archive another instance can import.")
	cf := registerClientFlags(fs, false)
	id := fs.String("installation", "", "the installation to export, as shown by `zoomies installations list`")
	passFile := fs.String("passphrase-file", "", "seal the App's private key and webhook secret under the passphrase in this file, so the archive can be imported without this instance's key")
	out := fs.String("out", "", "where to write the archive (default zoomies-installation-<id>.json in the current directory)")
	fs.example(
		"zoomies export --installation ins_k3f9qz2m --passphrase-file ./move.pass",
		"zoomies export --installation ins_k3f9qz2m --out history.json   # history only, no credentials",
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return usagef("export", "needs --installation, the ID `zoomies installations list` shows")
	}
	passphrase, err := readPassphraseFile(*passFile)
	if err != nil {
		return err
	}
	client, err := cf.client()
	if err != nil {
		return err
	}
	dest := *out
	if dest == "" {
		dest = "zoomies-installation-" + *id + ".json"
	}

	var body any
	if passphrase != "" {
		body = map[string]string{"passphrase": passphrase}
	}
	// Written beside the destination and renamed into place, so an export
	// that fails half way never leaves a truncated archive under the name an
	// operator will later import from.
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".zoomies-export-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := client.postInto(ctx, "/installations/"+url.PathEscape(*id)+"/export", body, tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Wrote %s.\n", dest)
	if passphrase == "" {
		fmt.Fprintln(e.out, "It carries no credentials: an import of it will need the App's private key entered by hand.")
	} else {
		fmt.Fprintln(e.out, "The App's credentials are in it, sealed under the passphrase. Keep the two apart.")
	}
	return nil
}

// runImport is `zoomies import <archive>`.
func runImport(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies import <archive> [--passphrase-file FILE]",
		"Write an installation archive from `zoomies export` onto this instance, all of it or none.")
	cf := registerClientFlags(fs, true)
	passFile := fs.String("passphrase-file", "", "the passphrase the archive was exported with")
	fs.example("zoomies import zoomies-installation-ins_k3f9qz2m.json --passphrase-file ./move.pass")
	if err := fs.parse(args); err != nil {
		return err
	}
	path, err := fs.oneArg("the archive `zoomies export --installation` wrote")
	if err != nil {
		return err
	}
	passphrase, err := readPassphraseFile(*passFile)
	if err != nil {
		return err
	}
	archive, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !json.Valid(archive) {
		return fmt.Errorf("%s is not an installation archive: it is not JSON", path)
	}
	cf.defaultTimeoutTo(fs, importRequestTimeout)
	client, err := cf.client()
	if err != nil {
		return err
	}
	p, err := cf.printer(e)
	if err != nil {
		return err
	}
	var done struct {
		Installation installationItem `json:"installation"`
		Rows         map[string]int   `json:"rows"`
		Skipped      map[string]int   `json:"skipped"`
	}
	raw, err := client.post(ctx, "/installations/import", nil,
		map[string]any{"archive": json.RawMessage(archive), "passphrase": passphrase}, &done)
	if err != nil {
		return err
	}
	if p.structured() {
		return p.emit(raw)
	}
	total := 0
	for _, n := range done.Rows {
		total += n
	}
	p.note("Imported %s (%s): %d rows.", done.Installation.Target, done.Installation.ID, total)
	if n := done.Skipped["runners"]; n > 0 {
		p.note("%d runner rows stayed behind: they name hosts only the old instance has. Their history came as sessions.", n)
	}
	p.note("Run `zoomies installations verify %s` to check the App's credentials here.", done.Installation.ID)
	return nil
}

// readPassphraseFile reads a passphrase from a file rather than a flag,
// because a flag's value is in the shell history and the process list.
func readPassphraseFile(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	pass := strings.TrimRight(string(raw), "\r\n")
	if strings.TrimSpace(pass) == "" {
		return "", fmt.Errorf("%s is empty; put the passphrase in it, or leave --passphrase-file out", path)
	}
	return pass, nil
}

// postInto sends a JSON body and copies a successful answer straight to w, for
// a response that may be larger than do is prepared to hold in memory.
func (c *apiClient) postInto(ctx context.Context, path string, body any, w io.Writer) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(encoded)
	}
	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, c.endpoint(path, nil), reader)
	if err != nil {
		return err
	}
	c.decorate(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if stopped(ctx, err) {
			return context.Canceled
		}
		return c.transportError(http.MethodPost, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return parseAPIError(http.MethodPost, path, resp.StatusCode, raw)
	}
	_, err = io.Copy(w, resp.Body)
	return err
}

package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"github.com/eyupio/zoomies/internal/version"
	"gopkg.in/yaml.v3"
)

// runConfig is `zoomies config check|print`.
func runConfig(ctx context.Context, e *env, args []string) error {
	return runGroup(ctx, e, "config", "Look at a configuration file without starting anything.", []*subcommand{
		{"check", "", "Validate a file: warnings on stderr, a non-zero exit on any error", runConfigCheck},
		{"print", "", "Print the effective configuration, secrets blanked", runConfigPrint},
	}, args)
}

// runConfigCheck validates a configuration file and says what is wrong with it.
//
// It is the same validator the controller runs at startup, so a clean check
// here is a promise that startup will not fail on the configuration.
func runConfigCheck(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies config check [--config path]",
		"Validate a configuration file. Warnings are printed and exit 0; errors exit 1.")
	cfgPath := fs.String("config", "", "path to zoomies.yaml (default: "+config.DefaultConfigFile()+")")
	fs.example("zoomies config check --config /etc/zoomies/zoomies.yaml")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	findings := cfg.Validate()
	printFindings(e.out, findings)
	if err := findings.Err(); err != nil {
		return err
	}

	switch n := len(findings.Warnings()); n {
	case 0:
		fmt.Fprintf(e.out, "%s is valid, and nothing in it weakens the defaults.\n", configSource(cfg))
	case 1:
		fmt.Fprintf(e.out, "%s is valid, with 1 warning above.\n", configSource(cfg))
	default:
		fmt.Fprintf(e.out, "%s is valid, with %d warnings above.\n", configSource(cfg), n)
	}
	return nil
}

// runConfigPrint prints the configuration as it will actually be used: the
// file, plus every ZOOMIES_* override, plus the defaults underneath.
func runConfigPrint(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies config print [--config path] [--output yaml|json]",
		"Print the effective configuration -- file, environment and defaults combined -- with secrets blanked.")
	cfgPath := fs.String("config", "", "path to zoomies.yaml (default: "+config.DefaultConfigFile()+")")
	format := fs.String("output", "yaml", "yaml or json")
	fs.example("zoomies config print", "zoomies config print --output json | jq .server")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	redacted := blankSecrets(cfg)

	switch strings.ToLower(*format) {
	case "yaml", "yml":
		fmt.Fprintf(e.out, "# effective configuration, from %s\n", configSource(cfg))
		enc := yaml.NewEncoder(e.out)
		enc.SetIndent(2)
		if err := enc.Encode(redacted); err != nil {
			return err
		}
		return enc.Close()
	case "json":
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(redacted)
	default:
		return usagef("config print", "--output %q is not a format; use yaml or json", *format)
	}
}

// secretPlaceholder stands in for a value that is set but must not be printed.
// Printing nothing at all would be ambiguous: an operator debugging a config
// needs to know whether a secret is configured, just not what it is.
const secretPlaceholder = "(set, not shown)"

// blankSecrets copies the configuration with every secret replaced. It works on
// a copy so that nothing downstream can accidentally use the blanked values.
func blankSecrets(cfg *config.Config) *config.Config {
	c := *cfg
	blank := func(s string) string {
		if strings.TrimSpace(s) == "" {
			return ""
		}
		return secretPlaceholder
	}
	c.Security.EncryptionKey = blank(c.Security.EncryptionKey)
	c.OIDC.ClientSecret = blank(c.OIDC.ClientSecret)
	c.Agent.JoinToken = blank(c.Agent.JoinToken)
	c.Agent.AgentToken = blank(c.Agent.AgentToken)
	return &c
}

// printFindings writes the validator's output in the shape an operator can act
// on: what is true, why it matters, and what to change.
func printFindings(w io.Writer, findings config.Findings) {
	if len(findings) == 0 {
		return
	}
	colour := isTerminal(w)
	for _, f := range findings {
		label := string(f.Severity)
		if colour {
			switch f.Severity {
			case config.SeverityError:
				label = colourise(colourRed, label)
			case config.SeverityWarning:
				label = colourise(colourYellow, label)
			default:
				label = colourise(colourDim, label)
			}
		}
		setting := f.Setting
		if setting != "" {
			setting = "  (" + setting + ")"
		}
		fmt.Fprintf(w, "[%s] %s%s\n", label, f.Title, setting)
		if f.Detail != "" {
			fmt.Fprintf(w, "        %s\n", f.Detail)
		}
		if f.Fix != "" {
			fmt.Fprintf(w, "        fix: %s\n", f.Fix)
		}
	}
	fmt.Fprintln(w)
}

// ---------------------------------------------------------------------------
// healthcheck
// ---------------------------------------------------------------------------

// runHealthcheck probes a controller's liveness endpoint. It is the container
// HEALTHCHECK, which is why it takes a URL rather than a config file: inside
// the image there is no configuration to read, and the point is to ask the
// process on the other end of the socket whether it is answering.
func runHealthcheck(ctx context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies healthcheck --url <url>",
		"Probe a controller's /healthz. Exits 0 when it answers, 1 when it does not.")
	target := fs.String("url", "", "the controller's base URL, e.g. http://127.0.0.1:8080 (required)")
	timeout := fs.Duration("timeout", 5*time.Second, "how long to wait for an answer")
	caFile := fs.String("ca-file", "", "PEM file holding the controller's certificate")
	insecure := fs.Bool("insecure", false, "do not verify the controller's certificate")
	fs.example(
		"zoomies healthcheck --url http://127.0.0.1:8080",
		`HEALTHCHECK CMD ["/usr/local/bin/zoomies", "healthcheck", "--url", "http://127.0.0.1:8080"]`,
	)
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if strings.TrimSpace(*target) == "" {
		return usagef("healthcheck", "needs --url, for example --url http://127.0.0.1:8080")
	}

	url := strings.TrimRight(*target, "/")
	if !strings.HasSuffix(url, "/healthz") {
		url += "/healthz"
	}

	client, err := httpClient(*caFile, *insecure, *timeout)
	if err != nil {
		return err
	}
	reqCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "zoomies-healthcheck/"+version.Version)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s did not answer: %w", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s: %s", url, resp.Status, strings.TrimSpace(string(body)))
	}
	fmt.Fprintf(e.out, "%s is healthy\n", url)
	return nil
}

// httpClient builds a client with the caller's TLS choices. It is shared by the
// health check and the API client so that --ca-file means the same thing in
// both.
func httpClient(caFile string, insecure bool, timeout time.Duration) (*http.Client, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}
	if caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("reading --ca-file %s: %w", caFile, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("--ca-file %s holds no PEM certificate; it should be the controller's certificate, not its key", caFile)
		}
		tlsCfg.RootCAs = pool
	}
	return &http.Client{
		// No Client.Timeout: a followed log tail and the event stream are idle
		// for minutes by design. Every request sets its own deadline instead.
		Transport: &http.Transport{
			TLSClientConfig:     tlsCfg,
			Proxy:               http.ProxyFromEnvironment,
			TLSHandshakeTimeout: timeout,
			ForceAttemptHTTP2:   true,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

// runVersion prints the build. --short is what install.sh parses; --json is
// what a fleet inventory script wants.
func runVersion(_ context.Context, e *env, args []string) error {
	fs := newFlagSet(e, "zoomies version [--short|--json]", "Print the version this binary was built from.")
	short := fs.Bool("short", false, "just the version and commit, on one line")
	asJSON := fs.Bool("json", false, "the whole build stamp as JSON")
	if err := fs.parse(args); err != nil {
		return err
	}
	if err := fs.noMoreArgs(); err != nil {
		return err
	}
	if *short && *asJSON {
		return usagef("version", "--short and --json ask for different things; pick one")
	}

	switch {
	case *short:
		fmt.Fprintln(e.out, version.Short())
	case *asJSON:
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]string{
			"version": version.Version,
			"commit":  version.Commit,
			"date":    version.Date,
			"go":      runtime.Version(),
			"os":      runtime.GOOS,
			"arch":    runtime.GOARCH,
		})
	default:
		fmt.Fprintln(e.out, version.String())
		if version.Date != "" {
			fmt.Fprintf(e.out, "built %s\n", version.Date)
		}
	}
	return nil
}

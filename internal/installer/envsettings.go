package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"gopkg.in/yaml.v3"
)

// A controller's settings live in its database, where the settings page
// changes them. A container deployment installed before that was so carries
// them in its .env instead, and a ZOOMIES_* variable wins over the database:
// every one of them shows on the settings page as locked, and a change made
// there never takes effect. So an upgrade finds them, says so, and with the
// operator's say moves them -- stores each value in the database, then
// comments the line out of .env and takes it out of the Compose file, so the
// controller comes back up on the same values from the place they belong.
//
// An agent keeps its environment: it has no database to move anything to.

// movedSetting is one variable a deployment's container is started with that
// sets a setting its database should hold.
type movedSetting struct {
	env     string
	setting config.Setting
	value   string
	// store is false for an empty value where the default is empty too:
	// there is nothing to keep, only a variable to stop setting.
	store bool
}

// shown is the value as the operator is told it, never a credential.
func (m movedSetting) shown() string {
	switch {
	case m.setting.Secret:
		return "(a credential, stored sealed)"
	case m.value == "":
		return "(empty)"
	}
	return m.value
}

// environmentSettings is every stored setting the deployment's container is
// started with. The running container's own environment is the answer, since
// it is what the controller reads -- whether it came from .env, from a
// literal in the Compose file, or from a docker run long ago. The .env is the
// fallback for a container that is not there to ask.
func (p *upgradePlan) environmentSettings(ctx context.Context) []movedSetting {
	if p.record.Mode == ModeAgent || !p.record.Deployment.Containerised() {
		return nil
	}
	var env []string
	switch {
	case p.replacement != nil:
		env = p.replacement.Env()
	default:
		out, err := p.docker(ctx, "inspect", "--type", "container", "--format", "{{json .Config.Env}}", containerOr(p.record))
		if err == nil {
			_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &env)
		}
	}
	vars := map[string]string{}
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			vars[k] = v
		}
	}
	if len(vars) == 0 && p.record.EnvFile != "" {
		if parsed, err := ParseEnvFile(p.record.EnvFile); err == nil {
			vars = parsed
		}
	}
	defaults := config.Default()
	var out []movedSetting
	for name, value := range vars {
		s, ok := config.SettingForEnv(name)
		if !ok {
			continue
		}
		keep := true
		if value == "" {
			if def, err := defaults.Value(s.Key); err == nil && config.Text(s, def) == "" {
				keep = false
			}
		}
		out = append(out, movedSetting{env: name, setting: s, value: value, store: keep})
	}
	sort.Slice(out, func(i, j int) bool { return config.CompareKeys(out[i].setting.Key, out[j].setting.Key) < 0 })
	return out
}

// settleSettings says which settings the deployment keeps in its environment
// and, with the operator's say, marks them to be moved when the service is
// replaced -- which is the one moment the controller is stopped, and so the
// one moment its database can be written.
func (p *upgradePlan) settleSettings(ctx context.Context) {
	moved := p.environmentSettings(ctx)
	if len(moved) == 0 {
		return
	}
	out := p.opts.Out
	fmt.Fprintf(out, "%s are set in the container environment and locked on the Settings page:\n",
		strconv.Itoa(len(moved))+" "+pluralise(len(moved), "setting"))
	for _, m := range moved {
		fmt.Fprintf(out, "  - %s (%s) = %s\n", m.env, m.setting.Key, m.shown())
	}
	where := p.record.EnvFile
	if p.record.Deployment == DeploymentCompose {
		where += " and " + p.record.ComposeFile()
	}
	fmt.Fprintf(out, "Move these values into the database to unlock them.\nOriginal environment entries are commented out in %s.\n", where)

	approved := false
	switch {
	case p.opts.Check:
		fmt.Fprintln(out, "The upgrade will offer to move them, and moves nothing without your approval.")
		return
	case p.opts.AssumeYes:
		approved = true
	case p.opts.Interactive && p.opts.In != nil:
		approved = askApproval(p.opts.In, out, "Move them into the database now? [Y/n] ")
	}
	if !approved {
		fmt.Fprintln(out, "Left where they are: the upgrade goes on, and they keep overriding the database.")
		fmt.Fprintln(out, "To move them, run: zoomies upgrade --yes  (as root, as the upgrade itself is)")
		return
	}
	p.move = moved
}

// importInput is what `zoomies config import-env` is handed: the settings to
// store, one per line.
func importInput(moved []movedSetting) (string, error) {
	var b strings.Builder
	for _, m := range moved {
		if !m.store {
			continue
		}
		quoted, err := quoteEnvValue(m.env, m.value)
		if err != nil {
			return "", err
		}
		b.WriteString(m.env + "=" + quoted + "\n")
	}
	return b.String(), nil
}

// moveComposeSettings stores the settings through a one-off container of the
// new image on the deployment's volume, then takes them out of .env and the
// Compose file. It runs with the controller stopped, between the pull and the
// recreate. It returns the files as they were, for a rollback to put back; a
// nil restore means nothing was moved, and why has been said.
func (p *upgradePlan) moveComposeSettings(ctx context.Context) (restore func() error) {
	envBefore, err := os.ReadFile(p.record.EnvFile)
	if err != nil {
		p.notMoved(err)
		return nil
	}
	envInfo, err := os.Stat(p.record.EnvFile)
	if err != nil {
		p.notMoved(err)
		return nil
	}
	composePath := p.record.ComposeFile()
	composeBefore, err := os.ReadFile(composePath)
	if err != nil {
		p.notMoved(err)
		return nil
	}
	// Both edits are worked out before anything is stored or written: a
	// Compose file that still names a variable .env no longer sets would hand
	// the controller an empty value, which is worse than moving nothing.
	now := time.Now().UTC()
	envAfter := commentOutEnv(envBefore, p.move, now)
	composeAfter, err := removeComposeEnv(composeBefore, p.move)
	if err != nil {
		p.notMoved(fmt.Errorf("reading %s: %w", composePath, err))
		return nil
	}
	input, err := importInput(p.move)
	if err != nil {
		p.notMoved(err)
		return nil
	}

	fmt.Fprintln(p.opts.Out, "Stopping the controller to move its settings into its database.")
	if _, err := p.compose(ctx, "stop", "--timeout", strconv.Itoa(int(serviceStopTimeout.Seconds())), "zoomies"); err != nil {
		p.notMoved(err)
		return nil
	}
	if input != "" {
		text, err := p.composeInput(ctx, input, "run", "--rm", "--no-deps", "-T", "zoomies", "config", "import-env")
		if err != nil {
			p.notMoved(err)
			return nil
		}
		p.echo(text)
	}

	mode := envInfo.Mode().Perm()
	backup := fmt.Sprintf("%s.bak.%s", composePath, now.Format("20060102T150405Z"))
	if err := os.WriteFile(backup, composeBefore, 0o640); err != nil {
		p.notMoved(fmt.Errorf("backing up %s: %w", composePath, err))
		return nil
	}
	if err := writeFileAtomic(p.record.EnvFile, envAfter, mode); err != nil {
		p.notMoved(err)
		return nil
	}
	if err := writeFileAtomic(composePath, composeAfter, 0o640); err != nil {
		_ = writeFileAtomic(p.record.EnvFile, envBefore, mode)
		p.notMoved(err)
		return nil
	}
	fmt.Fprintf(p.opts.Out, "Moved %s into the database and commented them out of %s; the Compose file as it was is at %s.\n",
		strconv.Itoa(len(p.move))+" "+pluralise(len(p.move), "setting"), p.record.EnvFile, backup)
	return func() error {
		return errors.Join(writeFileAtomic(p.record.EnvFile, envBefore, mode), writeFileAtomic(composePath, composeBefore, 0o640))
	}
}

// moveDockerSettings stores the settings through a one-off container of the
// new image with the stopped controller's volumes, and drops the variables
// from the replacement's environment. It reports whether they were moved;
// the environment file is commented once the replacement is running.
func (p *upgradePlan) moveDockerSettings(ctx context.Context) bool {
	input, err := importInput(p.move)
	if err != nil {
		p.notMoved(err)
		return false
	}
	if input != "" {
		args := []string{"run", "--rm", "-i", "--network", "none", "--volumes-from", p.replacement.ID}
		if exists(p.record.EnvFile) {
			// Where the encryption key is, for a credential to be sealed with.
			args = append(args, "--env-file", p.record.EnvFile)
		}
		args = append(args, p.image, "config", "import-env")
		text, err := p.dockerInput(ctx, input, args...)
		if err != nil {
			p.notMoved(err)
			return false
		}
		p.echo(text)
	}
	names := map[string]bool{}
	for _, m := range p.move {
		names[m.env] = true
	}
	if err := p.replacement.RemoveEnv(names); err != nil {
		p.notMoved(err)
		return false
	}
	return true
}

// commentDockerEnvFile comments the moved variables out of a docker
// deployment's environment file, which a later `zoomies init` or a hand-run
// docker run would otherwise read them back from.
func (p *upgradePlan) commentDockerEnvFile() {
	if !exists(p.record.EnvFile) {
		return
	}
	before, err := os.ReadFile(p.record.EnvFile)
	if err == nil {
		var info os.FileInfo
		if info, err = os.Stat(p.record.EnvFile); err == nil {
			err = writeFileAtomic(p.record.EnvFile, commentOutEnv(before, p.move, time.Now().UTC()), info.Mode().Perm())
		}
	}
	if err != nil {
		fmt.Fprintf(p.opts.Out, "The settings are in the database, but %s could not be updated (%v); comment out the ZOOMIES_* lines listed above by hand.\n", p.record.EnvFile, err)
		return
	}
	fmt.Fprintf(p.opts.Out, "Moved %s into the database and commented them out of %s.\n", strconv.Itoa(len(p.move))+" "+pluralise(len(p.move), "setting"), p.record.EnvFile)
}

func (p *upgradePlan) notMoved(err error) {
	fmt.Fprintf(p.opts.Out, "Could not move the settings into the database (%v); they stay in the environment, and the upgrade goes on.\n", err)
	p.move = nil
}

// echo passes on what the import said it stored, and not the lines Compose
// prints about creating its one-off container.
func (p *upgradePlan) echo(text string) {
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "Container ") {
			fmt.Fprintln(p.opts.Out, "  "+line)
		}
	}
}

func (p *upgradePlan) composeInput(ctx context.Context, input string, args ...string) (string, error) {
	name, argv := p.composeArgv(args...)
	return p.opts.runInput(ctx, input, name, argv...)
}

func (p *upgradePlan) dockerInput(ctx context.Context, input string, args ...string) (string, error) {
	name, argv := p.dockerArgv(args...)
	return p.opts.runInput(ctx, input, name, argv...)
}

// commentOutEnv comments out each moved variable's line, with a line above it
// saying where the setting went. A credential's value is not kept in the
// comment: the database has it, sealed, and a copy in plain text beside it
// would be the one an attacker reads.
func commentOutEnv(body []byte, moved []movedSetting, now time.Time) []byte {
	byName := map[string]movedSetting{}
	for _, m := range moved {
		byName[m.env] = m
	}
	lines := strings.Split(string(body), "\n")
	out := make([]string, 0, len(lines)+len(moved))
	for _, line := range lines {
		trimmed := strings.TrimPrefix(strings.TrimSpace(line), "export ")
		name, _, ok := strings.Cut(trimmed, "=")
		m, moving := byName[strings.TrimSpace(name)]
		if !ok || !moving || strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
			continue
		}
		out = append(out, fmt.Sprintf("# Commented out by zoomies upgrade on %s: %s lives in the database now; change it on the settings page.",
			now.Format("2006-01-02"), m.setting.Key))
		if m.setting.Secret {
			out = append(out, "# "+m.env+"=(stored sealed in the database)")
		} else {
			out = append(out, "# "+line)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// removeComposeEnv takes the moved variables out of the zoomies service's
// environment, and writes their values in wherever else the service used
// them -- a TLS certificate's mount above all, which an older file spelled
// ${ZOOMIES_TLS_CERT_FILE} and which would otherwise name nothing once .env
// stops setting it.
func removeComposeEnv(body []byte, moved []movedSetting) ([]byte, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("the Compose file is empty")
	}
	services := mappingValue(root.Content[0], "services")
	svc := mappingValue(services, "zoomies")
	if svc == nil || svc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("the Compose file has no zoomies service")
	}
	names := map[string]string{}
	for _, m := range moved {
		names[m.env] = m.value
	}
	var env *yaml.Node
	for i := 0; i+1 < len(svc.Content); i += 2 {
		if svc.Content[i].Value == "environment" {
			env = svc.Content[i+1]
		}
	}
	if env != nil {
		switch env.Kind {
		case yaml.MappingNode:
			kept := env.Content[:0]
			for i := 0; i+1 < len(env.Content); i += 2 {
				if _, moving := names[env.Content[i].Value]; moving {
					continue
				}
				kept = append(kept, env.Content[i], env.Content[i+1])
			}
			env.Content = kept
		case yaml.SequenceNode:
			kept := env.Content[:0]
			for _, item := range env.Content {
				name, _, _ := strings.Cut(item.Value, "=")
				if _, moving := names[name]; moving {
					continue
				}
				kept = append(kept, item)
			}
			env.Content = kept
		}
	}
	substitute(svc, env, names)

	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&root); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// interpolation matches a Compose reference to a variable: $NAME, ${NAME},
// and ${NAME} with a default or a required-message after it.
var interpolation = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(?:[:]?[-?+][^}]*)?\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// substitute writes the value in for every reference to a moved variable in
// the scalars under n, skipping the environment block, which has already had
// them taken out. A literal $ in a value is doubled, which is Compose's escape.
func substitute(n, skip *yaml.Node, values map[string]string) {
	if n == nil || n == skip {
		return
	}
	if n.Kind == yaml.ScalarNode {
		n.Value = interpolation.ReplaceAllStringFunc(n.Value, func(ref string) string {
			m := interpolation.FindStringSubmatch(ref)
			name := m[1]
			if name == "" {
				name = m[2]
			}
			v, moving := values[name]
			if !moving {
				return ref
			}
			return strings.ReplaceAll(v, "$", "$$")
		})
		return
	}
	for _, c := range n.Content {
		substitute(c, skip, values)
	}
}

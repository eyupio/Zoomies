package installer

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/config"
	"gopkg.in/yaml.v3"
)

// An upgrade replaces the binary or the image, and a release sometimes needs
// more of the host than the one that was installed: a folder, a mount, a file
// the installer used to write and somebody has since removed. Left to the next
// `zoomies init`, that is a deployment that upgrades cleanly and then quietly
// lacks what the new release expects -- a pool's tool cache that is never
// kept, with nothing to say why.
//
// So an upgrade looks for what this release expects and the deployment does
// not have, says what it would add, and adds it only with the operator's say:
// --yes, or an answer at the terminal. With neither -- a scheduled run, a
// pipeline -- it changes nothing beyond what it always changed, and says what
// is missing and the command that adds it. Nothing on the host changes
// without approval.

// layoutChange is one thing this release expects that the deployment lacks.
type layoutChange struct {
	// what is said to the operator: what would be added, and where.
	what string
	// required marks a change the upgrade cannot go on without. Only a
	// missing Compose file is: there is nothing to bring the new image up
	// with.
	required bool
	// sharedMount marks the change that gives a running container the shared
	// folder. Until the container is recreated it does not have it, which is what
	// the update helper's install looks for.
	sharedMount bool
	apply       func(ctx context.Context) error
}

// layoutChanges is everything this deployment lacks, for what it runs: the
// shared folder and the socket only where it runs runners, the certificate
// only where it serves TLS from files. What it runs is read from its own
// settings -- see deploymentSettings.
func (p *upgradePlan) layoutChanges(ctx context.Context) ([]layoutChange, error) {
	s := p.settings(ctx)
	if !s.read {
		fmt.Fprintf(p.opts.Out, "Could not read this deployment's settings from its database (%s), so the upgrade offers what a host that runs runners needs.\n", s.why)
	}
	switch p.record.Deployment {
	case DeploymentCompose:
		changes, err := p.composeLayoutChanges(s)
		return append(append(changes, p.hostHealthChanges(ctx, s)...), p.cliConfigChanges(s)...), err
	case DeploymentDocker:
		return append(append(p.dockerLayoutChanges(s), p.hostHealthChanges(ctx, s)...), p.cliConfigChanges(s)...), nil
	default:
		return append(p.nativeLayoutChanges(s), p.cliConfigChanges(s)...), nil
	}
}

// socketGroup is the group that owns the runtime's socket, 0 when it cannot
// be read.
func (p *upgradePlan) socketGroup(path string) int {
	if p.opts.socketGroup != nil {
		return p.opts.socketGroup(path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if _, g, ok := fileOwner(fi); ok && g > 0 {
		return g
	}
	return 0
}

// wantMount is one mount this release gives a container of this deployment.
type wantMount struct {
	// target is the path inside the container; alt is another spelling of
	// it an older Compose file may use, such as ${ZOOMIES_TLS_CERT_FILE}.
	target, alt string
	// bind is what is added: source:target[:mode].
	bind string
	why  string
	// covered marks a mount that any mount of a folder above it already
	// provides: a pool's cache under a folder the operator mounted is seen.
	covered bool
}

// isUnder reports whether p is dir or inside it. Both are paths inside a
// Linux container or on its host, so they are compared with forward slashes
// whatever this upgrade runs on.
func isUnder(p, dir string) bool {
	dir = path.Clean(dir)
	p = path.Clean(p)
	return p == dir || (dir != "/" && strings.HasPrefix(p, dir+"/"))
}

// wantedMounts is every mount the installer would give this deployment
// today, and the group that owns the runtime's socket, 0 when none is needed
// or it could not be read. It is the same decision ComposeFileSpecFor and
// DockerRunSpecFor take at install, taken from the running deployment's
// settings instead of a plan, which is what lets an upgrade find the mounts an
// older release never wrote.
func (p *upgradePlan) wantedMounts(s deploymentSettings) ([]wantMount, int) {
	var out []wantMount
	runs := s.runsRunners(p.record.Mode)
	if runs {
		out = append(out, wantMount{target: SharedHostDir, bind: SharedHostDir + ":" + SharedHostDir,
			why: "the shared folder, where pools keep their tool caches"})
	}
	gid := 0
	if sock := runtimeSocket(s.cfg); runs && s.cfg.Agent.Backend != "process" && sock != "" {
		out = append(out, wantMount{target: sock, bind: sock + ":" + sock,
			why: "the runtime's socket, which the agent creates runner containers on"})
		gid = p.socketGroup(sock)
	}
	if runs {
		// A size-limited pool cache outside the shared folder: the host's
		// daemon mounts it into runners without help, so the cache works --
		// but the agent in this container cannot see the folder, finds
		// nothing to measure, and the limit is never kept. Mounting it at its
		// own path is what makes the limit real.
		for _, dir := range s.cacheDirs {
			if isUnder(dir.path, SharedHostDir) {
				continue
			}
			out = append(out, wantMount{target: dir.path, bind: dir.path + ":" + dir.path, covered: true,
				why: fmt.Sprintf("the cache folder of pool %s, whose size limit the agent keeps by measuring it", strings.Join(dir.pools, ", "))})
		}
	}
	tls := s.cfg.Server.TLS
	if p.record.Mode != ModeAgent && tls.Mode == config.TLSFiles && tls.CertFile != "" && tls.KeyFile != "" {
		out = append(out,
			wantMount{target: tls.CertFile, alt: "${ZOOMIES_TLS_CERT_FILE}", bind: tls.CertFile + ":" + tls.CertFile + ":ro",
				why: "the TLS certificate the controller serves"},
			wantMount{target: tls.KeyFile, alt: "${ZOOMIES_TLS_KEY_FILE}", bind: tls.KeyFile + ":" + tls.KeyFile + ":ro",
				why: "the TLS certificate's key"})
	}
	return out, gid
}

func (p *upgradePlan) composeLayoutChanges(s deploymentSettings) ([]layoutChange, error) {
	var out []layoutChange
	path := p.record.ComposeFile()
	want, gid := p.wantedMounts(s)
	runs := s.runsRunners(p.record.Mode)
	body, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		spec := composeSpecFor(p.record, s, gid)
		out = append(out, layoutChange{
			what:     "write " + path + " again from this deployment's record and settings; it is missing, and the upgraded container is brought up with it",
			required: true,
			apply: func(context.Context) error {
				rendered, err := RenderComposeFile(spec)
				if err != nil {
					return err
				}
				return writeFileAtomic(path, []byte(rendered), 0o640)
			},
		})
	case err != nil:
		return nil, fmt.Errorf("installer: reading %s: %w", path, err)
	default:
		have, err := composeServiceMounts(body)
		if err != nil {
			return nil, fmt.Errorf("installer: %s is not a Compose file this release can read: %w", path, err)
		}
		var missing []wantMount
		for _, m := range want {
			if !have.mounts(m) {
				missing = append(missing, m)
			}
		}
		group := ""
		if gid > 0 && !have.groups[strconv.Itoa(gid)] && !have.groups["${DOCKER_GID}"] {
			group = strconv.Itoa(gid)
		}
		if len(missing) > 0 || group != "" {
			var items []string
			for _, m := range missing {
				items = append(items, fmt.Sprintf("mount %s (%s)", m.target, m.why))
			}
			if group != "" {
				items = append(items, fmt.Sprintf("add group %s, which owns the runtime's socket, so the image's account can use it", group))
			}
			out = append(out, layoutChange{
				what:        fmt.Sprintf("in %s, which an older release wrote: %s; the file is backed up first", path, strings.Join(items, "; ")),
				sharedMount: slices.ContainsFunc(missing, func(m wantMount) bool { return m.target == SharedHostDir }),
				apply: func(context.Context) error {
					return editComposeFile(path, missing, group)
				},
			})
		}
	}
	if runs {
		out = append(out, p.sharedFolderChanges(SharedHostDir, ImageUID, ImageUID)...)
	}
	return out, nil
}

func (p *upgradePlan) dockerLayoutChanges(s deploymentSettings) []layoutChange {
	var out []layoutChange
	want, gid := p.wantedMounts(s)
	if p.replacement != nil {
		for _, m := range want {
			if p.replacement.HasBind(m.target) || (m.covered && p.replacement.HasBindAbove(m.target)) {
				continue
			}
			bind := m.bind
			out = append(out, layoutChange{
				what:        fmt.Sprintf("mount %s in the replacement container (%s)", m.target, m.why),
				sharedMount: m.target == SharedHostDir,
				apply: func(context.Context) error {
					return p.replacement.AddBind(bind)
				},
			})
		}
		if group := strconv.Itoa(gid); gid > 0 && !p.replacement.HasGroup(group) {
			out = append(out, layoutChange{
				what: fmt.Sprintf("add group %s to the replacement container, which owns the runtime's socket", group),
				apply: func(context.Context) error {
					return p.replacement.AddGroup(group)
				},
			})
		}
	}
	if s.runsRunners(p.record.Mode) {
		out = append(out, p.sharedFolderChanges(SharedHostDir, ImageUID, ImageUID)...)
	}
	return out
}

// nativeLayoutChanges gives the shared folder to whoever owns the state
// directory beside it, which is the account the service runs as.
func (p *upgradePlan) nativeLayoutChanges(s deploymentSettings) []layoutChange {
	if !s.runsRunners(p.record.Mode) {
		return nil
	}
	dir := config.SharedDir()
	uid, gid := -1, -1
	if fi, err := os.Stat(filepath.Dir(dir)); err == nil {
		if u, g, ok := fileOwner(fi); ok {
			uid, gid = u, g
		}
	}
	return p.sharedFolderChanges(dir, uid, gid)
}

// sharedTarget is where the shared folder is and who has to own it.
type sharedTarget struct {
	dir      string
	uid, gid int
}

// sharedFolderChanges is the shared folder and its layout, where any of it is
// missing or the folder belongs to an account that cannot add to it.
func (p *upgradePlan) sharedFolderChanges(dir string, uid, gid int) []layoutChange {
	if s := p.opts.shared; s != nil {
		dir, uid, gid = s.dir, s.uid, s.gid
	}
	problems := SharedDirProblems(dir, uid)
	if len(problems) == 0 {
		return nil
	}
	owner := ""
	if uid >= 0 {
		owner = fmt.Sprintf(", owned by uid %d so Zoomies can add to it", uid)
	}
	return []layoutChange{{
		what: fmt.Sprintf("create %s and the folders in it (%s)%s", dir, strings.Join(problems, "; "), owner),
		apply: func(context.Context) error {
			if _, err := PrepareSharedDir(dir, uid, gid); err != nil {
				return err
			}
			// A folder that is there but belongs to somebody else is given
			// back; PrepareSharedDir leaves an existing one as it is.
			if uid >= 0 {
				if err := os.Lchown(dir, uid, gid); err != nil {
					return fmt.Errorf("installer: giving %s to uid %d: %w", dir, uid, err)
				}
			}
			return nil
		},
	}}
}

// composeSpecFor rebuilds the Compose file's spec from the deployment's record
// and settings, which is everything the installer rendered it from.
func composeSpecFor(rec DeploymentRecord, s deploymentSettings, gid int) ComposeFileSpec {
	mode := rec.Mode
	if mode == "" {
		mode = ModeSingle
	}
	runs := s.runsRunners(mode)
	socket := runtimeSocket(s.cfg)
	mountSocket := runs && s.cfg.Agent.Backend != "process" && socket != ""
	spec := ComposeFileSpec{
		Mode:          mode,
		ContainerPort: ContainerPort,
		Publish:       mode != ModeAgent,
		MountSocket:   mountSocket,
		SocketPath:    socket,
		GroupAdd:      mountSocket && gid > 0,
		Healthcheck:   mode != ModeAgent,
		MountShared:   runs,
		Container:     rec.Container,
		Volume:        rec.Volume,
		Network:       rec.Network,
	}
	if tls := s.cfg.Server.TLS; mode != ModeAgent && tls.Mode == config.TLSFiles {
		spec.TLSCertFile, spec.TLSKeyFile = tls.CertFile, tls.KeyFile
	}
	return spec
}

// runtimeSocket is the socket the deployment's agent creates runners on. An
// empty agent.docker_host is detection, which in a container finds Docker's
// standard socket, the one the installer mounts; Podman's is the user's own
// and is only known when it is set.
func runtimeSocket(cfg *config.Config) string {
	if cfg.Agent.DockerHost == "" && (cfg.Agent.Backend == "" || cfg.Agent.Backend == "docker") {
		return "/var/run/docker.sock"
	}
	return socketPath(cfg.Agent.DockerHost)
}

// serviceMounts is what the zoomies service of a Compose file mounts, by
// target, and the groups it adds.
type serviceMounts struct {
	targets map[string]bool
	groups  map[string]bool
	// samePath are host folders bound at their own path. Only those make a
	// folder inside them the same folder on both sides: a named volume at
	// /var/lib/zoomies hides the host's /var/lib/zoomies/anything.
	samePath map[string]bool
}

func (h serviceMounts) mounts(m wantMount) bool {
	if h.targets[m.target] || (m.alt != "" && h.targets[m.alt]) {
		return true
	}
	if m.covered {
		for dir := range h.samePath {
			if isUnder(m.target, dir) {
				return true
			}
		}
	}
	return false
}

// composeServiceMounts reads the zoomies service's volumes, in either of
// Compose's syntaxes, and its group_add.
func composeServiceMounts(body []byte) (serviceMounts, error) {
	var doc struct {
		Services map[string]struct {
			Volumes  []yaml.Node `yaml:"volumes"`
			GroupAdd []string    `yaml:"group_add"`
		} `yaml:"services"`
	}
	out := serviceMounts{targets: map[string]bool{}, groups: map[string]bool{}, samePath: map[string]bool{}}
	if err := yaml.Unmarshal(body, &doc); err != nil {
		return out, err
	}
	svc, ok := doc.Services["zoomies"]
	if !ok {
		return out, fmt.Errorf("it has no zoomies service")
	}
	for _, v := range svc.Volumes {
		switch v.Kind {
		case yaml.ScalarNode:
			if target, ok := shortMountTarget(v.Value); ok {
				out.targets[target] = true
				if source, _, _ := strings.Cut(v.Value, ":"); source == target && path.IsAbs(source) {
					out.samePath[path.Clean(source)] = true
				}
			}
		case yaml.MappingNode:
			var long struct {
				Type   string `yaml:"type"`
				Source string `yaml:"source"`
				Target string `yaml:"target"`
			}
			if err := v.Decode(&long); err == nil && long.Target != "" {
				out.targets[long.Target] = true
				if long.Type == "bind" && long.Source == long.Target && path.IsAbs(long.Source) {
					out.samePath[path.Clean(long.Source)] = true
				}
			}
		}
	}
	for _, g := range svc.GroupAdd {
		out.groups[strings.TrimSpace(g)] = true
	}
	return out, nil
}

// shortMountTarget is the target of a "source:target[:mode]" volume. A
// ${VARIABLE} source or target is kept as written, which is how a file this
// release wrote names the TLS files.
func shortMountTarget(v string) (string, bool) {
	var parts []string
	depth := 0
	start := 0
	for i, r := range v {
		switch {
		case r == '{':
			depth++
		case r == '}':
			depth--
		case r == ':' && depth == 0:
			parts = append(parts, v[start:i])
			start = i + 1
		}
	}
	parts = append(parts, v[start:])
	if len(parts) < 2 {
		return "", false
	}
	return parts[1], true
}

// editComposeFile adds mounts and a group to the zoomies service, keeping a
// copy of the file as it was. It edits the file rather than writing it again
// from the template: an operator's own additions -- another service, a label,
// a resource limit -- are theirs, and an upgrade that silently dropped them
// would be a worse problem than the one it solved.
func editComposeFile(path string, mounts []wantMount, group string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	edited, err := addComposeMounts(body, mounts, group)
	if err != nil {
		return err
	}
	backup := fmt.Sprintf("%s.bak.%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.WriteFile(backup, body, 0o640); err != nil {
		return fmt.Errorf("installer: backing up %s: %w", path, err)
	}
	return writeFileAtomic(path, edited, 0o640)
}

// addComposeMounts is the edit itself, on the parsed document, so that
// comments and every other key survive it.
func addComposeMounts(body []byte, mounts []wantMount, group string) ([]byte, error) {
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
		return nil, fmt.Errorf("the Compose file has no zoomies service to add to")
	}
	if len(mounts) > 0 {
		volumes := sequenceUnder(svc, "volumes")
		if volumes == nil {
			return nil, fmt.Errorf("the zoomies service's volumes are not a list")
		}
		for _, m := range mounts {
			volumes.Content = append(volumes.Content, &yaml.Node{
				Kind: yaml.ScalarNode, Value: m.bind,
				HeadComment: "Added by zoomies upgrade: " + m.why + ".",
			})
		}
	}
	if group != "" {
		groups := sequenceUnder(svc, "group_add")
		if groups == nil {
			return nil, fmt.Errorf("the zoomies service's group_add is not a list")
		}
		groups.Content = append(groups.Content, &yaml.Node{
			Kind: yaml.ScalarNode, Value: group, Style: yaml.DoubleQuotedStyle,
			HeadComment: "Added by zoomies upgrade: the group that owns the runtime's socket.",
		})
	}
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

// sequenceUnder is the list under key in a mapping, created empty when the
// key is missing, or nil when the key holds something other than a list.
func sequenceUnder(m *yaml.Node, key string) *yaml.Node {
	v := mappingValue(m, key)
	if v == nil {
		m.Content = append(m.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Value: key},
			&yaml.Node{Kind: yaml.SequenceNode})
		return m.Content[len(m.Content)-1]
	}
	if v.Kind != yaml.SequenceNode {
		return nil
	}
	return v
}

// mappingValue is the value under key in a mapping node, or nil.
func mappingValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

package hosttune

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

type Snapshot struct {
	Exists bool        `json:"exists"`
	Data   []byte      `json:"data,omitempty"`
	Mode   fs.FileMode `json:"mode"`
	UID    int         `json:"uid"`
	GID    int         `json:"gid"`
}
type FileChange struct {
	Path   string      `json:"path"`
	Before Snapshot    `json:"before"`
	After  []byte      `json:"after"`
	Mode   fs.FileMode `json:"mode"`
}
type Operation struct {
	Do       []string `json:"do,omitempty"`
	Undo     []string `json:"undo,omitempty"`
	Path     string   `json:"path,omitempty"`
	Value    string   `json:"value,omitempty"`
	Previous string   `json:"previous,omitempty"`
}
type UnitChange struct {
	Name          string `json:"name"`
	BeforeEnabled string `json:"before_enabled"`
	BeforeActive  string `json:"before_active"`
	AfterEnabled  string `json:"after_enabled"`
	AfterActive   string `json:"after_active"`
}
type Change struct {
	Units         []UnitChange `json:"units,omitempty"`
	ID            string       `json:"id"`
	Previous      string       `json:"previous"`
	New           string       `json:"new"`
	Files         []FileChange `json:"files,omitempty"`
	Operations    []Operation  `json:"operations,omitempty"`
	DockerRestart bool         `json:"docker_restart,omitempty"`
	Notice        string       `json:"notice,omitempty"`
	At            time.Time    `json:"at"`
	Actor         string       `json:"actor"`
	Phase         string       `json:"phase"`
}
type State struct {
	DockerRestartPending bool     `json:"docker_restart_pending"`
	Version              int      `json:"version"`
	Changes              []Change `json:"changes"`
}

func snapshot(e *Engine, p string) (Snapshot, error) {
	i, err := e.System.Stat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return Snapshot{}, nil
	}
	if err != nil {
		return Snapshot{}, err
	}
	if !i.Mode().IsRegular() {
		return Snapshot{}, fmt.Errorf("refusing non-regular file %s", p)
	}
	b, err := e.System.ReadFile(p)
	uid, gid := fileOwner(i)
	return Snapshot{Exists: true, Data: b, Mode: i.Mode().Perm(), UID: uid, GID: gid}, err
}
func snapshotMode(s Snapshot) fs.FileMode {
	if s.Exists {
		return s.Mode
	}
	return 0644
}
func fileChange(e *Engine, p, s string) (FileChange, error) {
	b, err := snapshot(e, p)
	if managed(string(b.Data)) {
		return FileChange{}, fmt.Errorf("%s is managed elsewhere", p)
	}
	mode := fs.FileMode(0644)
	if b.Exists {
		mode = b.Mode
	}
	return FileChange{Path: p, Before: b, After: []byte(s), Mode: mode}, err
}
func (e *Engine) LoadState() (State, error) {
	s := State{Version: 1, Changes: []Change{}}
	b, err := e.System.ReadFile(StatePath)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err = json.Unmarshal(b, &s); err != nil {
		return s, err
	}
	if s.Version != 1 {
		return s, fmt.Errorf("unsupported tuning state version %d", s.Version)
	}
	return s, nil
}
func (e *Engine) saveState(s State) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return e.System.WriteFile(StatePath, append(b, '\n'), 0600)
}
func (e *Engine) Plan(ctx context.Context, r Result) (Change, error) {
	c, ok := e.Check(r.ID)
	if !ok || c.Plan == nil {
		return Change{}, fmt.Errorf("%s is report-only", r.ID)
	}
	if !e.Supported() || e.Container || e.UID != 0 {
		return Change{}, fmt.Errorf("tuning requires root on a supported Linux host outside a container")
	}
	fresh := c.Detect(ctx, e)
	if c.ID == "kernel.hwe-install" && strings.HasPrefix(fresh.Reason, "optional install") {
		fresh.Reason = ""
	}
	if fresh.Status != Warn || fresh.Reason != "" {
		return Change{}, fmt.Errorf("%s cannot be changed: %s (%s)", r.ID, fresh.Status, fresh.Reason)
	}
	fresh.ID = c.ID
	fresh.Recommended = r.Recommended
	return c.Plan(ctx, e, fresh)
}

// Preview is deliberately literal: all bytes removed/added and every command
// are visible, rather than an abbreviated description hiding configuration.
func (c Change) Preview() string {
	var b bytes.Buffer
	for _, f := range c.Files {
		fmt.Fprintf(&b, "--- %s (before)\n+++ %s (after)\n", f.Path, f.Path)
		for _, l := range bytes.Split(bytes.TrimSuffix(f.Before.Data, []byte("\n")), []byte("\n")) {
			fmt.Fprintf(&b, "-%s\n", l)
		}
		for _, l := range bytes.Split(bytes.TrimSuffix(f.After, []byte("\n")), []byte("\n")) {
			fmt.Fprintf(&b, "+%s\n", l)
		}
	}
	for _, o := range c.Operations {
		if o.Path != "" {
			fmt.Fprintf(&b, "write %s: %q -> %q\n", o.Path, o.Previous, o.Value)
		} else {
			fmt.Fprintf(&b, "command: %q\nrevert: %q\n", o.Do, o.Undo)
		}
	}
	if c.Notice != "" {
		fmt.Fprintln(&b, c.Notice)
	}
	return b.String()
}
func (e *Engine) verifyRevert(ctx context.Context, c Change) error {
	for _, u := range c.Units {
		en, ac, err := unitState(ctx, e, u.Name)
		if err != nil {
			return err
		}
		allowedEnabled := en == u.AfterEnabled || (c.Phase == "pending" && (en == u.BeforeEnabled || en == "disabled"))
		allowedActive := ac == u.AfterActive || (c.Phase == "pending" && ac == u.BeforeActive)
		if !allowedEnabled || !allowedActive {
			return fmt.Errorf("%s was changed outside Zoomies; refusing to overwrite it", u.Name)
		}
	}

	for _, op := range c.Operations {
		p, after, before := op.Path, op.Value, op.Previous
		if len(op.Do) >= 3 && op.Do[0] == "sysctl" {
			key, val, _ := strings.Cut(op.Do[2], "=")
			p = "/proc/sys/" + strings.ReplaceAll(key, ".", "/")
			after = val
			_, before, _ = strings.Cut(op.Undo[2], "=")
		}
		if p != "" {
			v, err := read(e, p)
			if err != nil {
				return err
			}
			if v != after && !(c.Phase == "pending" && v == before) {
				return fmt.Errorf("%s was changed outside Zoomies; refusing to overwrite it", p)
			}
		}
		if len(op.Undo) > 2 && op.Undo[0] == "apt-get" {
			running, err := command(ctx, e, "uname", "-r")
			if err != nil {
				return err
			}
			for _, pkg := range op.Undo[4:] {
				if strings.Contains(pkg, running) {
					return fmt.Errorf("the installed HWE kernel is running; boot the previous kernel before reverting")
				}
			}
			plan, err := command(ctx, e, "apt-get", append([]string{"--simulate", "remove", "--"}, op.Undo[4:]...)...)
			if err != nil {
				return fmt.Errorf("cannot verify package reversal")
			}
			allowed := map[string]bool{}
			for _, pkg := range op.Undo[4:] {
				allowed[pkg] = true
			}
			for _, l := range strings.Split(plan, "\n") {
				f := strings.Fields(l)
				if len(f) > 1 && ((f[0] == "Remv" && !allowed[f[1]]) || f[0] == "Inst") {
					return fmt.Errorf("package reversal would change an unrelated package: %s", f[1])
				}
			}
		}
	}
	return nil
}
func (e *Engine) execute(ctx context.Context, o Operation, undo bool) error {
	if o.Path != "" {
		v := o.Value
		if undo {
			v = o.Previous
		}
		return e.System.WriteValue(o.Path, v)
	}
	a := o.Do
	if undo {
		a = o.Undo
	}
	if len(a) == 0 {
		return nil
	}
	out, err := e.System.Run(ctx, a[0], a[1:]...)
	if err != nil {
		return fmt.Errorf("%s: %s: %w", a[0], out, err)
	}
	return nil
}

// Apply writes the complete reversal plan before any host mutation. A pending
// record is retained after any failure so a later revert can recover it.
func (e *Engine) Apply(ctx context.Context, c Change, actor string) error {
	if !e.Supported() || e.Container || e.UID != 0 {
		return fmt.Errorf("tuning requires root on a supported Linux host outside a container")
	}
	unlock, err := e.System.Lock(StateDir + "/lock")
	if err != nil {
		return err
	}
	defer unlock()
	s, err := e.LoadState()
	if err != nil {
		return err
	}
	for _, old := range s.Changes {
		if old.Phase == "pending" {
			return fmt.Errorf("an interrupted change needs --revert first: %s", old.ID)
		}
		if old.ID == c.ID && old.Phase == "applied" {
			return nil
		}
	}
	for _, f := range c.Files {
		now, err := snapshot(e, f.Path)
		if err != nil {
			return err
		}
		if now.Exists != f.Before.Exists || !bytes.Equal(now.Data, f.Before.Data) || (now.Exists && (now.Mode != f.Before.Mode || now.UID != f.Before.UID || now.GID != f.Before.GID)) {
			return fmt.Errorf("%s changed after preview; run tune again", f.Path)
		}
	}
	c.At = e.Now().UTC()
	c.Actor = actor
	c.Phase = "pending"
	if c.DockerRestart {
		s.DockerRestartPending = true
	}
	s.Changes = append(s.Changes, c)
	if err = e.saveState(s); err != nil {
		return err
	}
	for i, f := range c.Files {
		if f.Before.Exists {
			backup := filepath.Join(StateDir, "backups", fmt.Sprintf("%d-%s-%d", c.At.UnixNano(), c.ID, i))
			if err = e.System.WriteFile(backup, f.Before.Data, 0600); err != nil {
				return err
			}
		}
		if err = e.System.WriteFile(f.Path, f.After, f.Mode); err != nil {
			return err
		}
	}
	for _, o := range c.Operations {
		if err = e.execute(ctx, o, false); err != nil {
			return err
		}
	}
	s.Changes[len(s.Changes)-1].Phase = "applied"
	return e.saveState(s)
}
func (e *Engine) Revert(ctx context.Context, only, skip map[string]bool, dry bool) ([]Change, error) {
	if !e.Supported() || e.Container || e.UID != 0 {
		return nil, fmt.Errorf("revert requires root on a supported Linux host")
	}
	var unlock func()
	var err error
	if !dry {
		unlock, err = e.System.Lock(StateDir + "/lock")
		if err != nil {
			return nil, err
		}
		defer unlock()
	}
	s, err := e.LoadState()
	if err != nil {
		return nil, err
	}
	var out []Change
	virtual := map[string]Snapshot{}
	// Shared drop-ins must unwind in reverse order; a selective revert of an
	// earlier edit is refused if a later live edit touched the same file.
	for i := len(s.Changes) - 1; i >= 0; i-- {
		c := s.Changes[i]
		if c.Phase == "reverted" || skip[c.ID] || (len(only) > 0 && !only[c.ID]) {
			continue
		}
		for _, f := range c.Files {
			for j := i + 1; j < len(s.Changes); j++ {
				if s.Changes[j].Phase == "reverted" {
					continue
				}
				for _, later := range s.Changes[j].Files {
					if later.Path == f.Path {
						return out, fmt.Errorf("revert %s first because it also edits %s", s.Changes[j].ID, f.Path)
					}
				}
			}
			now, err := snapshot(e, f.Path)
			if dry {
				if v, ok := virtual[f.Path]; ok {
					now = v
					err = nil
				}
			}
			if err != nil {
				return out, err
			}
			if (!bytes.Equal(now.Data, f.After) && !(now.Exists == f.Before.Exists && bytes.Equal(now.Data, f.Before.Data))) || (now.Exists && f.Before.Exists && (now.UID != f.Before.UID || now.GID != f.Before.GID || now.Mode != f.Mode)) {
				return out, fmt.Errorf("%s was changed outside Zoomies; refusing to overwrite it", f.Path)
			}
		}
		if err = e.verifyRevert(ctx, c); err != nil {
			return out, err
		}
		out = append(out, c)
		if dry {
			for _, f := range c.Files {
				virtual[f.Path] = f.Before
			}
			s.Changes[i].Phase = "reverted"
			continue
		}
		for _, f := range c.Files {
			if f.Before.Exists {
				err = e.System.WriteFile(f.Path, f.Before.Data, f.Before.Mode)
			} else {
				err = e.System.Remove(f.Path)
				if errors.Is(err, fs.ErrNotExist) {
					err = nil
				}
			}
			if err != nil {
				return out, err
			}
		}
		for j := len(c.Operations) - 1; j >= 0; j-- {
			if err = e.execute(ctx, c.Operations[j], true); err != nil {
				return out, err
			}
		}
		s.Changes[i].Phase = "reverted"
		if c.DockerRestart {
			s.DockerRestartPending = true
		}
		if err = e.saveState(s); err != nil {
			return out, err
		}
	}
	return out, nil
}

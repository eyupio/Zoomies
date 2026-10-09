//go:build unix

package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// installerFor is the installer as `zoomies init` has it by the time it has
// a service to offer the helper for: the offer host's systemd, unit directory
// and recorded commands, and a native install of the plan's shape.
func (h *offerHost) installerFor(configure func(o *Options, i *Installer)) (*Installer, Plan) {
	i := &Installer{
		opts: Options{
			ConfigDir: h.configDir, Out: h.out,
			helperHost: h.opts.helperHost, run: h.runner.run,
		},
		out: h.out, ui: newUI(h.out),
	}
	configure(&i.opts, i)
	i.in = i.opts.In
	return i, Plan{Deployment: DeploymentNative, ConfigDir: h.configDir, Mode: ModeSingle}
}

func yes() *bool { v := true; return &v }

// The helper is a grant of root, so init never adds it unless somebody says so
// in a way that is about the helper: the question, --update-helper, or the
// answer file's update_helper. --yes accepts the install's confirmations in
// advance and is none of those.
// saidTimes is how often the sentence that says what the helper is for is expected:
// once in the question when one is asked, and once in the hint that follows a
// no or an unattended run.
func saidTimes(asks bool, says string) int {
	n := 0
	if asks {
		n++
	}
	if says == addLater {
		n++
	}
	return n
}

func TestInitDoesNotInstallTheHelperUnlessAsked(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(o *Options, i *Installer)
		answer    string
		installed bool
		asks      bool
		says      string
	}{
		{name: "enter", configure: func(o *Options, i *Installer) { i.interactive = true }, answer: "\n", asks: true, says: addLater},
		{name: "end of input", configure: func(o *Options, i *Installer) { i.interactive = true }, asks: true, says: addLater},
		{name: "n", configure: func(o *Options, i *Installer) { i.interactive = true }, answer: "n\n", asks: true, says: addLater},
		{name: "y", configure: func(o *Options, i *Installer) { i.interactive = true }, answer: "y\n", asks: true, installed: true},
		{name: "yes", configure: func(o *Options, i *Installer) { i.interactive = true }, answer: "yes\n", asks: true, installed: true},
		{name: "--yes alone", configure: func(o *Options, i *Installer) { o.AssumeYes = true }, says: addLater},
		{name: "--yes at the terminal and enter", configure: func(o *Options, i *Installer) { o.AssumeYes, i.interactive = true, true }, answer: "\n", asks: true, says: addLater},
		{name: "--yes with an answer waiting and no terminal", configure: func(o *Options, i *Installer) { o.AssumeYes = true }, answer: "y\n", says: addLater},
		{name: "--update-helper", configure: func(o *Options, i *Installer) { o.UpdateHelper = true }, installed: true},
		{name: "--update-helper without a terminal", configure: func(o *Options, i *Installer) { o.UpdateHelper, o.NonInteractive = true, true }, installed: true},
		{name: "the answer file's update_helper", configure: func(o *Options, i *Installer) { i.answers = &Answers{UpdateHelper: yes()} }, installed: true},
		{name: "an answer file that does not mention it", configure: func(o *Options, i *Installer) { i.answers = &Answers{} }, says: addLater},
		{name: "an answer file that says false", configure: func(o *Options, i *Installer) {
			no := false
			i.answers = &Answers{UpdateHelper: &no}
		}, says: addLater},
		{name: "unattended", configure: func(o *Options, i *Installer) { o.NonInteractive = true }, says: addLater},
		{name: "unattended with --yes", configure: func(o *Options, i *Installer) { o.NonInteractive, o.AssumeYes = true, true }, says: addLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			i, plan := h.installerFor(tc.configure)
			i.opts.In, i.in = strings.NewReader(tc.answer), strings.NewReader(tc.answer)
			i.stepUpdateHelper(context.Background(), plan)

			out := h.out.String()
			if asked := strings.Contains(out, "[y/N]"); asked != tc.asks {
				t.Errorf("asked = %v, want %v:\n%s", asked, tc.asks, out)
			}
			if tc.says != "" && !strings.Contains(out, tc.says) {
				t.Errorf("output does not say %q:\n%s", tc.says, out)
			}
			if got, want := strings.Count(out, UpdateHelperExplained), saidTimes(tc.asks, tc.says); got != want {
				t.Errorf("%q is said %d times, want %d:\n%s", UpdateHelperExplained, got, want, out)
			}
			if tc.asks {
				for _, want := range []string{"sudo zoomies updates helper remove", "as root", "validated request", "by writing a request", "web UI"} {
					if !strings.Contains(out, want) {
						t.Errorf("the question does not say %q:\n%s", want, out)
					}
				}
			}
			if got := h.installed(); got != tc.installed {
				t.Fatalf("installed = %v, want %v:\n%s", got, tc.installed, out)
			}
			if !tc.installed {
				h.installedNothing(t)
				if h.ranHelperCommands() {
					t.Errorf("a helper that was not added still ran %v", h.runner.lines())
				}
			}
		})
	}
}

// What the install does not offer, it does not mention: a host without systemd
// cannot have the units, a helper that is there needs no offer, and a service
// that runs as root, or none at all, has nothing for the helper to serve.
func TestInitSaysNothingWhereTheHelperHasNothingToOffer(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, h *offerHost)
	}{
		{name: "no systemd", setup: func(t *testing.T, h *offerHost) {
			h.opts.helperHost.systemdDir = filepath.Join(h.base, "no-systemd")
		}},
		{name: "already installed", setup: func(t *testing.T, h *offerHost) { h.install(t) }},
		{name: "a service that runs as root", setup: func(t *testing.T, h *offerHost) {
			h.opts.helperHost.resolve = func(string) (InstallHelperOptions, error) {
				return InstallHelperOptions{}, helperNotApplicable{os.ErrNotExist}
			}
		}},
	} {
		for _, interactive := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "", true: " at a terminal"}[interactive], func(t *testing.T) {
				h := newOfferHost(t)
				tc.setup(t, h)
				i, plan := h.installerFor(func(o *Options, i *Installer) { i.interactive = interactive })
				i.opts.In, i.in = strings.NewReader("y\n"), strings.NewReader("y\n")
				calls := len(h.runner.calls)
				i.stepUpdateHelper(context.Background(), plan)
				if out := h.out.String(); strings.Contains(out, "update helper") || strings.Contains(out, "[y/N]") {
					t.Errorf("said something about a helper there is nothing to offer:\n%s", out)
				}
				if ran := h.runner.lines()[calls:]; len(ran) != 0 {
					t.Errorf("ran %v", ran)
				}
			})
		}
	}
}

// --update-helper on a host that cannot take it is not ignored in silence.
func TestInitSaysWhyAHelperAskedForByNameCannotBeAdded(t *testing.T) {
	h := newOfferHost(t)
	h.opts.helperHost.systemdDir = filepath.Join(h.base, "no-systemd")
	i, plan := h.installerFor(func(o *Options, i *Installer) { o.UpdateHelper = true })
	i.stepUpdateHelper(context.Background(), plan)
	if out := h.out.String(); !strings.Contains(out, "does not run systemd") || !strings.Contains(out, addLater[len("Add later: "):]) {
		t.Errorf("--update-helper was ignored without a word:\n%s", out)
	}
	if h.installed() {
		t.Error("the helper's units were written on a host without systemd")
	}
}

// A refusal after a yes is the helper's own sentence, said once, with the
// command to retry. The install is finished by then and is no worse for it, so
// the step has no error to return.
func TestInitReportsARefusedHelperAndGoesOn(t *testing.T) {
	for _, mode := range []string{"asked", "--update-helper", "the answer file"} {
		t.Run(mode, func(t *testing.T) {
			h := newOfferHost(t)
			// Anybody can write the folder above the binary, so the helper would
			// refuse to run it as root.
			mustDo(t, os.Chmod(filepath.Dir(h.binary), 0o777))
			i, plan := h.installerFor(func(o *Options, i *Installer) {
				switch mode {
				case "asked":
					i.interactive = true
				case "--update-helper":
					o.UpdateHelper = true
				default:
					i.answers = &Answers{UpdateHelper: yes()}
				}
			})
			i.opts.In, i.in = strings.NewReader("y\n"), strings.NewReader("y\n")
			plan.ConfigDir = "/srv/zoomies/etc"
			i.stepUpdateHelper(context.Background(), plan)
			out := h.out.String()
			for _, want := range []string{"writable by the world", "The update helper was not added", "sudo zoomies updates helper install --config-dir /srv/zoomies/etc"} {
				if !strings.Contains(out, want) {
					t.Errorf("output does not say %q:\n%s", want, out)
				}
			}
			h.installedNothing(t)
		})
	}
}

// At a terminal a service that cannot be resolved for a reason the operator can
// put right is said in one line; unattended it is not.
func TestInitHintsAtAResolutionTheOperatorCanPutRight(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		h := newOfferHost(t)
		h.opts.helperHost.resolve = func(string) (InstallHelperOptions, error) {
			return InstallHelperOptions{}, os.ErrPermission
		}
		i, plan := h.installerFor(func(o *Options, i *Installer) { i.interactive = interactive })
		i.opts.In, i.in = strings.NewReader("y\n"), strings.NewReader("y\n")
		i.stepUpdateHelper(context.Background(), plan)
		if said := strings.Contains(h.out.String(), "cannot be offered on this host"); said != interactive {
			t.Errorf("interactive = %v, said = %v:\n%s", interactive, said, h.out)
		}
	}
}

// A container that runs no runners does not mount the shared folder, so the
// helper would only refuse it. It is not offered there unless asked for by
// name, which gets the helper's own sentence about why not.
func TestInitDoesNotOfferTheHelperToAControllerOnlyContainer(t *testing.T) {
	for _, tc := range []struct {
		name      string
		embedded  bool
		mode      Mode
		requested bool
		offered   bool
	}{
		{name: "a controller only", mode: ModeController},
		{name: "a controller with its agent", mode: ModeSingle, embedded: true, offered: true},
		{name: "a runner host", mode: ModeAgent, offered: true},
		{name: "a controller only, asked for by name", mode: ModeController, requested: true, offered: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			i, plan := h.installerFor(func(o *Options, i *Installer) { i.interactive = true; o.UpdateHelper = tc.requested })
			i.opts.In, i.in = strings.NewReader("n\n"), strings.NewReader("n\n")
			plan.Deployment, plan.Mode, plan.Embedded = DeploymentCompose, tc.mode, tc.embedded
			i.stepUpdateHelper(context.Background(), plan)
			if said := strings.Contains(h.out.String(), "update helper") || strings.Contains(h.out.String(), "[y/N]"); said != tc.offered {
				t.Errorf("offered = %v, want %v:\n%s", said, tc.offered, h.out)
			}
		})
	}
}

// agent join's --yes means one thing: replace the credentials this host
// already has. It is not an answer to the helper question either.
func TestAgentJoinOffersTheHelperAndDefaultsToNo(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(o *JoinOptions)
		answer    string
		installed bool
		asks      bool
		says      string
	}{
		{name: "enter", configure: func(o *JoinOptions) { o.Interactive = true }, answer: "\n", asks: true, says: addLater},
		{name: "end of input", configure: func(o *JoinOptions) { o.Interactive = true }, asks: true, says: addLater},
		{name: "n", configure: func(o *JoinOptions) { o.Interactive = true }, answer: "n\n", asks: true, says: addLater},
		{name: "y", configure: func(o *JoinOptions) { o.Interactive = true }, answer: "y\n", asks: true, installed: true},
		{name: "--yes alone", configure: func(o *JoinOptions) { o.AssumeYes = true }, says: addLater},
		{name: "--yes at the terminal and enter", configure: func(o *JoinOptions) { o.AssumeYes, o.Interactive = true, true }, answer: "\n", asks: true, says: addLater},
		{name: "--yes with an answer waiting and no terminal", configure: func(o *JoinOptions) { o.AssumeYes = true }, answer: "y\n", says: addLater},
		{name: "--update-helper", configure: func(o *JoinOptions) { o.UpdateHelper = true }, installed: true},
		{name: "--update-helper without a terminal", configure: func(o *JoinOptions) { o.UpdateHelper, o.NonInteractive = true, true }, installed: true},
		{name: "--non-interactive", configure: func(o *JoinOptions) { o.Interactive, o.NonInteractive = true, true }, answer: "y\n", says: addLater},
		{name: "no terminal", configure: func(o *JoinOptions) {}, answer: "y\n", says: addLater},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newOfferHost(t)
			opts := JoinOptions{
				ConfigDir: h.configDir, Out: h.out, In: strings.NewReader(tc.answer),
				helperHost: h.opts.helperHost, run: h.runner.run,
			}
			tc.configure(&opts)
			opts.offerUpdateHelper(context.Background())

			out := h.out.String()
			if asked := strings.Contains(out, "[y/N]"); asked != tc.asks {
				t.Errorf("asked = %v, want %v:\n%s", asked, tc.asks, out)
			}
			if tc.says != "" && !strings.Contains(out, tc.says+" --config-dir "+h.configDir) {
				t.Errorf("output does not say %q:\n%s", tc.says, out)
			}
			if got, want := strings.Count(out, UpdateHelperExplained), saidTimes(tc.asks, tc.says); got != want {
				t.Errorf("%q is said %d times, want %d:\n%s", UpdateHelperExplained, got, want, out)
			}
			if got := h.installed(); got != tc.installed {
				t.Fatalf("installed = %v, want %v:\n%s", got, tc.installed, out)
			}
			if !tc.installed {
				h.installedNothing(t)
			}
		})
	}
}

// The same checks the upgrade makes: a host without systemd, or that has the
// helper, is offered nothing, and a refusal does not fail the join, which has
// redeemed its single-use token and written the service by then.
func TestAgentJoinSaysNothingWhereTheHelperHasNothingToOfferAndReportsARefusal(t *testing.T) {
	t.Run("no systemd", func(t *testing.T) {
		h := newOfferHost(t)
		h.opts.helperHost.systemdDir = filepath.Join(h.base, "no-systemd")
		opts := JoinOptions{ConfigDir: h.configDir, Out: h.out, In: strings.NewReader("y\n"), Interactive: true, helperHost: h.opts.helperHost, run: h.runner.run}
		opts.offerUpdateHelper(context.Background())
		if out := h.out.String(); out != "" {
			t.Errorf("said something on a host without systemd:\n%s", out)
		}
	})
	t.Run("no systemd, asked for by name", func(t *testing.T) {
		h := newOfferHost(t)
		h.opts.helperHost.systemdDir = filepath.Join(h.base, "no-systemd")
		opts := JoinOptions{ConfigDir: h.configDir, Out: h.out, UpdateHelper: true, helperHost: h.opts.helperHost, run: h.runner.run}
		opts.offerUpdateHelper(context.Background())
		if out := h.out.String(); !strings.Contains(out, "does not run systemd") || !strings.Contains(out, "The update helper was not added") {
			t.Errorf("--update-helper was ignored without a word on a host without systemd:\n%s", out)
		}
		h.installedNothing(t)
	})
	t.Run("already installed", func(t *testing.T) {
		h := newOfferHost(t)
		h.install(t)
		calls := len(h.runner.calls)
		opts := JoinOptions{ConfigDir: h.configDir, Out: h.out, In: strings.NewReader("y\n"), Interactive: true, helperHost: h.opts.helperHost, run: h.runner.run}
		opts.offerUpdateHelper(context.Background())
		if out := h.out.String(); strings.Contains(out, "update helper") || len(h.runner.calls) != calls {
			t.Errorf("offered an installed helper again:\n%s", out)
		}
	})
	t.Run("refused after a yes", func(t *testing.T) {
		h := newOfferHost(t)
		mustDo(t, os.Chmod(filepath.Dir(h.binary), 0o777))
		opts := JoinOptions{ConfigDir: h.configDir, Out: h.out, In: strings.NewReader("y\n"), Interactive: true, helperHost: h.opts.helperHost, run: h.runner.run}
		opts.offerUpdateHelper(context.Background())
		out := h.out.String()
		for _, want := range []string{"writable by the world", "The update helper was not added", "sudo zoomies updates helper install --config-dir " + h.configDir} {
			if !strings.Contains(out, want) {
				t.Errorf("output does not say %q:\n%s", want, out)
			}
		}
		h.installedNothing(t)
	})
}

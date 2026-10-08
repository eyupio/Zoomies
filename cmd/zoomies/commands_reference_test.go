package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The reference the skills carry is generated from the binary, so the one
// thing it must do is list every command the binary has -- except itself,
// which is a generator and not something an agent runs on a fleet.
func TestCommandsListsEveryTopLevelCommandExceptItself(t *testing.T) {
	out, _ := runCLI(t, "commands", "--output", "json")
	var docs []commandDoc
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	var got, want []string
	for _, d := range docs {
		got = append(got, d.Name)
		if strings.TrimSpace(d.Help) == "" {
			t.Errorf("%s has no help text", d.Name)
		}
	}
	for _, c := range commands() {
		if c.name != "commands" {
			want = append(want, c.name)
		}
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("commands = %v\nwant %v", got, want)
	}
	// A group's subcommands come with it, each with the help the binary prints.
	for _, d := range docs {
		if d.Name == "pools" && len(d.Subcommands) == 0 {
			t.Errorf("pools has no subcommands listed")
		}
		for _, s := range d.Subcommands {
			if strings.TrimSpace(s.Help) == "" || s.Brief == "" {
				t.Errorf("%s %s: brief %q, help %q", d.Name, s.Name, s.Brief, s.Help)
			}
		}
	}
}

// The reference is committed and published, so a help text that quoted the
// machine it was generated on -- a home directory, a temp path -- would ship
// that machine's layout to everyone.
func TestCommandsHelpTextsHoldNoPathsFromThisMachine(t *testing.T) {
	out, _ := runCLI(t, "commands", "--output", "json")
	// /tmp itself is in several help texts on purpose (it is a folder a pool
	// can keep in memory), so the temporary directory is not a needle; the
	// working directory and the home directory are what would give the
	// generating machine away.
	wd, _ := os.Getwd()
	for _, needle := range []string{wd, os.Getenv("HOME")} {
		if needle != "" && needle != "/" && strings.Contains(out, needle) {
			t.Errorf("the help texts mention %q", needle)
		}
	}
}

// skills/zoomies/reference.md is what an agent reads instead of running
// --help on everything; it is generated, and this is what keeps it so.
func TestTheCommandReferenceMatchesTheBinary(t *testing.T) {
	// The reference is the Linux binary's help, which is where Zoomies is
	// operated from; Windows prints the same pinned default with its own
	// separator (\etc\zoomies), so the comparison is a Linux one and the
	// Windows job checks the rest of this package.
	if runtime.GOOS == "windows" {
		t.Skip("the command reference is generated from and compared with the Linux binary")
	}
	want, err := os.ReadFile("../../skills/zoomies/reference.md")
	if err != nil {
		t.Fatal(err)
	}
	// The command pins its own environment while it collects the help, so no
	// harness is needed here; a Windows checkout turns the file's line endings
	// into CRLF, which is not a difference in what the binary says.
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	var out, errOut bytes.Buffer
	e := &env{out: &out, err: &errOut, in: strings.NewReader("")}
	if code := dispatch(context.Background(), e, []string{"commands", "--output", "markdown"}); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if out.String() != string(want) {
		t.Fatalf("skills/zoomies/reference.md is out of date; run `make generate` from the repository root and commit the result\n%s", firstDifference(string(want), out.String()))
	}
}

// firstDifference names the first line the two texts disagree on, so a stale
// reference says which help text moved rather than only that one did.
func firstDifference(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	for i := 0; i < len(al) && i < len(bl); i++ {
		if al[i] != bl[i] {
			return "line " + strconv.Itoa(i+1) + ":\n  file:   " + al[i] + "\n  binary: " + bl[i]
		}
	}
	return "the file has " + strconv.Itoa(len(al)) + " lines and the binary prints " + strconv.Itoa(len(bl))
}

// A subcommand whose name and arguments leave fewer than two spaces before its
// brief does not match the line the generator reads, and drops out of the
// reference with everything under it and no error. Every line a group lists is
// therefore one the generator reads.
func TestEverySubcommandAGroupListsIsInTheReference(t *testing.T) {
	docs := collectCommandDocs(context.Background())
	byName := map[string]commandDoc{}
	for _, d := range docs {
		byName[d.Name] = d
	}
	for _, c := range commands() {
		if c.name == "commands" {
			continue
		}
		var listed []string
		in := false
		for _, line := range strings.Split(byName[c.name].Help, "\n") {
			switch {
			case line == "Subcommands:":
				in = true
			case in && strings.HasPrefix(line, "  "):
				listed = append(listed, strings.Fields(line)[0])
			case in:
				in = false
			}
		}
		var got []string
		for _, s := range byName[c.name].Subcommands {
			got = append(got, s.Name)
		}
		if strings.Join(got, ",") != strings.Join(listed, ",") {
			t.Errorf("zoomies %s lists %v, and the reference has %v", c.name, listed, got)
		}
	}
}

package store

import (
	"strings"
	"testing"
)

func TestNewRunnerNameIsBrandedAndShort(t *testing.T) {
	seen := map[string]bool{}
	for range 200 {
		name := NewRunnerName()
		if !strings.HasPrefix(name, "zoomies-") {
			t.Fatalf("runner name %q is not branded", name)
		}
		if got := len(name); got != len("zoomies-")+8 {
			t.Fatalf("runner name %q is %d characters, want %d", name, got, len("zoomies-")+8)
		}
		if seen[name] {
			t.Fatalf("runner name %q was minted twice in 200 tries", name)
		}
		seen[name] = true
		if !IsRunnerName(name) {
			t.Fatalf("IsRunnerName(%q) = false, want true for a name we just minted", name)
		}
	}
}

func TestIsRunnerNameRejectsSomebodyElsesRunner(t *testing.T) {
	for _, name := range []string{"", "runner-01", "gh-zoomies-1", "zoomies", "azoomies-x"} {
		if IsRunnerName(name) {
			t.Errorf("IsRunnerName(%q) = true, want false: the reaper would delete a registration it does not own", name)
		}
	}
	// GitHub does not promise the case it echoes back, and deleting the wrong
	// runner is worse than being generous here.
	if !IsRunnerName("Zoomies-A3F9QZ2M") {
		t.Error("IsRunnerName does not recognise its own name in a different case")
	}
}

func TestBrandLabelsAlwaysCarryTheBrand(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{"zoomies"}},
		{"adds the brand", []string{"gpu"}, []string{"gpu", "zoomies"}},
		{"does not duplicate it", []string{"Zoomies", "gpu"}, []string{"gpu", "zoomies"}},
		{"normalises the rest", []string{" GPU ", "gpu"}, []string{"gpu", "zoomies"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BrandLabels(c.in)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("BrandLabels(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestBrandedLabel(t *testing.T) {
	cases := []struct{ in, want string }{
		{"linux-x64", "zoomies-linux-x64"},
		{"GPU", "zoomies-gpu"},
		{"Ubuntu 24.04", "zoomies-ubuntu-24-04"},
		{"zoomies-gpu", "zoomies-gpu"},
		{"zoomies", "zoomies"},
		{"", "zoomies"},
		{"---", "zoomies"},
		{strings.Repeat("a", 60), "zoomies-" + strings.Repeat("a", 40)},
	}
	for _, c := range cases {
		if got := BrandedLabel(c.in); got != c.want {
			t.Errorf("BrandedLabel(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestSanitizeLabelCollapsesRunsOfPunctuation(t *testing.T) {
	if got := SanitizeLabel("big  builders!!"); got != "big-builders" {
		t.Fatalf("SanitizeLabel = %q, want %q", got, "big-builders")
	}
}

package api

import (
	"slices"
	"testing"

	"github.com/eyupio/zoomies/internal/controller"
	"gopkg.in/yaml.v3"
)

// The explanation's vocabulary is a closed set written in Go, and the contract
// repeats it so a client can switch on a value and a generated type can name it.
// Two copies of a list drift, and a class the contract does not name reaches a
// client as a string its type says cannot exist. This holds the contract to the
// code, in both directions, the way the fault kinds are held to theirs.
func TestTheContractNamesEveryWordTheExplainerCanSay(t *testing.T) {
	raw, err := openapiSpec()
	if err != nil {
		t.Fatalf("openapiSpec: %v", err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]struct {
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parsing the contract: %v", err)
	}
	enum := func(schema, property string) []string {
		t.Helper()
		got := doc.Components.Schemas[schema].Properties[property].Enum
		if len(got) == 0 {
			t.Fatalf("components.schemas.%s.properties.%s has no enum", schema, property)
		}
		return got
	}

	classes := make([]string, 0)
	for _, c := range controller.JobClasses() {
		classes = append(classes, string(c))
	}
	steps := []string{string(controller.StepRead), string(controller.StepChange), string(controller.StepRerun)}
	confidences := []string{string(controller.ConfidenceHigh), string(controller.ConfidenceMedium), string(controller.ConfidenceLow)}

	for _, c := range []struct {
		what           string
		contract, code []string
	}{
		{"JobExplanation.class", enum("JobExplanation", "class"), classes},
		{"JobExplanation.confidence", enum("JobExplanation", "confidence"), confidences},
		{"JobEvidence.kind", enum("JobEvidence", "kind"), controller.EvidenceKinds()},
		{"JobNextStep.kind", enum("JobNextStep", "kind"), steps},
	} {
		want, got := slices.Clone(c.code), slices.Clone(c.contract)
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(want, got) {
			t.Errorf("%s: the contract says %v and the code says %v; change both, or a client meets a word its type does not name", c.what, got, want)
		}
	}
}

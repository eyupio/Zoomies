package aicontext

import "testing"

func TestContextExclusionsApplyToNestedAndRootFilesBeforeAdmission(t *testing.T) {
	cfg := DefaultConfig("main")
	for _, test := range []struct {
		pattern, path string
		excluded      bool
	}{
		{"**/dist/**", "dist/main.js", true}, {"**/dist/**", "web/dist/main.js", true},
		{"**/**/generated?.go", "generated1.go", true}, {"**/**/generated?.go", "a/b/generated2.go", true},
		{"src/*.go", "src/deep/main.go", true}, {"src/*.go", "README.md", false},
		{"**/[!a]*.go", "b.go", true}, {"**/[!a]*.go", "a.go", false},
		{"literal[.go", "literal[.go", true}, {"**/*.go", "src/café.go", true},
		{"[]].go", "].go", true}, {"[!]].go", "a.go", true},
	} {
		cfg.Exclude = []string{test.pattern}
		if err := cfg.CheckSourceFiles([]File{{Path: test.path}}); (err != nil) != test.excluded {
			t.Errorf("pattern %q path %q: %v", test.pattern, test.path, err)
		}
	}
}

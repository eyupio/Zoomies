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

// A text file over 1 MiB makes generation refuse the whole run, so the defaults
// must cover the artefacts that usually cause it rather than leave every
// operator to find them from a failed workflow.
func TestDefaultExclusionsCoverBulkyArtefactsAndKeepCredentialPaths(t *testing.T) {
	cfg := DefaultConfig("main")
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"assets/mermaid.min.js", "static/app.min.css", "web/app.js.map", "README.drawio",
		"logs/run.log", "console.bak", ".env", "keys/server.pem", "vendor/x/y.go",
	} {
		if err := cfg.CheckSourceFiles([]File{{Path: path}}); err == nil {
			t.Errorf("%s should be excluded by default", path)
		}
	}
	for _, path := range []string{"main.go", "web/app.js", "docs/readme.md", "log.go", "backup.go"} {
		if err := cfg.CheckSourceFiles([]File{{Path: path}}); err != nil {
			t.Errorf("%s should not be excluded by default: %v", path, err)
		}
	}
}

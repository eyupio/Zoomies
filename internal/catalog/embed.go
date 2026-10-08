package catalog

import (
	_ "embed"
	"encoding/json"
)

// catalog.json is written by gen_catalog.go and checked in, so the binary
// serves the same document the site publishes; TestTheCatalogIsCurrent fails
// the build when it falls behind the page or the registry.
//
//go:embed catalog.json
var embedded []byte

// Embedded returns the generated catalog exactly as it was written.
func Embedded() []byte { return embedded }

// Current parses the embedded catalog.
func Current() (*Catalog, error) {
	var c Catalog
	if err := json.Unmarshal(embedded, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Marshal renders a catalog the one way it is ever written, so the generator,
// the test that checks it and anything that rewrites it agree byte for byte.
func Marshal(c *Catalog) ([]byte, error) {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

package controller

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/eyupio/zoomies/internal/updates"
	"github.com/eyupio/zoomies/internal/updates/channel"
	"github.com/eyupio/zoomies/internal/version"
)

// UpdatesSeedEnvVar names the environment variable that gives a controller a
// release list of its own to be behind, for the browser suite's Updates
// project.
//
// The binary that suite drives is a dev build, which no release comparison
// accepts, so without this the Updates page could only ever say that the build
// is left alone. The value is not a flag: it is the folder the controller will
// do its updating through, which the suite makes and names, and unset or empty
// switches the fixture off. Like the other seeds it is not a setting, and
// nothing in a real deployment's configuration reads it.
const UpdatesSeedEnvVar = "ZOOMIES_SEED_UPDATES"

// updatesSeedVersion is the release the seeded controller says it was built
// from, one behind both of the releases it is offered.
const updatesSeedVersion = "1.3.0"

// updatesSeedReleases are the two releases the fixture offers, newest first as
// GitHub lists them. v1.3.1 has been public for longer than a day and v1.3.2 for
// less, which is the pair the design uses to show what auto's wait costs: the
// newer release restarts it, and the older is passed over for it.
var updatesSeedReleases = []struct {
	tag string
	age time.Duration
}{
	{"v1.3.2", 6 * time.Hour},
	{"v1.3.1", 30 * time.Hour},
}

// updatesSeedRequested reports the folder the fixture was given, or false when
// it was not asked for.
func updatesSeedRequested() (folder string, ok bool) {
	folder = strings.TrimSpace(os.Getenv(UpdatesSeedEnvVar))
	return folder, folder != ""
}

// seedUpdates makes a dev build a release behind two others, and reads their
// list once so the page has something to show before the first scheduled check.
//
// It also stands in for the update helper. The folder it was given becomes the
// controller's update folder and holds the marker an installed helper leaves, so
// the Update button is offered and a press writes its request where the suite
// can read it. Nothing answers that request: the suite plays the helper by
// writing a result.json of its own, which is how it makes an attempt end.
//
// That check is half a minute after start and the page is opened sooner; a
// status that says the list has not been read yet is true, and is not what the
// browser suite is there to look at. It does nothing unless the fixture was
// asked for.
func (c *Controller) seedUpdates(ctx context.Context) error {
	folder, ok := updatesSeedRequested()
	if !ok {
		return nil
	}
	binary := updates.AssetName(runtime.GOOS, runtime.GOARCH)
	if binary == "" {
		return fmt.Errorf("no release is published for %s/%s, so the fixture has none to offer; run it on linux or darwin",
			runtime.GOOS, runtime.GOARCH)
	}

	now := c.Now()
	listed := make([]map[string]any, 0, len(updatesSeedReleases))
	for _, r := range updatesSeedReleases {
		listed = append(listed, map[string]any{
			"tag_name":     r.tag,
			"html_url":     "https://example.invalid/releases/" + r.tag,
			"published_at": now.Add(-r.age).UTC().Format(time.RFC3339),
			"prerelease":   false,
			"draft":        false,
			// Complete for the system the controller runs on, which is the one
			// it is asked about.
			"assets": []map[string]string{{"name": "checksums.txt"}, {"name": binary}},
		})
	}
	body, err := json.Marshal(listed)
	if err != nil {
		return fmt.Errorf("writing the fixture's release list: %w", err)
	}

	// The client the release check and the capacity-demand events share, with
	// only its transport changed, so that whatever else leaves by it still does.
	client := *c.httpClient
	next := client.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	client.Transport = releaseListFixture{body: body, next: next}
	c.httpClient = &client
	// Written before the loops start, so nothing is reading them.
	version.Version = updatesSeedVersion
	c.updateFolder = folder
	if err := writeFixtureMarker(folder, now); err != nil {
		return err
	}

	if err := c.checkForRelease(ctx); err != nil {
		return fmt.Errorf("reading the fixture's release list: %w", err)
	}
	c.log.Info("seeded the update fixture", "env", UpdatesSeedEnvVar,
		"update_folder", folder, "running", updatesSeedVersion)
	return nil
}

// writeFixtureMarker leaves the marker the installer writes last, which is all
// the controller looks for to call the helper installed.
func writeFixtureMarker(folder string, now time.Time) error {
	body, err := json.Marshal(channel.Marker{
		V: updates.WireVersion, Version: updatesSeedVersion,
		Binary: "/nonexistent/zoomies-fixture", InstalledAt: now.UTC(),
	})
	if err != nil {
		return fmt.Errorf("writing the fixture's helper marker: %w", err)
	}
	if err := os.WriteFile(filepath.Join(folder, channel.MarkerFile), body, 0o600); err != nil {
		return fmt.Errorf("writing the fixture's helper marker into %s, which the browser suite makes before it starts the controller: %w", folder, err)
	}
	return nil
}

// releaseListFixture answers the request for the release list from a list made
// once, and sends every other request on to the transport the controller had.
type releaseListFixture struct {
	body []byte
	next http.RoundTripper
}

func (f releaseListFixture) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.String() != releaseListURL {
		return f.next.RoundTrip(r)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(f.body)),
		Request:    r,
	}, nil
}

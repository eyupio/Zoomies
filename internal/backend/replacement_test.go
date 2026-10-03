package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestContainerReplacementKeepsCredentialsPortsVolumesAndIsolation(t *testing.T) {
	var created map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			_, _ = w.Write([]byte(`{"Id":"0123456789abcdef","Name":"/zoomies","Config":{"Image":"agent:old","Env":["TOKEN=keep-this"],"User":"65532","Healthcheck":{"Test":["NONE"]}},"HostConfig":{"AutoRemove":false,"Binds":["/socket:/socket"],"PortBindings":{"8080/tcp":[{"HostIp":"127.0.0.1","HostPort":"8081"}]},"SecurityOpt":["no-new-privileges:true"],"CustomFutureOption":true},"Mounts":[{"Type":"volume","Name":"existing-data","Destination":"/var/lib/zoomies","RW":true}],"State":{"Running":true},"NetworkSettings":{"Networks":{"zoomies":{"Aliases":["zoomies","0123456789ab"],"IPAddress":"172.18.0.4","EndpointID":"old-endpoint","IPAMConfig":null}}}}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/create") {
			if r.URL.Query().Get("name") != "zoomies" {
				t.Error("wrong name")
			}
			if err := json.NewDecoder(r.Body).Decode(&created); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"Id":"new"}`))
			return
		}
		if r.Method == http.MethodDelete {
			if r.URL.Query().Get("v") != "0" {
				t.Error("removed data volumes")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		t.Errorf("unexpected request %s", r.URL)
		w.WriteHeader(500)
	}))
	defer server.Close()
	client, err := NewAPIClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := client.PrepareReplacement(context.Background(), "zoomies", "agent:new")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.CreateReplacement(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if created["Image"] != "agent:new" || created["User"] != "65532" || !reflect.DeepEqual(created["Env"], []any{"TOKEN=keep-this"}) {
		t.Fatalf("lost configuration: %v", created)
	}
	host := created["HostConfig"].(map[string]any)
	if host["CustomFutureOption"] != true || host["PortBindings"] == nil || host["SecurityOpt"] == nil {
		t.Fatalf("lost runtime options: %v", host)
	}
	if !reflect.DeepEqual(host["Binds"], []any{"/socket:/socket", "existing-data:/var/lib/zoomies:rw"}) {
		t.Fatalf("lost data volume: %v", host)
	}
	endpoints := created["NetworkingConfig"].(map[string]any)["EndpointsConfig"].(map[string]any)
	network := endpoints["zoomies"].(map[string]any)
	if network["IPAddress"] != nil || network["EndpointID"] != nil || !reflect.DeepEqual(network["Aliases"], []any{"zoomies"}) {
		t.Fatalf("copied runtime network identity: %v", network)
	}
	if err := client.RemoveContainerKeepingVolumes(context.Background(), snapshot.ID, false); err != nil {
		t.Fatal(err)
	}
}

func TestAnAutomaticallyRemovedContainerIsRefusedBeforeAnUpgradeCanStopIt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatal("preflight mutated a container")
		}
		_, _ = w.Write([]byte(`{"Id":"old","Config":{"Image":"old"},"HostConfig":{"AutoRemove":true}}`))
	}))
	defer server.Close()
	client, _ := NewAPIClient(server.URL)
	if _, err := client.PrepareReplacement(context.Background(), "old", "new"); err == nil {
		t.Fatal("accepted a container that would destroy the rollback copy when stopped")
	}
}

// An upgrade adds the shared folder to a container that was created before
// there was one. The bind is added beside what the container already had;
// losing an operator's own mount on the way would be the worse outcome.
func TestAReplacementGainsABindAndKeepsItsOthers(t *testing.T) {
	r := &ContainerReplacement{body: map[string]json.RawMessage{
		"HostConfig": json.RawMessage(`{"Binds":["/socket:/socket:ro"],"Mounts":[{"Type":"bind","Source":"/data","Target":"/mnt/data"}],"CustomFutureOption":true}`),
	}}
	for target, want := range map[string]bool{"/socket": true, "/mnt/data": true, "/var/lib/zoomies/shared": false} {
		if got := r.HasBind(target); got != want {
			t.Errorf("HasBind(%s) = %v, want %v", target, got, want)
		}
	}
	if err := r.AddBind("/var/lib/zoomies/shared:/var/lib/zoomies/shared"); err != nil {
		t.Fatal(err)
	}
	if !r.HasBind("/var/lib/zoomies/shared") {
		t.Error("the added bind is not there")
	}
	var host map[string]any
	if err := json.Unmarshal(r.body["HostConfig"], &host); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host["Binds"], []any{"/socket:/socket:ro", "/var/lib/zoomies/shared:/var/lib/zoomies/shared"}) {
		t.Errorf("binds = %v", host["Binds"])
	}
	if host["CustomFutureOption"] != true || host["Mounts"] == nil {
		t.Errorf("lost host options: %v", host)
	}
}

// The group that owns the runtime's socket is how the image's account reaches
// it; a container created without it is given it, and keeps its other groups.
func TestAReplacementGainsAGroupAndKeepsItsOthers(t *testing.T) {
	r := &ContainerReplacement{body: map[string]json.RawMessage{
		"HostConfig": json.RawMessage(`{"GroupAdd":["100"],"CustomFutureOption":true}`),
	}}
	if !r.HasGroup("100") || r.HasGroup("998") {
		t.Fatal("HasGroup misread the existing groups")
	}
	if err := r.AddGroup("998"); err != nil {
		t.Fatal(err)
	}
	var host map[string]any
	if err := json.Unmarshal(r.body["HostConfig"], &host); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(host["GroupAdd"], []any{"100", "998"}) || host["CustomFutureOption"] != true {
		t.Errorf("host config = %v", host)
	}
}

// A pool's cache folder needs no mount of its own when the container already
// sees it through a host folder bound at its own path -- and only then: a
// volume, or a bind to another path, hides the host's folder instead.
func TestAFolderIsSeenOnlyThroughASamePathBindAboveIt(t *testing.T) {
	r := &ContainerReplacement{body: map[string]json.RawMessage{
		"HostConfig": json.RawMessage(`{"Binds":["zoomies-data:/var/lib/zoomies","/srv/mounted:/srv/mounted","/opt/x:/elsewhere"],"Mounts":[{"Type":"bind","Source":"/data","Target":"/data"},{"Type":"volume","Source":"v","Target":"/vol"}]}`),
	}}
	for target, want := range map[string]bool{
		"/srv/mounted/cache":   true,
		"/data/cache":          true,
		"/srv/mounted":         false, // the folder itself is HasBind's question
		"/var/lib/zoomies/c":   false, // under a named volume
		"/elsewhere/cache":     false, // under a bind from another host path
		"/vol/cache":           false,
		"/srv/mounted-other/x": false,
	} {
		if got := r.HasBindAbove(target); got != want {
			t.Errorf("HasBindAbove(%s) = %v, want %v", target, got, want)
		}
	}
}

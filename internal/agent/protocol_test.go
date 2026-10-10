package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func updateTask(id, updateID, tag string) Task {
	return Task{ID: id, Kind: TaskUpdateAgent, UpdateID: updateID, UpdateTag: tag}
}

func TestValidateTaskAcceptsAnUpdateTaskWithAnIDAndAValidTag(t *testing.T) {
	if err := validateTask(updateTask("task-1", "upd_1", "v1.3.5")); err != nil {
		t.Errorf("an update task naming an attempt and a release tag was refused: %v", err)
	}
}

// The controller never sends these, so the refusal is read in an agent's log by
// whoever is working out why a host did not update, and has to say what was wrong
// and what to do about it.
func TestValidateTaskRefusesAnUpdateTaskWithABadTag(t *testing.T) {
	for _, tag := range []string{"", "latest", "dev", "1.3.5", "V1.3.5", "v1.3", "v1.3.5-rc1", " v1.3.5", "v1.3.5\n", "v1.3.5; reboot"} {
		t.Run(tag, func(t *testing.T) {
			err := validateTask(updateTask("task-1", "upd_1", tag))
			if err == nil {
				t.Fatalf("an update task for the tag %q was accepted", tag)
			}
			if !strings.Contains(err.Error(), "vMAJOR.MINOR.PATCH") {
				t.Errorf("the refusal does not say what a tag looks like: %v", err)
			}
		})
	}
}

func TestValidateTaskRefusesAnUpdateTaskWithNoID(t *testing.T) {
	err := validateTask(updateTask("task-1", "", "v1.3.5"))
	if err == nil {
		t.Fatal("an update task with no update ID was accepted, so its result could not be matched to an attempt")
	}
	if !strings.Contains(err.Error(), "update ID") {
		t.Errorf("the refusal does not name the missing field: %v", err)
	}
}

// ProtocolVersion stays 1, so an older peer has to see exactly what it always
// saw: a task that is not an update, and a heartbeat that reports none, must
// not grow a key.
func TestTheNewFieldsAreOmittedFromOlderShapes(t *testing.T) {
	task := Task{ID: "task-1", Kind: TaskCheckHost, IssuedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	got, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"task-1","kind":"check_host","issued_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Errorf("a task without the update fields encodes as\n%s\nwant\n%s", got, want)
	}

	beat, err := json.Marshal(HeartbeatRequest{ProtocolVersion: ProtocolVersion, Version: "1.3.5"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(beat), `"update"`) {
		t.Errorf("a heartbeat with no update to report carries the key: %s", beat)
	}

	// The answer grows update_held, which an older agent has never heard of: a
	// controller holding nothing sends no key, and one holding a report sends a
	// key the older agent's decoder passes over.
	answer, err := json.Marshal(HeartbeatResponse{OK: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(answer), `"update_held"`) {
		t.Errorf("an answer that holds nothing carries the key: %s", answer)
	}
	var older struct {
		OK       bool `json:"ok"`
		Cordoned bool `json:"cordoned"`
	}
	if err := json.Unmarshal([]byte(`{"ok":true,"cordoned":false,"update_held":true}`), &older); err != nil || !older.OK {
		t.Errorf("an older agent decoding an answer that holds its report got %+v, %v", older, err)
	}
}

func TestAnUpdateReportRoundTripsOnAHeartbeat(t *testing.T) {
	in := HeartbeatRequest{ProtocolVersion: ProtocolVersion, Update: &UpdateReport{
		ID: "upd_1", OK: false, Tag: "v1.3.5", From: "1.3.4", Error: "the checksum did not match",
		FinishedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
	}}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out HeartbeatRequest
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.Update == nil || *out.Update != *in.Update {
		t.Errorf("update report = %+v, want %+v", out.Update, in.Update)
	}
	if strings.Contains(string(raw), `"to"`) {
		t.Errorf("an empty To is on the wire: %s", raw)
	}
}

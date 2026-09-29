package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// installationHistory is one installation with a little of everything the
// set covers: a pool, a runner that has gone (and so has a session), one still
// here, a job with an event, a delivery, a scaling event, a capacity sample,
// an audit row naming each kind, and a rolled-up usage day.
type installationHistory struct {
	inst   *Installation
	pool   *Pool
	gone   *Runner
	live   *Runner
	job    *Job
	target string
}

func seedInstallationHistory(t *testing.T, s *Store, now *time.Time, host *Host, target string, jobID int64) installationHistory {
	t.Helper()
	ctx := context.Background()
	inst := &Installation{AppID: 1, InstallationID: jobID, Target: target, TargetType: TargetOrg,
		PrivateKeyEnc: []byte("sealed-" + target), WebhookSecretEnc: []byte("secret-" + target)}
	if err := s.CreateInstallation(ctx, inst); err != nil {
		t.Fatalf("CreateInstallation: %v", err)
	}
	pool := &Pool{Name: target + "-linux", InstallationID: inst.ID, Labels: StringSlice{target},
		Backend: BackendDocker, MaxRunners: 4, Ephemeral: true, DockerMode: DockerNone, Enabled: true}
	if err := s.CreatePool(ctx, pool); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	pricedPool(t, s, pool, 0.5)
	start := time.Date(2026, 5, 9, 9, 0, 0, 0, time.UTC)
	gone := confirmed(t, s, now, pool, host, target+"-gone", start, start.Add(90*time.Minute))
	*now = start.Add(26 * time.Hour)
	live := &Runner{PoolID: pool.ID, HostID: host.ID, Name: target + "-live"}
	if err := s.CreateRunner(ctx, live); err != nil {
		t.Fatalf("CreateRunner: %v", err)
	}
	job, err := s.UpsertJob(ctx, &Job{GitHubJobID: jobID, Repo: target + "/app", Workflow: "build",
		State: JobQueued, InstallationID: inst.ID, PoolID: pool.ID, QueuedAt: *now})
	if err != nil {
		t.Fatalf("UpsertJob: %v", err)
	}
	if err := s.AppendJobEvent(ctx, &JobEvent{JobID: job.ID, Kind: "queued", Source: "webhook", At: *now}); err != nil {
		t.Fatalf("AppendJobEvent: %v", err)
	}
	if err := s.RecordDelivery(ctx, &WebhookDelivery{DeliveryID: "d-" + target, Event: "workflow_job",
		Status: "accepted", InstallationID: inst.ID, ReceivedAt: *now}); err != nil {
		t.Fatalf("RecordDelivery: %v", err)
	}
	if err := s.AppendScalingEvent(ctx, &ScalingEvent{PoolID: pool.ID, PoolName: pool.Name, From: 0, To: 1,
		Reason: "one queued", CreatedAt: *now}); err != nil {
		t.Fatalf("AppendScalingEvent: %v", err)
	}
	if err := s.RecordUsageCapacity(ctx, pool.ID, *now, true); err != nil {
		t.Fatalf("RecordUsageCapacity: %v", err)
	}
	for _, target := range [][2]string{{"installation", inst.ID}, {"pool", pool.ID}, {"runner", gone.ID}, {"job", job.ID}} {
		if err := s.AppendAudit(ctx, &AuditEvent{Action: target[0] + ".update", TargetKind: target[0],
			TargetID: target[1], CreatedAt: *now}); err != nil {
			t.Fatalf("AppendAudit: %v", err)
		}
	}
	return installationHistory{inst: inst, pool: pool, gone: gone, live: live, job: job, target: target}
}

func exportRows(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	tables, err := s.ExportInstallation(context.Background(), id)
	if err != nil {
		t.Fatalf("ExportInstallation(%s): %v", id, err)
	}
	b, err := json.Marshal(tables)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func usageFigures(t *testing.T, s *Store, id string) []UsageRow {
	t.Helper()
	rows, err := s.Usage(context.Background(), time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), UsageByInstallation)
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	var out []UsageRow
	for _, r := range rows {
		if r.Key == id {
			out = append(out, r)
		}
	}
	return out
}

func tableRows(t *testing.T, tables []ArchiveTable) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tb := range tables {
		out[tb.Table] = len(tb.Rows)
	}
	return out
}

func twoInstallations(t *testing.T) (*Store, installationHistory, installationHistory) {
	t.Helper()
	now := time.Date(2026, 5, 9, 8, 0, 0, 0, time.UTC)
	s := newTestStoreAt(t, func() time.Time { return now })
	host := &Host{Name: "vm-1", Capacity: 8, Embedded: true, Backends: StringSlice{"docker"}}
	if err := s.CreateHost(context.Background(), host); err != nil {
		t.Fatalf("CreateHost: %v", err)
	}
	a := seedInstallationHistory(t, s, &now, host, "acme", 101)
	b := seedInstallationHistory(t, s, &now, host, "globex", 202)
	now = time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	if _, err := s.RollUpUsage(context.Background(), now); err != nil {
		t.Fatalf("RollUpUsage: %v", err)
	}
	return s, a, b
}

// The export is everything about one installation and nothing about another:
// every table in the set has this installation's rows in it, and none of the
// neighbour's.
func TestAnInstallationsExportHoldsItsWholeHistoryAndNoOtherInstallations(t *testing.T) {
	s, a, b := twoInstallations(t)
	tables, err := s.ExportInstallation(context.Background(), a.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{
		"installations": 1, "pools": 1, "runners": 2, "pool_prewarms": 0, "jobs": 1, "job_events": 1,
		"webhook_deliveries": 1, "scaling_events": 1, "runner_sessions": 1, "usage_daily": 1,
		"usage_capacity_samples": 1, "capacity_demand_deliveries": 0, "audit_events": 4,
	}
	if got := tableRows(t, tables); !reflect.DeepEqual(got, want) {
		t.Errorf("rows per table = %v, want %v", got, want)
	}
	raw, _ := json.Marshal(tables)
	for _, other := range []string{b.inst.ID, b.pool.ID, b.gone.ID, b.live.ID, b.job.ID, b.target} {
		if strings.Contains(string(raw), other) {
			t.Errorf("the export of %s names %s, which belongs to the other installation", a.target, other)
		}
	}
}

// The acceptance for ZF-210b: exporting and purging one installation leaves
// the other's rows, and the figures computed from them, exactly as they were.
func TestPurgingOneInstallationLeavesTheOthersRowsAndFiguresByteIdentical(t *testing.T) {
	s, a, b := twoInstallations(t)
	ctx := context.Background()
	beforeRows, beforeFigures := exportRows(t, s, b.inst.ID), usageFigures(t, s, b.inst.ID)
	if len(beforeFigures) == 0 {
		t.Fatal("the other installation has no usage figures, so the comparison would prove nothing")
	}

	_ = exportRows(t, s, a.inst.ID)
	runners, err := s.PurgeInstallation(ctx, a.inst.ID)
	if err != nil {
		t.Fatalf("PurgeInstallation: %v", err)
	}
	if len(runners) != 2 {
		t.Errorf("the purge reported %d runners gone, want 2", len(runners))
	}

	if after := exportRows(t, s, b.inst.ID); string(after) != string(beforeRows) {
		t.Errorf("the other installation's rows changed:\nbefore %s\nafter  %s", beforeRows, after)
	}
	if after := usageFigures(t, s, b.inst.ID); !reflect.DeepEqual(after, beforeFigures) {
		t.Errorf("the other installation's figures changed:\nbefore %+v\nafter  %+v", beforeFigures, after)
	}
	if _, err := s.ExportInstallation(ctx, a.inst.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("exporting the purged installation gave %v, want ErrNotFound", err)
	}
	// Nothing anywhere still names what went: that is the difference between
	// a purge and the ordinary delete, which leaves history behind.
	for _, id := range []string{a.inst.ID, a.pool.ID, a.gone.ID, a.live.ID, a.job.ID} {
		for _, q := range []string{
			`SELECT COUNT(*) FROM audit_events WHERE target_id = ?`,
			`SELECT COUNT(*) FROM jobs WHERE id = ?1 OR pool_id = ?1 OR installation_id = ?1`,
			`SELECT COUNT(*) FROM job_events WHERE job_id = ?`,
			`SELECT COUNT(*) FROM runner_sessions WHERE runner_id = ?1 OR pool_id = ?1 OR installation_id = ?1`,
			`SELECT COUNT(*) FROM usage_daily WHERE pool_id = ?1 OR installation_id = ?1`,
			`SELECT COUNT(*) FROM scaling_events WHERE pool_id = ?`,
			`SELECT COUNT(*) FROM usage_capacity_samples WHERE pool_id = ?`,
			`SELECT COUNT(*) FROM webhook_deliveries WHERE installation_id = ?`,
		} {
			var n int
			if err := s.read.QueryRowContext(ctx, q, id).Scan(&n); err != nil {
				t.Fatalf("%s: %v", q, err)
			}
			if n != 0 {
				t.Errorf("%d rows still name %s after the purge: %s", n, id, q)
			}
		}
	}
}

// A job another installation claims stays with it even when it names a pool
// of the one being purged: the job's own installation is the stronger claim.
func TestAPurgeLeavesAJobAnotherInstallationClaims(t *testing.T) {
	s, a, b := twoInstallations(t)
	ctx := context.Background()
	stray, err := s.UpsertJob(ctx, &Job{GitHubJobID: 999, Repo: "globex/app", Workflow: "build", State: JobQueued,
		InstallationID: b.inst.ID, PoolID: a.pool.ID, QueuedAt: time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeInstallation(ctx, a.inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetJob(ctx, stray.ID); err != nil {
		t.Errorf("the other installation's job went with the purge: %v", err)
	}
}

// A pool that has since moved to another installation belongs to that one,
// even though the purged installation's old sessions still name it.
func TestAPurgeLeavesAPoolThatHasMovedToAnotherInstallation(t *testing.T) {
	s, a, b := twoInstallations(t)
	ctx := context.Background()
	a.pool.InstallationID = b.inst.ID
	if err := s.UpdatePool(ctx, a.pool); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PurgeInstallation(ctx, a.inst.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPool(ctx, a.pool.ID); err != nil {
		t.Errorf("the purge took a pool another installation now holds: %v", err)
	}
}

// An archive goes back in whole onto an empty database, and what comes out of
// the new one is what went into the old one -- except the runners, whose rows
// point at hosts that only the old instance has.
func TestAnExportedInstallationImportsOntoAFreshDatabase(t *testing.T) {
	s, a, _ := twoInstallations(t)
	ctx := context.Background()
	tables, err := s.ExportInstallation(ctx, a.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(tables)
	var carried []ArchiveTable
	if err := json.Unmarshal(raw, &carried); err != nil {
		t.Fatalf("the archive does not read back: %v", err)
	}

	fresh := newTestStore(t)
	got, err := fresh.ImportInstallation(ctx, carried)
	if err != nil {
		t.Fatalf("ImportInstallation: %v", err)
	}
	if got.Skipped["runners"] != 2 || got.Rows["runner_sessions"] != 1 || got.Rows["audit_events"] != 4 {
		t.Errorf("import = %+v", got)
	}
	again, err := fresh.ExportInstallation(ctx, a.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range tables {
		if tables[i].Table == "runners" {
			continue
		}
		w, _ := json.Marshal(tables[i])
		g, _ := json.Marshal(again[i])
		if string(w) != string(g) {
			t.Errorf("%s did not survive the round trip:\nwant %s\ngot  %s", tables[i].Table, w, g)
		}
	}
	inst, err := fresh.GetInstallation(ctx, a.inst.ID)
	if err != nil || string(inst.PrivateKeyEnc) != "sealed-acme" {
		t.Errorf("the installation came back as %+v, %v", inst, err)
	}

	// A second import of the same archive is a conflict, all or nothing.
	if _, err := fresh.ImportInstallation(ctx, carried); !errors.Is(err, ErrConflict) {
		t.Errorf("a second import gave %v, want ErrConflict", err)
	}
}

// An archive's column names reach an INSERT, so a name the schema does not
// have must be refused before it gets there.
func TestAnImportRefusesATableOrColumnTheSchemaDoesNotHave(t *testing.T) {
	s, a, _ := twoInstallations(t)
	ctx := context.Background()
	tables, err := s.ExportInstallation(ctx, a.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]ArchiveTable) []ArchiveTable
	}{
		{"an unknown table", func(ts []ArchiveTable) []ArchiveTable {
			return append(ts, ArchiveTable{Table: "users", Columns: []string{"id"}, Rows: [][]ArchiveValue{{{V: "usr_x"}}}})
		}},
		{"an unknown column", func(ts []ArchiveTable) []ArchiveTable {
			ts[1].Columns = append([]string(nil), ts[1].Columns...)
			ts[1].Columns[0] = "id) VALUES (1); DROP TABLE users; --"
			return ts
		}},
		{"a column named twice", func(ts []ArchiveTable) []ArchiveTable {
			ts[0].Columns = append([]string(nil), ts[0].Columns...)
			ts[0].Columns[len(ts[0].Columns)-1] = ts[0].Columns[0]
			return ts
		}},
		{"no installation", func(ts []ArchiveTable) []ArchiveTable { return ts[1:] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := append([]ArchiveTable(nil), tables...)
			fresh := newTestStore(t)
			if _, err := fresh.ImportInstallation(ctx, tc.mutate(cp)); !errors.Is(err, ErrArchiveShape) {
				t.Errorf("import gave %v, want ErrArchiveShape", err)
			}
			if list, _ := fresh.ListInstallations(ctx); len(list) != 0 {
				t.Errorf("a refused import left %d installations behind", len(list))
			}
		})
	}
}

// The archive carries the usage_daily rows the source had already rolled up
// and the sessions they came from, but not the watermark. On the new machine
// the roll-up therefore starts from the oldest imported session and computes
// days the archive already holds; a plain INSERT aborted on the primary key,
// every hour, for the whole instance -- and with the watermark never written
// the sessions could never be pruned either.
func TestTheUsageRollUpSucceedsAfterAnImportAndCountsNothingTwice(t *testing.T) {
	s, a, _ := twoInstallations(t)
	ctx := context.Background()
	tables, err := s.ExportInstallation(ctx, a.inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh := newTestStore(t)
	if _, err := fresh.ImportInstallation(ctx, tables); err != nil {
		t.Fatalf("ImportInstallation: %v", err)
	}
	imported := usageDays(t, fresh)
	if len(imported) == 0 {
		t.Fatal("the archive carried no usage days, so this test proves nothing")
	}

	until := time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC)
	for pass := 1; pass <= 2; pass++ {
		if _, err := fresh.RollUpUsage(ctx, until); err != nil {
			t.Fatalf("roll-up pass %d after the import: %v", pass, err)
		}
	}
	if _, _, ok, err := fresh.UsageRollupRange(ctx); err != nil || !ok {
		t.Fatalf("the roll-up wrote no watermark: ok=%v, %v", ok, err)
	}
	if got := usageDays(t, fresh); !reflect.DeepEqual(got, imported) {
		t.Fatalf("the roll-up changed the imported days:\nwant %+v\ngot  %+v", imported, got)
	}
}

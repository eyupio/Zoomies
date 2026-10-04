package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Classes
// ---------------------------------------------------------------------------

func TestSizeClassesAreOrderedAndNamedLikeLabels(t *testing.T) {
	all := SizeClasses()
	if len(all) != 3 || all[0] != SizeSmall || all[1] != SizeMedium || all[2] != SizeLarge {
		t.Fatalf("SizeClasses() = %v, want small, medium, large in that order", all)
	}
	all[0] = "tampered"
	if SizeClasses()[0] != SizeSmall {
		t.Fatal("SizeClasses() hands out the slice it compares against")
	}

	for want, c := range map[int]SizeClass{0: SizeSmall, 1: SizeMedium, 2: SizeLarge, -1: "huge"} {
		if got := c.Rank(); got != want {
			t.Errorf("%q.Rank() = %d, want %d", c, got, want)
		}
	}
	if up, ok := SizeSmall.Larger(); !ok || up != SizeMedium {
		t.Errorf("small.Larger() = %q, %v", up, ok)
	}
	if _, ok := SizeLarge.Larger(); ok {
		t.Error("there is a class above large")
	}
	if down, ok := SizeLarge.Smaller(); !ok || down != SizeMedium {
		t.Errorf("large.Smaller() = %q, %v", down, ok)
	}
	if _, ok := SizeSmall.Smaller(); ok {
		t.Error("there is a class below small")
	}
	if _, ok := SizeClass("huge").Larger(); ok {
		t.Error("an unknown class has a larger one")
	}

	for in, want := range map[string]SizeClass{"Large": SizeLarge, " MEDIUM ": SizeMedium, "small": SizeSmall} {
		if got, ok := ParseSizeClass(in); !ok || got != want {
			t.Errorf("ParseSizeClass(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "xl", "zoomies-large", "larger"} {
		if _, ok := ParseSizeClass(in); ok {
			t.Errorf("ParseSizeClass(%q) accepted something that is not a class", in)
		}
	}
}

// A class is spelled in a workflow as a label, so the label is the contract: it
// has to be what the rest of the fleet's labels look like, and it has to be
// found again in a runs-on however GitHub cases it.
func TestASizeClassIsAskedForByABrandedLabel(t *testing.T) {
	if got := SizeLarge.Label(); got != "zoomies-large" {
		t.Fatalf("large's label = %q, want zoomies-large", got)
	}
	for _, c := range SizeClasses() {
		got, ok := ClassOfLabel(strings.ToUpper(c.Label()))
		if !ok || got != c {
			t.Errorf("ClassOfLabel(%q) = %q, %v, want %q", strings.ToUpper(c.Label()), got, ok, c)
		}
	}
	for _, l := range []string{"large", "zoomies", "zoomies-xl", "self-hosted", "zoomies-large-arm64", ""} {
		if c, ok := ClassOfLabel(l); ok {
			t.Errorf("ClassOfLabel(%q) = %q; that label does not ask for a class", l, c)
		}
	}
}

func TestAnAutoKeyNamesAnArchitectureAndAClass(t *testing.T) {
	// The grammar's own spelling of the architecture, so two hosts that report
	// it two ways end up in one pool.
	for _, arch := range []string{"amd64", "x86_64", "X64"} {
		if got := AutoKeyFor(arch, SizeLarge); got != "amd64/large" {
			t.Errorf("AutoKeyFor(%q, large) = %q, want amd64/large", arch, got)
		}
	}
	if got := AutoKeyFor("aarch64", SizeSmall); got != "arm64/small" {
		t.Errorf("AutoKeyFor(aarch64, small) = %q", got)
	}
	arch, class, ok := ParseAutoKey("arm64/medium")
	if !ok || arch != "arm64" || class != SizeMedium {
		t.Fatalf("ParseAutoKey(arm64/medium) = %q, %q, %v", arch, class, ok)
	}
	for _, bad := range []string{"", "arm64", "arm64/", "/large", "riscv/large", "x64/large", "arm64/huge", "arm64/large/x"} {
		if _, _, ok := ParseAutoKey(bad); ok {
			t.Errorf("ParseAutoKey(%q) accepted something AutoKeyFor never writes", bad)
		}
	}
}

// ---------------------------------------------------------------------------
// A host's class
// ---------------------------------------------------------------------------

func TestAHostRemembersItsSizeClassAndNoHeartbeatWritesIt(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	got, _ := s.GetHost(ctx, host.ID)
	if got.SizeClass.Set() {
		t.Fatalf("a host nobody has looked at has a class: %+v", got.SizeClass)
	}

	since := versionsEpoch
	want := HostSizeClass{Class: SizeMedium, Pending: SizeLarge, PendingSince: &since, ChangedAt: &since}
	if err := s.SetHostSizeClass(ctx, host.ID, want, ""); err != nil {
		t.Fatalf("SetHostSizeClass: %v", err)
	}
	got, _ = s.GetHost(ctx, host.ID)
	if got.SizeClass.Class != SizeMedium || got.SizeClass.Pending != SizeLarge ||
		!got.SizeClass.PendingSince.Equal(since) || !got.SizeClass.ChangedAt.Equal(since) {
		t.Fatalf("the class did not survive a round trip: %+v", got.SizeClass)
	}
	if hosts, _ := s.ListHosts(ctx); len(hosts) != 1 || hosts[0].SizeClass.Class != SizeMedium {
		t.Fatalf("ListHosts reads the class differently from GetHost: %+v", hosts)
	}

	// A heartbeat carries a copy of the host read at the top of the request. If
	// either write it reaches wrote the class, the controller's decision would
	// be put back to what it was with nothing to say so; an operator's edit must
	// not reach it either.
	stale := *got
	stale.SizeClass = HostSizeClass{}
	stale.CPUs, stale.MemoryMB = 12, 32768
	if err := s.UpdateHost(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHostReported(ctx, &stale); err != nil {
		t.Fatal(err)
	}
	capacity := 3
	if err := s.PatchHost(ctx, host.ID, HostChanges{Capacity: &capacity}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetHost(ctx, host.ID)
	if got.SizeClass.Class != SizeMedium || got.CPUs != 12 || got.Capacity != 3 {
		t.Fatalf("another writer reached the class, or its own facts were lost: %+v, %d CPUs, capacity %d",
			got.SizeClass, got.CPUs, got.Capacity)
	}

	created := &Host{Name: "joined", Capacity: 2, SizeClass: HostSizeClass{Class: SizeSmall}}
	if err := s.CreateHost(ctx, created); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetHost(ctx, created.ID); got.SizeClass.Class != SizeSmall {
		t.Fatalf("CreateHost dropped the class: %+v", got.SizeClass)
	}
}

// The class is decided by whichever pass gets there first, and a pass that was
// reading an older one must find that out instead of writing over it.
func TestSetHostSizeClassWritesOnlyFromTheClassItWasDecidedFrom(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, _, host := seedPool(t, s)

	if err := s.SetHostSizeClass(ctx, host.ID, HostSizeClass{Class: SizeSmall}, ""); err != nil {
		t.Fatal(err)
	}
	err := s.SetHostSizeClass(ctx, host.ID, HostSizeClass{Class: SizeLarge}, "")
	if !errors.Is(err, ErrSizeClassMoved) {
		t.Fatalf("a decision made from a class the host has left was accepted: %v", err)
	}
	if got, _ := s.GetHost(ctx, host.ID); got.SizeClass.Class != SizeSmall {
		t.Fatalf("the refused write changed the class to %q", got.SizeClass.Class)
	}
	if err := s.SetHostSizeClass(ctx, host.ID, HostSizeClass{Class: SizeLarge}, SizeSmall); err != nil {
		t.Fatalf("a decision from the current class was refused: %v", err)
	}
	if err := s.SetHostSizeClass(ctx, "host_missing", HostSizeClass{Class: SizeLarge}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a host that is not there was reported as %v, want ErrNotFound", err)
	}

	// Two passes deciding from the same class: exactly one of them wins.
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- s.SetHostSizeClass(ctx, host.ID, HostSizeClass{Class: SizeMedium}, SizeLarge)
		}()
	}
	wg.Wait()
	close(results)
	won := 0
	for err := range results {
		if err == nil {
			won++
		} else if !errors.Is(err, ErrSizeClassMoved) {
			t.Fatalf("unexpected error from a racing decision: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d racing decisions were accepted, want exactly 1", won)
	}
}

func TestAnEmptySizeClassIsStoredAsNothing(t *testing.T) {
	v, err := HostSizeClass{}.Value()
	if err != nil {
		t.Fatal(err)
	}
	if v != "{}" {
		t.Fatalf("an empty class is stored as %v, want {} so that unclassified hosts stay recognisable", v)
	}
	var h HostSizeClass
	for _, raw := range []any{nil, "", "{}", []byte("{}")} {
		if err := h.Scan(raw); err != nil || h.Set() {
			t.Fatalf("Scan(%#v) = %v, %+v; want no class and no error", raw, err, h)
		}
	}
	if err := h.Scan(42); err == nil {
		t.Fatal("a column of the wrong type was accepted as a class")
	}
}

// An operator's size label is what a host answers for `size`, ahead of what the
// controller worked out -- the same rule os and arch follow -- and it never
// ends up written into the controller's own record.
func TestAnOperatorsSizeLabelBeatsTheHeldClass(t *testing.T) {
	held := HostSizeClass{Class: SizeSmall}
	cases := []struct {
		name         string
		labels       StringMap
		wantClass    SizeClass
		wantOperator bool
		wantSelector string
	}{
		{"nothing said and nothing held", nil, "", false, ""},
		{"the held class answers when no label does", nil, SizeSmall, false, "small"},
		{"a size label wins over the held class", StringMap{"size": "large"}, SizeLarge, true, "large"},
		// A host selector compares a label exactly, so a class that is only a class
		// on a looser reading would be counted towards a pool whose own selector
		// never matches the host.
		{"a class has to be written exactly", StringMap{"size": "Large"}, "", true, "Large"},
		{"and without spaces", StringMap{"size": " large "}, "", true, " large "},
		{"a size that is not a class is the operator's, and no class", StringMap{"size": "huge"}, "", true, "huge"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Host{Labels: tc.labels}
			if tc.name != "nothing said and nothing held" {
				h.SizeClass = held
			}
			class, operator := h.EffectiveSizeClass()
			if class != tc.wantClass || operator != tc.wantOperator {
				t.Errorf("EffectiveSizeClass() = %q, %v, want %q, %v", class, operator, tc.wantClass, tc.wantOperator)
			}
			if got := h.SelectorValue(LabelSize); got != tc.wantSelector {
				t.Errorf("SelectorValue(size) = %q, want %q", got, tc.wantSelector)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pools the controller keeps
// ---------------------------------------------------------------------------

func autoPool(inst *Installation, name, key string) *Pool {
	return &Pool{
		Name: name, InstallationID: inst.ID, Labels: StringSlice{"zoomies-" + name}, Backend: BackendDocker,
		MaxRunners: 4, DockerMode: DockerNone, Enabled: true, Ephemeral: true, SizeFromProfile: true,
		AutoKey: key, AutoMin: 1, AutoCap: 6, AutoPaused: false,
	}
}

func TestAPoolRemembersWhatTheOperatorAskedOfAnAutomaticPool(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst, operatorPool, _ := seedPool(t, s)
	if operatorPool.FromHosts() {
		t.Fatal("a pool an operator made says the controller keeps it")
	}

	p := autoPool(inst, "large", "amd64/large")
	if err := s.CreatePool(ctx, p); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}
	got, err := s.GetPool(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FromHosts() || got.AutoKey != "amd64/large" || got.AutoMin != 1 || got.AutoCap != 6 || got.AutoPaused {
		t.Fatalf("the automatic pool's fields did not survive a round trip: %+v", got)
	}

	got.AutoPaused, got.AutoCap, got.AutoMin = true, 3, 0
	if err := s.UpdatePool(ctx, got); err != nil {
		t.Fatal(err)
	}
	pools, _ := s.ListPools(ctx)
	var read *Pool
	for _, q := range pools {
		if q.ID == p.ID {
			read = q
		}
	}
	if read == nil || !read.AutoPaused || read.AutoCap != 3 || read.AutoMin != 0 || read.AutoKey != "amd64/large" {
		t.Fatalf("an edit of the operator's side was lost, or changed the key: %+v", read)
	}

	// A pool's name is not its key, and an operator who renames it does not
	// change which pool it is.
	read.Name = "renamed"
	if err := s.UpdatePool(ctx, read); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.GetPool(ctx, p.ID); again.AutoKey != "amd64/large" {
		t.Fatalf("a rename changed the pool's key to %q", again.AutoKey)
	}
}

// There is one pool for each architecture and class in an installation, and the
// database is what says so: two reconcilers racing to create the same pool
// cannot both succeed. An operator's pools all carry the empty key and are not
// limited by it.
func TestThereIsOneAutomaticPoolForEachKeyInAnInstallation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst, _, _ := seedPool(t, s)
	other := &Installation{AppID: 1, InstallationID: 3, Target: "other", TargetType: TargetOrg}
	if err := s.CreateInstallation(ctx, other); err != nil {
		t.Fatal(err)
	}

	if err := s.CreatePool(ctx, autoPool(inst, "large", "amd64/large")); err != nil {
		t.Fatal(err)
	}
	err := s.CreatePool(ctx, autoPool(inst, "large-again", "amd64/large"))
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("a second pool for one key in one installation was accepted: %v", err)
	}
	if err := s.CreatePool(ctx, autoPool(inst, "large-arm", "arm64/large")); err != nil {
		t.Fatalf("a different architecture was refused: %v", err)
	}
	if err := s.CreatePool(ctx, autoPool(other, "other-large", "amd64/large")); err != nil {
		t.Fatalf("the same key in another installation was refused: %v", err)
	}
	for _, name := range []string{"hand-made-1", "hand-made-2"} {
		p := autoPool(inst, name, "")
		p.AutoMin, p.AutoCap = 0, 0
		if err := s.CreatePool(ctx, p); err != nil {
			t.Fatalf("an operator pool was limited by the key's index: %v", err)
		}
	}
}

// applied is the pool as the controller wants it: from with the figures the
// reconciler derives changed.
func applied(from *Pool, mutate func(*Pool)) *Pool {
	to := *from
	mutate(&to)
	return &to
}

func TestTheControllersWriteToAPoolTouchesOnlyPoolsItKeeps(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	s := newTestStoreAt(t, func() time.Time { return now })
	inst, operatorPool, _ := seedPool(t, s)
	kept := autoPool(inst, "large", "amd64/large")
	if err := s.CreatePool(ctx, kept); err != nil {
		t.Fatal(err)
	}

	// The guard: whatever ID the reconciler holds, an operator's pool is not
	// something it can rewrite.
	changed, err := s.ApplyAutoPool(ctx, operatorPool, applied(operatorPool, func(p *Pool) { p.MinRunners, p.MaxRunners, p.Enabled = 0, 99, false }))
	if err != nil || changed {
		t.Fatalf("ApplyAutoPool on an operator pool = %v, %v; it must change nothing", changed, err)
	}
	if got, _ := s.GetPool(ctx, operatorPool.ID); got.MaxRunners != 4 || !got.Enabled {
		t.Fatalf("an operator's pool was rewritten: max %d, enabled %v", got.MaxRunners, got.Enabled)
	}

	now = t0.Add(time.Hour)
	grown := applied(kept, func(p *Pool) { p.MinRunners, p.MaxRunners, p.Enabled = 1, 9, true })
	changed, err = s.ApplyAutoPool(ctx, kept, grown)
	if err != nil || !changed {
		t.Fatalf("ApplyAutoPool = %v, %v; want the change recorded", changed, err)
	}
	got, _ := s.GetPool(ctx, kept.ID)
	if got.MinRunners != 1 || got.MaxRunners != 9 || !got.Enabled || !got.UpdatedAt.Equal(now) {
		t.Fatalf("the derived figures were not written: %+v", got)
	}
	if got.AutoCap != 6 || got.AutoMin != 1 || got.Name != "zoomies-large" {
		t.Fatalf("the derived write reached the operator's side: %+v", got)
	}

	// A pass that finds nothing to change writes nothing, so the pool's
	// updated_at is the last time it really did change and not the last pass.
	now = t0.Add(2 * time.Hour)
	changed, err = s.ApplyAutoPool(ctx, got, grown)
	if err != nil || changed {
		t.Fatalf("an unchanged pass reported a change: %v, %v", changed, err)
	}
	if got, _ := s.GetPool(ctx, kept.ID); !got.UpdatedAt.Equal(t0.Add(time.Hour)) {
		t.Fatalf("an unchanged pass moved updated_at to %v", got.UpdatedAt)
	}

	changed, _ = s.ApplyAutoPool(ctx, got, applied(got, func(p *Pool) { p.MinRunners, p.MaxRunners, p.Enabled = 0, 0, false }))
	if !changed {
		t.Fatal("disabling an empty pool was not recorded")
	}
	if got, _ := s.GetPool(ctx, kept.ID); got.Enabled || got.MaxRunners != 0 {
		t.Fatalf("the pool was not disabled: %+v", got)
	}
}

// A pool whose limits and whose fixed settings both needed correcting used to
// take two passes, or a whole-row write that put back whatever an operator had
// saved since the pass read it. It is one statement now, and it names only the
// columns the controller owns.
func TestAnAutomaticPoolIsReshapedAndResizedInOneWrite(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst, _, _ := seedPool(t, s)
	kept := autoPool(inst, "large", "amd64/large")
	kept.DockerMode = DockerHostSocket
	kept.Ephemeral = false
	kept.HostSelector = StringMap{"size": "medium"}
	kept.Platform = Platform{OS: "linux"}
	if err := s.CreatePool(ctx, kept); err != nil {
		t.Fatal(err)
	}
	from, _ := s.GetPool(ctx, kept.ID)

	to := applied(from, func(p *Pool) {
		p.MaxRunners, p.MinRunners = 7, 1
		p.DockerMode, p.Ephemeral = DockerNone, true
		p.HostSelector = StringMap{"size": "large"}
		p.Platform = Platform{OS: "linux", Arch: "amd64"}
		p.Labels = StringSlice{"zoomies", "zoomies-large", "linux", "x64"}
	})
	changed, err := s.ApplyAutoPool(ctx, from, to)
	if err != nil || !changed {
		t.Fatalf("ApplyAutoPool = %v, %v; want one write", changed, err)
	}
	got, _ := s.GetPool(ctx, kept.ID)
	if got.MaxRunners != 7 || got.DockerMode != DockerNone || !got.Ephemeral || got.HostSelector["size"] != "large" ||
		got.Platform.Arch != "amd64" || len(got.Labels) != 4 {
		t.Fatalf("the limits and the shape were not both written: %+v", got)
	}
	// The same pass again finds nothing left to correct.
	if again, err := s.ApplyAutoPool(ctx, got, to); err != nil || again {
		t.Fatalf("a second identical write reported a change: %v, %v", again, err)
	}
}

func TestTheControllersWriteDoesNotUndoWhatAnOperatorSavedWhileItWasWorking(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	inst, _, _ := seedPool(t, s)
	kept := autoPool(inst, "large", "amd64/large")
	if err := s.CreatePool(ctx, kept); err != nil {
		t.Fatal(err)
	}
	// The pass reads the pool, and works out a new maximum from its cap of 6.
	read, _ := s.GetPool(ctx, kept.ID)
	want := applied(read, func(p *Pool) { p.MaxRunners = 6 })

	// While it works an operator lowers the cap and sets an idle timeout, each a
	// whole-row save of the pool as they read it.
	edited := *read
	edited.AutoCap = 2
	edited.IdleTimeout = Duration(15 * time.Minute)
	if err := s.UpdatePool(ctx, &edited); err != nil {
		t.Fatal(err)
	}

	changed, err := s.ApplyAutoPool(ctx, read, want)
	if err != nil || changed {
		t.Fatalf("a write worked out under a cap that has since changed was applied: %v, %v", changed, err)
	}
	got, _ := s.GetPool(ctx, kept.ID)
	if got.AutoCap != 2 || got.IdleTimeout.Duration() != 15*time.Minute || got.MaxRunners != 4 {
		t.Fatalf("the operator's edit was undone, or the stale maximum written: %+v", got)
	}

	// An edit the figures were not worked out from -- the idle timeout alone --
	// does not stop the write, and is not undone by it.
	fresh, _ := s.GetPool(ctx, kept.ID)
	edited = *fresh
	edited.IdleTimeout = Duration(20 * time.Minute)
	if err := s.UpdatePool(ctx, &edited); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.ApplyAutoPool(ctx, fresh, applied(fresh, func(p *Pool) { p.MaxRunners = 2 })); err != nil || !changed {
		t.Fatalf("an idle timeout saved meanwhile stopped the write: %v, %v", changed, err)
	}
	got, _ = s.GetPool(ctx, kept.ID)
	if got.MaxRunners != 2 || got.IdleTimeout.Duration() != 20*time.Minute {
		t.Fatalf("the write undid the idle timeout, or was not applied: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// What a job was classed as
// ---------------------------------------------------------------------------

func queuedJob(t *testing.T, s *Store, n int64) *Job {
	t.Helper()
	return seedJob(t, s, n, JobQueued, "")
}

func TestAJobIsClassedOnceAndRoutedToItsClass(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	job := queuedJob(t, s, 1)
	if job.SizeClass != "" || job.SizeBasis != "" || job.RoutedClass != "" || job.RanClass != "" {
		t.Fatalf("a new job claims a class: %+v", job)
	}

	got, wrote, err := s.StampJobClass(ctx, job.ID, JobClassing{Class: SizeLarge, Reason: "memory peaked at 9.1 GB over 14 runs", Basis: SizeBasisHistory, FloorMB: 10900, Route: true})
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("the stamp that classed the job did not say it wrote")
	}
	if got.SizeClass != SizeLarge || got.SizeReason != "memory peaked at 9.1 GB over 14 runs" ||
		got.SizeBasis != SizeBasisHistory || got.SizeFloorMB != 10900 || got.RoutedClass != SizeLarge || got.RoutedNote != "" {
		t.Fatalf("the stamp did not land, or did not route the job to its class: %+v", got)
	}

	// A second observer of the same queued job, or history that has moved on
	// since, must not rewrite what this job was taken to need.
	got, wrote, err = s.StampJobClass(ctx, job.ID, JobClassing{Class: SizeSmall, Reason: "no history", Basis: SizeBasisDefault})
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("a second stamp said it wrote: a count of jobs classed would be raised twice for one job")
	}
	if got.SizeClass != SizeLarge || got.SizeBasis != SizeBasisHistory || got.SizeFloorMB != 10900 {
		t.Fatalf("a second stamp rewrote the first: %+v", got)
	}

	// GitHub's deliveries move the job on; the stamp is the fleet's own note.
	again, err := s.UpsertJob(ctx, &Job{GitHubJobID: 1, Repo: "acme/api", JobName: "build", State: JobCompleted, Conclusion: "success"})
	if err != nil {
		t.Fatal(err)
	}
	if again.SizeClass != SizeLarge || again.RoutedClass != SizeLarge || again.SizeReason == "" {
		t.Fatalf("a webhook delivery lost the class: %+v", again)
	}

	// Nothing to record writes nothing, and says nothing is wrong.
	bare := queuedJob(t, s, 2)
	for _, c := range []JobClassing{{Class: "", Basis: SizeBasisDefault}, {Class: "huge", Basis: SizeBasisDefault}, {Class: SizeSmall}} {
		got, wrote, err := s.StampJobClass(ctx, bare.ID, c)
		if err != nil || wrote || got.SizeBasis != "" || got.SizeClass != "" {
			t.Fatalf("stamping %+v wrote %+v (said it wrote: %t), %v", c, got, wrote, err)
		}
	}
	if _, _, err := s.StampJobClass(ctx, "job_missing", JobClassing{Class: SizeSmall, Reason: "x", Basis: SizeBasisDefault}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stamping a job that does not exist = %v, want ErrNotFound", err)
	}
}

// A late `queued` delivery, after the job has run, reaches the controller as a
// stamp. It must not write a class on a job that is no longer waiting for one.
func TestAJobThatIsNoLongerWaitingIsNotStampedWithAClass(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	for _, state := range []JobState{JobInProgress, JobCompleted} {
		job := seedJob(t, s, int64(10+len(state)), state, "run_late")
		got, wrote, err := s.StampJobClass(ctx, job.ID, JobClassing{Class: SizeMedium, Reason: "the default", Basis: SizeBasisDefault, Route: true})
		if err != nil {
			t.Fatal(err)
		}
		if wrote || got.SizeBasis != "" || got.SizeClass != "" || got.RoutedClass != "" {
			t.Fatalf("a %s job was stamped with %+v (said it wrote: %t)", state, got, wrote)
		}
	}
	// A job held for approval is not yet queued, and is stamped like one: its
	// class is wanted for the claim it will be given when it is.
	held := seedJob(t, s, 9, JobWaiting, "")
	got, _, err := s.StampJobClass(ctx, held.ID, JobClassing{Class: SizeMedium, Reason: "the default", Basis: SizeBasisDefault})
	if err != nil || got.SizeBasis != SizeBasisDefault {
		t.Fatalf("a job held for approval was stamped %+v, %v", got, err)
	}
}

// While size routing is only being watched the class is recorded and the job is
// not sent anywhere: it is claimed for a pool exactly as it always was.
func TestAJobClassedWhileOnlyWatchedIsNotRouted(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	job := queuedJob(t, s, 1)
	got, _, err := s.StampJobClass(ctx, job.ID, JobClassing{Class: SizeLarge, Reason: "pinned", Basis: SizeBasisPin})
	if err != nil {
		t.Fatal(err)
	}
	if got.SizeClass != SizeLarge || got.RoutedClass != "" {
		t.Fatalf("a watched job was %+v; want a class and no route", got)
	}
	changed, err := s.SetQueuedJobClass(ctx, job.ID, JobClassing{Class: SizeLarge, Reason: "pinned", Basis: SizeBasisPin, Route: true})
	if err != nil || !changed {
		t.Fatalf("turning routing on for a queued job = %v, %v", changed, err)
	}
	if got, _ := s.GetJob(ctx, job.ID); got.RoutedClass != SizeLarge {
		t.Fatalf("the job was not routed: %+v", got)
	}
}

func TestOnlyAJobStillWaitingIsMovedToAnotherClass(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	waiting := queuedJob(t, s, 1)
	if _, _, err := s.StampJobClass(ctx, waiting.ID, JobClassing{Class: SizeSmall, Reason: "the default", Basis: SizeBasisDefault, Route: true}); err != nil {
		t.Fatal(err)
	}

	moved, err := s.SetJobRouted(ctx, waiting.ID, SizeSmall, SizeMedium, "no small host had room for 2m")
	if err != nil || !moved {
		t.Fatalf("SetJobRouted = %v, %v; want the move recorded", moved, err)
	}
	got, _ := s.GetJob(ctx, waiting.ID)
	if got.SizeClass != SizeSmall || got.RoutedClass != SizeMedium || got.RoutedNote == "" {
		t.Fatalf("the routing did not land, or changed what the job was classed as: %+v", got)
	}
	if moved, _ := s.SetJobRouted(ctx, waiting.ID, SizeMedium, SizeMedium, "no small host had room for 2m"); moved {
		t.Fatal("saying the same thing again was reported as a move")
	}

	// A decision made from where the job was, and read after something else moved
	// it, is stale: an operator's pin that landed first is not to be overwritten
	// by the fallback of a pass that had not seen it.
	if moved, _ := s.SetJobRouted(ctx, waiting.ID, SizeSmall, SizeLarge, "decided before the job was moved"); moved {
		t.Fatal("a move decided from a class the job has since left was applied")
	}
	if got, _ := s.GetJob(ctx, waiting.ID); got.RoutedClass != SizeMedium {
		t.Fatalf("a stale move changed the route to %q", got.RoutedClass)
	}

	// A job nobody has classed has nowhere to be moved from, and a job that has
	// started has been routed: what happened to it is not for rewriting.
	unclassed := queuedJob(t, s, 2)
	if moved, _ := s.SetJobRouted(ctx, unclassed.ID, "", SizeLarge, "x"); moved {
		t.Fatal("a job that was never classed was routed")
	}
	running := seedJob(t, s, 3, JobInProgress, "run_1")
	if _, _, err := s.StampJobClass(ctx, running.ID, JobClassing{Class: SizeSmall, Reason: "the default", Basis: SizeBasisDefault, Route: true}); err != nil {
		t.Fatal(err)
	}
	if moved, _ := s.SetJobRouted(ctx, running.ID, SizeSmall, SizeLarge, "x"); moved {
		t.Fatal("a job that is running was routed")
	}
	if _, err := s.SetJobRouted(ctx, waiting.ID, SizeMedium, "huge", "x"); err == nil {
		t.Fatal("routing a job to something that is not a class was accepted")
	}

	// A job whose class and reason are what they were is not undone by being
	// asked again: it was sent on because its own class had no room, and a pin for
	// somebody else's job in the same repository changes nothing about it.
	if changed, _ := s.SetQueuedJobClass(ctx, waiting.ID, JobClassing{Class: SizeSmall, Reason: "the default", Basis: SizeBasisDefault, Route: true}); changed {
		t.Fatal("asking again for the class a job already had was reported as a change")
	}
	if got, _ := s.GetJob(ctx, waiting.ID); got.RoutedClass != SizeMedium || got.RoutedNote == "" {
		t.Fatalf("a job sent on to medium was sent back: %+v", got)
	}

	// A pin reaches the jobs already waiting, and puts them back on the class
	// it names, whatever the wait had made of their routing.
	changed, err := s.SetQueuedJobClass(ctx, waiting.ID, JobClassing{Class: SizeLarge, Reason: "pinned to large by an operator", Basis: SizeBasisPin, Route: true})
	if err != nil || !changed {
		t.Fatalf("SetQueuedJobClass = %v, %v", changed, err)
	}
	got, _ = s.GetJob(ctx, waiting.ID)
	if got.SizeClass != SizeLarge || got.SizeBasis != SizeBasisPin || got.SizeFloorMB != 0 || got.RoutedClass != SizeLarge || got.RoutedNote != "" {
		t.Fatalf("the pin did not reach the queued job: %+v", got)
	}
	if changed, _ := s.SetQueuedJobClass(ctx, waiting.ID, JobClassing{Class: SizeLarge, Reason: "pinned to large by an operator", Basis: SizeBasisPin, Route: true}); changed {
		t.Fatal("restating the pin was reported as a change")
	}
	if changed, _ := s.SetQueuedJobClass(ctx, running.ID, JobClassing{Class: SizeLarge, Reason: "pinned", Basis: SizeBasisPin}); changed {
		t.Fatal("a pin rewrote a job that is already running")
	}
	if _, err := s.SetQueuedJobClass(ctx, waiting.ID, JobClassing{Class: SizeLarge, Reason: "x"}); err == nil {
		t.Fatal("a class with no basis was accepted")
	}
}

// A pin made while a job waits for an approver is what it should be queued by
// when the approval comes. It was classed when it arrived, and a pin that did not
// reach it would leave it in the class it had before the pin until somebody
// noticed.
func TestAPinReachesAJobHeldForAReview(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	held := seedJob(t, s, 1, JobWaiting, "")
	queued := queuedJob(t, s, 2)
	seedJob(t, s, 3, JobInProgress, "run_1")
	if _, _, err := s.StampJobClass(ctx, held.ID, JobClassing{Class: SizeMedium, Reason: "the default", Basis: SizeBasisDefault, Route: true}); err != nil {
		t.Fatal(err)
	}

	jobs, err := s.ListHeldJobs(ctx)
	if err != nil || len(jobs) != 1 || jobs[0].ID != held.ID {
		t.Fatalf("ListHeldJobs = %d jobs, %v; want only the job held for a review (not %s)", len(jobs), err, queued.ID)
	}
	changed, err := s.SetQueuedJobClass(ctx, held.ID, JobClassing{Class: SizeLarge, Reason: "pinned to large by an operator", Basis: SizeBasisPin, Route: true})
	if err != nil || !changed {
		t.Fatalf("SetQueuedJobClass on a held job = %v, %v; want the pin to reach it", changed, err)
	}
	if got, _ := s.GetJob(ctx, held.ID); got.SizeClass != SizeLarge || got.SizeBasis != SizeBasisPin || got.RoutedClass != SizeLarge {
		t.Fatalf("the held job is %+v after the pin", got)
	}
}

// The claim follows the route, so the page that lists a pool's queue and the
// scheduler that feeds it are talking about the same jobs.
func TestAWaitingJobIsPointedAtThePoolThatClaimsItAndARunningOneIsNot(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	waiting := queuedJob(t, s, 1)
	if waiting.PoolID != "" || waiting.Matched {
		t.Fatalf("the seeded job is already claimed: %+v", waiting)
	}

	moved, err := s.SetJobClaim(ctx, waiting.ID, "pool_large")
	if err != nil || !moved {
		t.Fatalf("SetJobClaim = %v, %v; want the claim recorded", moved, err)
	}
	if got, _ := s.GetJob(ctx, waiting.ID); got.PoolID != "pool_large" || !got.Matched {
		t.Fatalf("the claim did not land: %+v", got)
	}
	if moved, _ := s.SetJobClaim(ctx, waiting.ID, "pool_large"); moved {
		t.Fatal("saying the same thing again was reported as a move")
	}
	if moved, _ := s.SetJobClaim(ctx, waiting.ID, ""); !moved {
		t.Fatal("a job nothing claims any more was left claimed")
	}
	if got, _ := s.GetJob(ctx, waiting.ID); got.PoolID != "" || got.Matched {
		t.Fatalf("the claim was not cleared: %+v", got)
	}

	running := seedJob(t, s, 2, JobInProgress, "run_1")
	if moved, _ := s.SetJobClaim(ctx, running.ID, "pool_large"); moved {
		t.Fatal("a job that is already running was handed to another pool")
	}
}

func TestTheClassOfTheHostThatTookAJobIsRecordedOnce(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	job := seedJob(t, s, 1, JobInProgress, "run_1")

	got, wrote, err := s.StampJobRan(ctx, job.ID, SizeMedium)
	if err != nil || !wrote || got.RanClass != SizeMedium {
		t.Fatalf("StampJobRan = %+v, wrote %t, %v", got, wrote, err)
	}
	got, wrote, err = s.StampJobRan(ctx, job.ID, SizeLarge)
	if err != nil || wrote || got.RanClass != SizeMedium {
		t.Fatalf("a second observation rewrote where the job ran, or said it did: %+v, wrote %t, %v", got, wrote, err)
	}
	other := seedJob(t, s, 2, JobInProgress, "run_2")
	if got, wrote, _ := s.StampJobRan(ctx, other.ID, ""); wrote || got.RanClass != "" {
		t.Fatalf("a host with no class stamped %q (said it wrote: %t)", got.RanClass, wrote)
	}
	if _, _, err := s.StampJobRan(ctx, "job_missing", SizeSmall); !errors.Is(err, ErrNotFound) {
		t.Fatalf("StampJobRan on a missing job = %v, want ErrNotFound", err)
	}
}

func TestACPUThrottleSampleIsKeptAgainstTheRunningJobAndOnlyRises(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	running := usageJob(t, s, 1, "build", "pool_a", "run_1", JobInProgress, versionsEpoch)
	done := usageJob(t, s, 2, "build", "pool_a", "run_1", JobCompleted, versionsEpoch.Add(-time.Hour))
	if _, ok := running.Throttled(); ok {
		t.Fatal("a job that was never sampled has a throttle share")
	}

	for _, sample := range [][2]int64{{100, 10}, {400, 120}, {250, 90}, {0, 0}, {500, -5}} {
		if err := s.RecordJobThrottle(ctx, "run_1", sample[0], sample[1]); err != nil {
			t.Fatalf("RecordJobThrottle: %v", err)
		}
	}
	got, _ := s.GetJob(ctx, running.ID)
	if got.CPUPeriods != 500 || got.CPUThrottledPeriods != 120 {
		t.Fatalf("running job's counters = %d, %d; want the highest seen, 500 and 120", got.CPUPeriods, got.CPUThrottledPeriods)
	}
	if share, ok := got.Throttled(); !ok || share != 0.24 {
		t.Fatalf("Throttled() = %v, %v, want 0.24", share, ok)
	}
	if other, _ := s.GetJob(ctx, done.ID); other.CPUPeriods != 0 {
		t.Fatalf("a finished job was charged a later sample: %d periods", other.CPUPeriods)
	}
	if err := s.RecordJobThrottle(ctx, "", 10, 1); err != nil {
		t.Fatalf("a sample with no runner is not an error: %v", err)
	}

	// A counter that ran past its periods is a rounding of the sampler's, and
	// is not a job throttled more than all of the time.
	over := Job{CPUPeriods: 10, CPUThrottledPeriods: 14}
	if share, _ := over.Throttled(); share != 1 {
		t.Fatalf("Throttled() = %v for more throttled periods than periods, want 1", share)
	}
}

// ---------------------------------------------------------------------------
// A job's class between runs
// ---------------------------------------------------------------------------

func TestAJobsClassIsKeptAndSaysSinceWhenItHasBeenThere(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	s := newTestStoreAt(t, func() time.Time { return now })

	if _, err := s.GetJobClass(ctx, "acme/api", "CI", "build"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a job never classed = %v, want ErrNotFound", err)
	}
	c := &JobClass{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: SizeMedium,
		Reason: "memory peaked at 6 GB over 8 runs", Basis: SizeBasisHistory, FloorMB: 7232, Runs: 8}
	previous, err := s.PutJobClass(ctx, c)
	if err != nil || previous != "" {
		t.Fatalf("PutJobClass of a new job = %q, %v; want no previous class", previous, err)
	}
	got, err := s.GetJobClass(ctx, "acme/api", "CI", "build")
	if err != nil {
		t.Fatal(err)
	}
	if got.Class != SizeMedium || got.Runs != 8 || got.FloorMB != 7232 || got.Reason == "" || !got.MovedAt.Equal(t0) || !got.ComputedAt.Equal(t0) {
		t.Fatalf("the class did not round trip: %+v", got)
	}

	// Looking again and finding the same answer moves when it was last looked
	// at, not since when the job has been in this class.
	now = t0.Add(24 * time.Hour)
	previous, err = s.PutJobClass(ctx, &JobClass{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: SizeMedium,
		Reason: "memory peaked at 6 GB over 12 runs", Basis: SizeBasisHistory, Runs: 12})
	if err != nil || previous != SizeMedium {
		t.Fatalf("PutJobClass = %q, %v; want the medium it replaced", previous, err)
	}
	got, _ = s.GetJobClass(ctx, "acme/api", "CI", "build")
	if !got.MovedAt.Equal(t0) || !got.ComputedAt.Equal(t0.Add(24*time.Hour)) || got.Runs != 12 {
		t.Fatalf("an unchanged class moved its moved_at: %+v", got)
	}

	now = t0.Add(48 * time.Hour)
	previous, _ = s.PutJobClass(ctx, &JobClass{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: SizeLarge,
		Reason: "killed for memory", Basis: SizeBasisHistory, Runs: 13})
	got, _ = s.GetJobClass(ctx, "acme/api", "CI", "build")
	if previous != SizeMedium || got.Class != SizeLarge || !got.MovedAt.Equal(t0.Add(48*time.Hour)) {
		t.Fatalf("a move was not recorded as one: previous %q, %+v", previous, got)
	}

	// The key is the whole of repository, workflow and job.
	if _, err := s.GetJobClass(ctx, "acme/api", "CI", "test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another job of the same workflow shares the class: %v", err)
	}
	if _, err := s.PutJobClass(ctx, &JobClass{Repo: "acme/api", Workflow: "CI", JobName: "x", Class: "huge"}); err == nil {
		t.Fatal("a class that is not one was stored")
	}
}

func TestJobClassHistoryIsTheJobsMeasuredRunsInTheWindow(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	const repo, workflow, job = "eyupio/zoomies", "CI", "Go (controller)"
	n := 0
	record := func(pool string, at time.Time, mb int64, cpus float64, oom bool, periods, throttled int64, ran SizeClass) {
		n++
		runner := "run_" + strings.Repeat("x", n)
		usageJob(t, s, n, job, pool, runner, JobInProgress, at)
		if mb > 0 || cpus > 0 {
			if err := s.RecordJobUsage(ctx, runner, cpus, mb); err != nil {
				t.Fatal(err)
			}
		}
		if periods > 0 {
			if err := s.RecordJobThrottle(ctx, runner, periods, throttled); err != nil {
				t.Fatal(err)
			}
		}
		done := at.Add(7 * time.Minute)
		j, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: int64(5000 + n), State: JobCompleted, Conclusion: "success",
			Repo: repo, Workflow: workflow, JobName: job, StartedAt: &at, CompletedAt: &done})
		if err != nil {
			t.Fatal(err)
		}
		if ran != "" {
			if _, _, err := s.StampJobRan(ctx, j.ID, ran); err != nil {
				t.Fatal(err)
			}
		}
		if oom {
			if _, _, err := s.MarkJobOOMKilled(ctx, runner, "killed"); err != nil {
				t.Fatal(err)
			}
		}
	}

	record("pool_a", versionsEpoch.Add(-40*24*time.Hour), 9000, 3, false, 0, 0, "") // outside the window
	record("pool_a", versionsEpoch.Add(-3*24*time.Hour), 4000, 2.5, false, 200, 80, SizeSmall)
	record("pool_b", versionsEpoch.Add(-2*24*time.Hour), 6000, 3.9, true, 0, 0, SizeMedium) // another pool, same job
	record("pool_a", versionsEpoch.Add(-time.Hour), 0, 0, false, 0, 0, "")                  // never measured

	got, err := s.JobClassHistory(ctx, repo, workflow, job, versionsEpoch.Add(-14*24*time.Hour), JobUsageHistoryLimit)
	if err != nil {
		t.Fatalf("JobClassHistory: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("history has %d runs, want the two measured ones inside the window: %+v", len(got), got)
	}
	newest, older := got[0], got[1]
	if !newest.OOMKilled || newest.PeakMemoryMB != 6000 || newest.RanClass != SizeMedium {
		t.Fatalf("the newest run, from the other pool, is %+v", newest)
	}
	if older.Duration != 7*time.Minute || older.PeakMemoryMB != 4000 || older.PeakCPUs != 2.5 || older.RanClass != SizeSmall {
		t.Fatalf("the older run is %+v", older)
	}
	if share, ok := older.Throttled(); !ok || share != 0.4 {
		t.Fatalf("the older run's throttle share = %v, %v, want 0.4", share, ok)
	}
	if _, ok := newest.Throttled(); ok {
		t.Fatal("a run whose counters were never sampled has a throttle share")
	}

	if limited, _ := s.JobClassHistory(ctx, repo, workflow, job, versionsEpoch.Add(-14*24*time.Hour), 1); len(limited) != 1 || !limited[0].OOMKilled {
		t.Fatalf("a limit of one returned %+v, want the newest run only", limited)
	}
	if none, _ := s.JobClassHistory(ctx, repo, workflow, "another job", versionsEpoch.Add(-14*24*time.Hour), 20); len(none) != 0 {
		t.Fatalf("another job's history includes %+v", none)
	}
}

// The history is read when each job finishes, so it must seek its index rather
// than walk a jobs table that grows without bound.
func TestJobClassHistorySeeksItsIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+jobClassHistorySQL, "r", "w", "j", 0, 20)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "\n"); !strings.Contains(joined, "idx_jobs_usage_profile") {
		t.Fatalf("the history lookup does not use its index:\n%s", joined)
	}
}

// Label advice compares what a job's runs call for with what its workflow asks
// for, and the second half is whatever the latest measured run carried.
func TestAKeptClassComesWithTheLabelsOfTheJobsLatestMeasuredRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	run := func(n int64, job string, labels []string, at time.Time, mb int64) {
		t.Helper()
		started := at
		done := at.Add(5 * time.Minute)
		runner := "run_" + job + strings.Repeat("x", int(n))
		if _, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: n, Repo: "acme/api", Workflow: "CI", JobName: job, Labels: labels,
			State: JobInProgress, RunnerID: runner, QueuedAt: at, StartedAt: &started}); err != nil {
			t.Fatal(err)
		}
		if mb > 0 {
			if err := s.RecordJobUsage(ctx, runner, 1, mb); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := s.ApplyJob(ctx, &Job{GitHubJobID: n, Repo: "acme/api", Workflow: "CI", JobName: job, Labels: labels,
			State: JobCompleted, Conclusion: "success", StartedAt: &started, CompletedAt: &done}); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range []string{"build", "lint", "never measured"} {
		if _, err := s.PutJobClass(ctx, &JobClass{Repo: "acme/api", Workflow: "CI", JobName: job, Class: SizeMedium,
			Reason: "memory", Basis: SizeBasisHistory, Runs: 3}); err != nil {
			t.Fatal(err)
		}
	}
	// build was written for the small class first and later for the large one:
	// what it asks for now is what counts.
	run(1, "build", []string{"self-hosted", "zoomies-small"}, versionsEpoch.Add(-48*time.Hour), 900)
	run(2, "build", []string{"self-hosted", "zoomies-large"}, versionsEpoch.Add(-24*time.Hour), 900)
	run(3, "lint", []string{"self-hosted", "zoomies"}, versionsEpoch.Add(-time.Hour), 400)
	// A run nobody measured says nothing about what is asked for.
	run(4, "never measured", []string{"self-hosted", "zoomies-small"}, versionsEpoch.Add(-time.Hour), 0)

	got, err := s.ListJobClassesAsked(ctx)
	if err != nil {
		t.Fatalf("ListJobClassesAsked: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("%d classes listed, want the three kept: %+v", len(got), got)
	}
	by := map[string]*JobClassAsked{}
	for _, c := range got {
		by[c.JobName] = c
	}
	if l := by["build"].Labels; len(l) != 2 || l[1] != "zoomies-large" {
		t.Fatalf("build asks for %v; the latest run asked for zoomies-large", l)
	}
	if l := by["lint"].Labels; len(l) != 2 || l[1] != "zoomies" {
		t.Fatalf("lint asks for %v", l)
	}
	if l := by["never measured"].Labels; len(l) != 0 {
		t.Fatalf("a job with no measured run asks for %v; there is nothing to compare", l)
	}
	if by["build"].Class != SizeMedium || by["build"].Runs != 3 || by["build"].Reason == "" {
		t.Fatalf("the class did not come with its labels: %+v", by["build"].JobClass)
	}
	if got[0].JobName != "build" || got[1].JobName != "lint" || got[2].JobName != "never measured" {
		t.Fatalf("the classes are not in order: %s, %s, %s", got[0].JobName, got[1].JobName, got[2].JobName)
	}
}

// The labels are looked up for every kept class, so each lookup must seek.
func TestTheLabelLookupSeeksTheHistoryIndex(t *testing.T) {
	s := newTestStore(t)
	rows, err := s.read.QueryContext(t.Context(), "EXPLAIN QUERY PLAN "+jobClassesAskedSQL)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	if joined := strings.Join(plan, "\n"); !strings.Contains(joined, "idx_jobs_usage_profile") {
		t.Fatalf("the label lookup does not use the history index:\n%s", joined)
	}
}

// A class is kept for as long as its job runs, and for as long as the runs it
// was worked out from are: after that there is nothing behind it, and a row for
// every job a repository ever had would be read by every pass for ever.
func TestAClassForAJobThatHasNotRunForTheRetentionIsPruned(t *testing.T) {
	ctx := context.Background()
	t0 := time.Date(2026, 5, 1, 9, 0, 0, 0, time.UTC)
	now := t0
	s := newTestStoreAt(t, func() time.Time { return now })
	keep := func(job string) {
		t.Helper()
		if _, err := s.PutJobClass(ctx, &JobClass{Repo: "acme/api", Workflow: "CI", JobName: job, Class: SizeLarge, Basis: SizeBasisHistory, Runs: 9}); err != nil {
			t.Fatal(err)
		}
	}
	keep("retired")
	now = t0.Add(60 * 24 * time.Hour)
	keep("running")

	n, err := s.PruneJobClasses(ctx, now.Add(-30*24*time.Hour))
	if err != nil || n != 1 {
		t.Fatalf("PruneJobClasses = %d, %v; want the one job idle for two months", n, err)
	}
	if _, err := s.GetJobClass(ctx, "acme/api", "CI", "retired"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the idle job's class survived: %v", err)
	}
	if _, err := s.GetJobClass(ctx, "acme/api", "CI", "running"); err != nil {
		t.Fatalf("a job that ran today lost its class: %v", err)
	}
	// A second pass finds nothing left to remove.
	if n, err := s.PruneJobClasses(ctx, now.Add(-30*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("a second prune removed %d, %v", n, err)
	}
}

// ---------------------------------------------------------------------------
// Pins
// ---------------------------------------------------------------------------

func TestAPinIsForOneJobOrAWholeRepositoryAndTheJobsWins(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if _, err := s.SizePinFor(ctx, "acme/api", "CI", "build"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing pinned = %v, want ErrNotFound", err)
	}
	for name, p := range map[string]SizePin{
		"no repository":                 {Class: SizeLarge},
		"a repository with no owner":    {Repo: "api", Class: SizeLarge},
		"a class that is not one":       {Repo: "acme/api", Class: "huge"},
		"a workflow with no job":        {Repo: "acme/api", Workflow: "CI", Class: SizeLarge},
		"a job with no workflow":        {Repo: "acme/api", JobName: "build", Class: SizeLarge},
		"a repository and nothing else": {Repo: "", Workflow: "CI", JobName: "build", Class: SizeLarge},
		// A slash is not an owner and a name, and none of these could ever match
		// a repository GitHub reports.
		"a repository that is only a slash":      {Repo: "/", Class: SizeLarge},
		"a repository with no name":              {Repo: "acme/", Class: SizeLarge},
		"a repository with a slash and no owner": {Repo: "/api", Class: SizeLarge},
		"a repository with three parts":          {Repo: "acme/api/extra", Class: SizeLarge},
		"a repository with a space in it":        {Repo: "acme/my api", Class: SizeLarge},
		"a repository that is far too long":      {Repo: "acme/" + strings.Repeat("a", MaxPinText), Class: SizeLarge},
		"a workflow with a space at its end":     {Repo: "acme/api", Workflow: "CI ", JobName: "build", Class: SizeLarge},
		"a job name with a control character":    {Repo: "acme/api", Workflow: "CI", JobName: "build\x1b[31m", Class: SizeLarge},
		"a job name that is far too long":        {Repo: "acme/api", Workflow: "CI", JobName: strings.Repeat("j", MaxPinText+1), Class: SizeLarge},
	} {
		if err := s.SetSizePin(ctx, &p); err == nil {
			t.Errorf("a pin with %s was accepted", name)
		}
	}

	repoPin := &SizePin{Repo: "Acme/API", Class: SizeMedium, CreatedBy: "ada"}
	if err := s.SetSizePin(ctx, repoPin); err != nil {
		t.Fatalf("SetSizePin: %v", err)
	}
	if repoPin.Repo != "acme/api" || repoPin.CreatedAt.IsZero() {
		t.Fatalf("the pin was not normalised on the way in: %+v", repoPin)
	}
	jobPin := &SizePin{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: SizeLarge, CreatedBy: "ada"}
	if err := s.SetSizePin(ctx, jobPin); err != nil {
		t.Fatal(err)
	}

	// The repository's pin reaches every other job, however GitHub cases the
	// repository's name; the job's own pin wins over it.
	got, err := s.SizePinFor(ctx, "ACME/api", "CI", "test")
	if err != nil || got.Class != SizeMedium || !got.ForRepository() {
		t.Fatalf("another job's pin = %+v, %v; want the repository's medium", got, err)
	}
	got, err = s.SizePinFor(ctx, "acme/api", "CI", "build")
	if err != nil || got.Class != SizeLarge || got.ForRepository() || got.CreatedBy != "ada" {
		t.Fatalf("the pinned job's pin = %+v, %v; want its own large", got, err)
	}
	if _, err := s.SizePinFor(ctx, "acme/web", "CI", "build"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another repository inherited a pin: %v", err)
	}

	// Pinning again replaces rather than duplicates.
	if err := s.SetSizePin(ctx, &SizePin{Repo: "acme/api", Workflow: "CI", JobName: "build", Class: SizeSmall}); err != nil {
		t.Fatal(err)
	}
	pins, err := s.ListSizePins(ctx)
	if err != nil || len(pins) != 2 {
		t.Fatalf("ListSizePins = %d pins, %v; want the repository's and the job's", len(pins), err)
	}
	if !pins[0].ForRepository() || pins[1].Class != SizeSmall {
		t.Fatalf("pins listed out of order or not replaced: %+v %+v", pins[0], pins[1])
	}

	if err := s.DeleteSizePin(ctx, "ACME/api", "CI", "build"); err != nil {
		t.Fatalf("DeleteSizePin: %v", err)
	}
	if got, _ := s.SizePinFor(ctx, "acme/api", "CI", "build"); got == nil || !got.ForRepository() {
		t.Fatalf("with the job's pin gone the repository's should apply again: %+v", got)
	}
	if err := s.DeleteSizePin(ctx, "acme/api", "CI", "build"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleting a pin twice = %v, want ErrNotFound", err)
	}
}

// A form puts each sentence beside the field it is about, so the rule says which
// field that is, and says everything wrong at once rather than one thing a try.
func TestAPinSaysWhatIsWrongWithEachFieldItself(t *testing.T) {
	fields := func(p SizePin, checkClass bool) []string {
		var out []string
		for _, problem := range p.Problems(checkClass) {
			if problem.Message == "" {
				t.Errorf("a problem with %s has no sentence", problem.Field)
			}
			out = append(out, problem.Field)
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		pin        SizePin
		checkClass bool
		want       []string
	}{
		{"a sound pin", SizePin{Repo: " Acme/API ", Workflow: "CI", JobName: "build (linux)", Class: SizeLarge}, true, nil},
		{"a sound repository pin", SizePin{Repo: "acme/api", Class: SizeSmall}, true, nil},
		{"everything wrong", SizePin{Repo: "/", Workflow: "CI", Class: "huge"}, true, []string{"repo", "class", "job_name"}},
		{"a class that does not matter when removing", SizePin{Repo: "acme/api", Class: "huge"}, false, nil},
		{"a repository that is only a slash", SizePin{Repo: "/", Class: SizeSmall}, true, []string{"repo"}},
		{"names that cannot match", SizePin{Repo: "acme/api", Workflow: " CI", JobName: "build\n", Class: SizeSmall}, true, []string{"workflow", "job_name"}},
	} {
		if got := fields(tc.pin, tc.checkClass); !slices.Equal(got, tc.want) {
			t.Errorf("%s: problems with %v, want %v", tc.name, got, tc.want)
		}
	}
	if err := (SizePin{Repo: "acme/", Class: SizeSmall}).Validate(); err == nil || !strings.Contains(err.Error(), "owner/name") {
		t.Errorf("Validate said %v for a repository with no name", err)
	}
}

// ---------------------------------------------------------------------------
// The upgrade
// ---------------------------------------------------------------------------

// 0072 and 0073 must change nothing for a fleet that has not asked for them:
// every host reads as unclassified, every pool as one an operator made, every
// job as never classed, and the tables they add are empty.
func TestAnInstallFromBeforeSizeRoutingUpgradesUnchanged(t *testing.T) {
	ctx := context.Background()
	path := atSchemaBefore(t, "0072_size_classes.sql")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO installations (id, app_id, installation_id, target, target_type, created_at, updated_at)
			VALUES ('inst_old', 1, 2, 'acme', 'org', 1, 1)`,
		`INSERT INTO pools (id, name, installation_id, backend, created_at, updated_at) VALUES ('pool_old', 'zoomies-old', 'inst_old', 'docker', 1, 1)`,
		`INSERT INTO pools (id, name, installation_id, backend, created_at, updated_at) VALUES ('pool_old2', 'zoomies-old2', 'inst_old', 'docker', 1, 1)`,
		`INSERT INTO hosts (id, name, created_at) VALUES ('host_old', 'old', 1)`,
		`INSERT INTO jobs (id, github_job_id, state, queued_at) VALUES ('job_old', 7, 'queued', 1)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seeding the old schema: %v: %s", err, stmt)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, Options{Path: path})
	if err != nil {
		t.Fatalf("upgrading: %v", err)
	}
	defer s.Close()

	h, err := s.GetHost(ctx, "host_old")
	if err != nil {
		t.Fatalf("reading an upgraded host: %v", err)
	}
	if h.SizeClass.Set() || h.SelectorValue(LabelSize) != "" {
		t.Fatalf("an upgraded host has a class: %+v", h.SizeClass)
	}
	for _, id := range []string{"pool_old", "pool_old2"} {
		p, err := s.GetPool(ctx, id)
		if err != nil {
			t.Fatalf("reading an upgraded pool: %v", err)
		}
		if p.FromHosts() || p.AutoMin != 0 || p.AutoCap != 0 || p.AutoPaused {
			t.Fatalf("an upgraded pool is taken for one the controller keeps: %+v", p)
		}
	}
	j, err := s.GetJob(ctx, "job_old")
	if err != nil {
		t.Fatalf("reading an upgraded job: %v", err)
	}
	if j.SizeClass != "" || j.SizeBasis != "" || j.SizeReason != "" || j.RoutedClass != "" || j.RanClass != "" ||
		j.CPUPeriods != 0 || j.CPUThrottledPeriods != 0 {
		t.Fatalf("an upgraded job claims a class: %+v", j)
	}
	if pins, _ := s.ListSizePins(ctx); len(pins) != 0 {
		t.Fatalf("the upgrade pinned something: %+v", pins)
	}
	if _, err := s.GetJobClass(ctx, "a/b", "w", "j"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the upgrade classed a job: %v", err)
	}

	for _, col := range []struct{ table, name string }{
		{"hosts", "size_class"}, {"pools", "auto_key"}, {"pools", "auto_min"}, {"pools", "auto_cap"},
		{"pools", "auto_paused"}, {"jobs", "size_class"}, {"jobs", "size_basis"}, {"jobs", "size_floor_mb"}, {"jobs", "routed_class"},
		{"jobs", "ran_class"}, {"jobs", "cpu_periods"}, {"jobs", "cpu_throttled_periods"},
	} {
		var notNull, hasDefault int
		if err := s.read.QueryRowContext(ctx,
			`SELECT "notnull", dflt_value IS NOT NULL FROM pragma_table_info(?) WHERE name = ?`,
			col.table, col.name).Scan(&notNull, &hasDefault); err != nil {
			t.Fatalf("reading %s.%s: %v", col.table, col.name, err)
		}
		if notNull != 1 || hasDefault != 1 {
			t.Fatalf("%s.%s arrived without a NOT NULL default; existing rows would read as unset", col.table, col.name)
		}
	}
}

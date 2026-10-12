package lifecycle

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/onatm/boxface/internal/store"
)

const (
	owner      = "owner-1"
	otherOwner = "owner-2"
	image      = "image-1"
)

type fakeWorker struct {
	mu      sync.Mutex
	calls   []string
	disks   map[string]bool
	vms     map[string]bool
	block   map[string]chan struct{}
	entered chan string
	fail    map[string]error
	flaky   map[string]int
	active  map[string]bool
}

func newFakeWorker() *fakeWorker {
	return &fakeWorker{disks: map[string]bool{}, vms: map[string]bool{}, block: map[string]chan struct{}{}, entered: make(chan string, 16), fail: map[string]error{}, flaky: map[string]int{}}
}

func (f *fakeWorker) call(ctx context.Context, name string, apply func()) error {
	f.mu.Lock()
	gate := f.block[name]
	err := f.fail[name]
	if f.flaky[name] > 0 {
		f.flaky[name]--
		err = fmt.Errorf("%w: connection refused", ErrWorkerUnavailable)
	}
	f.mu.Unlock()
	if gate != nil {
		f.entered <- name
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
	if apply != nil {
		apply()
	}
	return nil
}

func (f *fakeWorker) CreateDisk(ctx context.Context, spec DiskSpec) error {
	return f.call(ctx, "CreateDisk", func() { f.disks[spec.DiskID] = true })
}
func (f *fakeWorker) RemoveDisk(ctx context.Context, diskID string) error {
	return f.call(ctx, "RemoveDisk", func() { delete(f.disks, diskID) })
}
func (f *fakeWorker) CreateNetwork(ctx context.Context, id string) error {
	return f.call(ctx, "CreateNetwork", nil)
}
func (f *fakeWorker) RemoveNetwork(ctx context.Context, id string) error {
	return f.call(ctx, "RemoveNetwork", nil)
}
func (f *fakeWorker) StartVM(ctx context.Context, spec VMSpec) error {
	return f.call(ctx, "StartVM", func() { f.vms[spec.EnvironmentID] = true })
}
func (f *fakeWorker) StopVM(ctx context.Context, id string) error {
	return f.call(ctx, "StopVM", func() { delete(f.vms, id) })
}
func (f *fakeWorker) WaitReady(ctx context.Context, id string) error {
	return f.call(ctx, "WaitReady", nil)
}

func (f *fakeWorker) ActiveVMs(context.Context) (map[string]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.vms), nil
}

func (f *fakeWorker) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "boxface.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`INSERT INTO owners (id, name, created_at) VALUES ('owner-1', 'one', '2026-10-09T00:00:00Z'), ('owner-2', 'two', '2026-10-09T00:00:00Z')`,
		`INSERT INTO images (id, digest, status, created_at) VALUES ('image-1', 'sha256:test', 'ready', '2026-10-09T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func newService(t *testing.T, db *sql.DB, w Worker) (*Service, *Runner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r := NewRunner(ctx, db, w)
	r.RetryDelay = time.Millisecond
	t.Cleanup(func() { cancel(); r.Wait() })
	return NewService(db, r, Capacity{}), r, cancel
}

var travel = CreateRequest{Name: "travel", ImageID: image, VCPUs: 2, MemoryMiB: 2048, DiskGiB: 20}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func mustOperation(t *testing.T, s *Service, id string) Operation {
	t.Helper()
	op, err := s.Operation(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func mustEnvironment(t *testing.T, s *Service, id string) Environment {
	t.Helper()
	env, err := s.Environment(context.Background(), owner, id)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestCreateRunsToRunning(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	s, r, _ := newService(t, db, w)

	acc, err := s.Create(context.Background(), owner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()

	if op := mustOperation(t, s, acc.OperationID); op.State != "succeeded" || op.TargetID != acc.EnvironmentID {
		t.Fatalf("operation = %+v", op)
	}
	if env := mustEnvironment(t, s, acc.EnvironmentID); env.ObservedState != "running" || env.DesiredState != "running" || env.DiskRetention != "attached" {
		t.Fatalf("environment = %+v", env)
	}
	if got, want := w.callLog(), []string{"CreateDisk", "CreateNetwork", "StartVM", "WaitReady"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if n := count(t, db, `SELECT count(*) FROM audit_events WHERE action = 'environment.create'`); n != 1 {
		t.Fatalf("audit events = %d", n)
	}
}

func TestRetriedCreateReturnsSameOperation(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	s, r, _ := newService(t, db, w)

	first, err := s.Create(context.Background(), owner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	second, err := s.Create(context.Background(), owner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()

	if first != second {
		t.Fatalf("retry returned %+v, first %+v", second, first)
	}
	if n := count(t, db, `SELECT count(*) FROM operations`); n != 1 {
		t.Fatalf("operations = %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM data_disks`); n != 1 {
		t.Fatalf("data disks = %d", n)
	}
	got := w.callLog()
	if n := len(slices.DeleteFunc(slices.Clone(got), func(c string) bool { return c != "CreateDisk" })); n != 1 {
		t.Fatalf("CreateDisk called %d times: %v", n, got)
	}
}

func TestReusedKeyWithDifferentParametersFails(t *testing.T) {
	db := openDB(t)
	s, r, _ := newService(t, db, newFakeWorker())

	if _, err := s.Create(context.Background(), owner, "key-1", travel); err != nil {
		t.Fatal(err)
	}
	changed := travel
	changed.MemoryMiB = 4096
	if _, err := s.Create(context.Background(), owner, "key-1", changed); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("err = %v, want ErrIdempotencyConflict", err)
	}
	r.Wait()
	if n := count(t, db, `SELECT count(*) FROM environments`); n != 1 {
		t.Fatalf("environments = %d", n)
	}
}

func TestIdempotencyKeysAreScopedByOwnerAndType(t *testing.T) {
	db := openDB(t)
	s, r, _ := newService(t, db, newFakeWorker())

	mine, err := s.Create(context.Background(), owner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.Create(context.Background(), otherOwner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	stop, err := s.Stop(context.Background(), owner, "key-1", mine.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if mine == theirs || stop.OperationID == mine.OperationID {
		t.Fatalf("keys shared across owner or type: %+v %+v %+v", mine, theirs, stop)
	}
}

func TestConflictingMutationsSerialise(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	release := make(chan struct{})
	w.block["StartVM"] = release
	s, r, _ := newService(t, db, w)

	created, err := s.Create(context.Background(), owner, "", travel)
	if err != nil {
		t.Fatal(err)
	}
	<-w.entered
	stopped, err := s.Stop(context.Background(), owner, "", created.EnvironmentID)
	if err != nil {
		t.Fatal(err)
	}
	if op := mustOperation(t, s, stopped.OperationID); op.State != "queued" {
		t.Fatalf("stop ran while create was in progress: %+v", op)
	}
	close(release)
	r.Wait()

	if got, want := w.callLog(), []string{"CreateDisk", "CreateNetwork", "StartVM", "WaitReady", "StopVM", "RemoveNetwork"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if env := mustEnvironment(t, s, created.EnvironmentID); env.ObservedState != "stopped" || env.DesiredState != "stopped" {
		t.Fatalf("environment = %+v", env)
	}
}

func TestRunnerResumesFromLastCompletedStep(t *testing.T) {
	db := openDB(t)
	first := newFakeWorker()
	first.block["StartVM"] = make(chan struct{})
	s1, r1, stopFirst := newService(t, db, first)

	acc, err := s1.Create(context.Background(), owner, "key-1", travel)
	if err != nil {
		t.Fatal(err)
	}
	<-first.entered
	stopFirst()
	r1.Wait()

	interrupted := mustOperation(t, s1, acc.OperationID)
	if interrupted.State != "running" || !slices.Equal(interrupted.CompletedSteps, []string{"create_disk", "create_network"}) {
		t.Fatalf("interrupted operation = %+v", interrupted)
	}

	second := newFakeWorker()
	s2, r2, _ := newService(t, db, second)
	r2.Kick()
	r2.Wait()

	if op := mustOperation(t, s2, acc.OperationID); op.State != "succeeded" {
		t.Fatalf("resumed operation = %+v", op)
	}
	if got, want := second.callLog(), []string{"StartVM", "WaitReady"}; !slices.Equal(got, want) {
		t.Fatalf("resumed calls = %v, want %v", got, want)
	}
	if env := mustEnvironment(t, s2, acc.EnvironmentID); env.ObservedState != "running" {
		t.Fatalf("environment = %+v", env)
	}
}

func TestFailedLaunchCleansUpAndKeepsDisk(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	w.fail["WaitReady"] = errors.New("VM exited before reporting readiness")
	s, r, _ := newService(t, db, w)

	acc, err := s.Create(context.Background(), owner, "", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()

	op := mustOperation(t, s, acc.OperationID)
	if op.State != "failed" || op.ErrorCode != "step_failed" || op.ErrorMessage != "wait_ready: VM exited before reporting readiness" {
		t.Fatalf("operation = %+v", op)
	}
	env := mustEnvironment(t, s, acc.EnvironmentID)
	if env.ObservedState != "stopped" || env.DesiredState != "stopped" || env.Error != op.ErrorMessage || env.DiskRetention != "attached" {
		t.Fatalf("environment = %+v", env)
	}
	if got, want := w.callLog(), []string{"CreateDisk", "CreateNetwork", "StartVM", "StopVM", "RemoveNetwork"}; !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	if !w.disks[env.DiskID] || w.vms[acc.EnvironmentID] {
		t.Fatalf("disks %v, vms %v", w.disks, w.vms)
	}
}

func TestFailedCleanupLeavesEnvironmentInError(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	w.fail["WaitReady"] = errors.New("timed out")
	w.fail["RemoveNetwork"] = errors.New("ip link del failed")
	s, r, _ := newService(t, db, w)

	acc, _ := s.Create(context.Background(), owner, "", travel)
	r.Wait()
	env := mustEnvironment(t, s, acc.EnvironmentID)
	if env.ObservedState != "error" || env.Error != "wait_ready: timed out; cleanup remove_network failed: ip link del failed" {
		t.Fatalf("environment = %+v", env)
	}
}

func TestUnavailableWorkerRetriesStep(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	w.flaky["StartVM"] = 3
	s, r, _ := newService(t, db, w)

	acc, _ := s.Create(context.Background(), owner, "", travel)
	r.Wait()
	if op := mustOperation(t, s, acc.OperationID); op.State != "succeeded" {
		t.Fatalf("operation = %+v", op)
	}
	if w.flaky["StartVM"] != 0 {
		t.Fatalf("StartVM retried too few times: %d attempts left", w.flaky["StartVM"])
	}
}

func TestReconcileStopsEnvironmentsWithoutVMs(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	s, r, _ := newService(t, db, w)

	gone, _ := s.Create(context.Background(), owner, "", travel)
	kept := travel
	kept.Name = "kept"
	alive, _ := s.Create(context.Background(), owner, "", kept)
	r.Wait()
	busyReq := travel
	busyReq.Name = "busy"
	busy, _ := s.Create(context.Background(), owner, "", busyReq)
	r.Wait()
	if _, err := db.Exec(`INSERT INTO operations (id, owner_id, type, target_id, request_hash, params, state, created_at, updated_at)
		VALUES ('held', ?, 'environment.start', ?, '', '{}', 'running', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z')`, owner, busy.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	delete(w.vms, gone.EnvironmentID)
	delete(w.vms, busy.EnvironmentID)
	w.mu.Unlock()

	r.mu.Lock()
	r.active[busy.EnvironmentID] = true
	r.mu.Unlock()
	stopped, err := r.Reconcile(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stopped, []string{gone.EnvironmentID}) {
		t.Fatalf("stopped = %v, want only %s", stopped, gone.EnvironmentID)
	}
	if env := mustEnvironment(t, s, gone.EnvironmentID); env.ObservedState != "stopped" || env.DesiredState != "stopped" || env.Error != reconciledMessage {
		t.Fatalf("gone = %+v", env)
	}
	if env := mustEnvironment(t, s, alive.EnvironmentID); env.ObservedState != "running" {
		t.Fatalf("alive = %+v", env)
	}
	if env := mustEnvironment(t, s, busy.EnvironmentID); env.ObservedState != "running" {
		t.Fatalf("environment with a pending operation was reconciled: %+v", env)
	}
	if got := w.callLog(); got[len(got)-1] != "RemoveNetwork" {
		t.Fatalf("network not removed: %v", got)
	}
}

func TestDeleteRetainsDiskUnlessPurged(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	s, r, _ := newService(t, db, w)

	kept, err := s.Create(context.Background(), owner, "", travel)
	if err != nil {
		t.Fatal(err)
	}
	other := travel
	other.Name = "experiment"
	purged, err := s.Create(context.Background(), owner, "", other)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if _, err := s.Delete(context.Background(), owner, "", kept.EnvironmentID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Delete(context.Background(), owner, "", purged.EnvironmentID, true); err != nil {
		t.Fatal(err)
	}
	r.Wait()

	keptEnv := mustEnvironment(t, s, kept.EnvironmentID)
	purgedEnv := mustEnvironment(t, s, purged.EnvironmentID)
	if keptEnv.ObservedState != "deleted" || keptEnv.DiskRetention != "retained" || !w.disks[keptEnv.DiskID] {
		t.Fatalf("kept environment = %+v", keptEnv)
	}
	if purgedEnv.ObservedState != "deleted" || purgedEnv.DiskRetention != "purged" || w.disks[purgedEnv.DiskID] {
		t.Fatalf("purged environment = %+v", purgedEnv)
	}
	if _, err := s.Start(context.Background(), owner, "", kept.EnvironmentID); !errors.Is(err, ErrEnvironmentDeleted) {
		t.Fatalf("start after delete: err = %v", err)
	}
	if _, err := s.Create(context.Background(), owner, "", travel); err != nil {
		t.Fatalf("name not reusable after delete: %v", err)
	}
	r.Wait()
}

func TestNameTakenWhileEnvironmentExists(t *testing.T) {
	db := openDB(t)
	s, r, _ := newService(t, db, newFakeWorker())

	if _, err := s.Create(context.Background(), owner, "", travel); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(context.Background(), owner, "", travel); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("err = %v, want ErrNameTaken", err)
	}
	if _, err := s.Create(context.Background(), otherOwner, "", travel); err != nil {
		t.Fatalf("other owner cannot use the name: %v", err)
	}
	r.Wait()
}

func TestOtherOwnerCannotSeeOrChangeEnvironment(t *testing.T) {
	db := openDB(t)
	s, r, _ := newService(t, db, newFakeWorker())

	acc, err := s.Create(context.Background(), owner, "", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if _, err := s.Environment(context.Background(), otherOwner, acc.EnvironmentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("environment: err = %v", err)
	}
	if _, err := s.Operation(context.Background(), otherOwner, acc.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("operation: err = %v", err)
	}
	if _, err := s.Stop(context.Background(), otherOwner, "", acc.EnvironmentID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stop: err = %v", err)
	}
}

func TestCreateValidation(t *testing.T) {
	db := openDB(t)
	s, _, _ := newService(t, db, newFakeWorker())

	cases := map[string]struct {
		req  CreateRequest
		want error
	}{
		"bad name":      {CreateRequest{Name: "Travel", ImageID: image, VCPUs: 1, MemoryMiB: 512, DiskGiB: 1}, ErrInvalid},
		"no vcpus":      {CreateRequest{Name: "travel", ImageID: image, VCPUs: 0, MemoryMiB: 512, DiskGiB: 1}, ErrInvalid},
		"unknown image": {CreateRequest{Name: "travel", ImageID: "missing", VCPUs: 1, MemoryMiB: 512, DiskGiB: 1}, ErrNotFound},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := s.Create(context.Background(), owner, "", tc.req); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if n := count(t, db, `SELECT count(*) FROM operations`); n != 0 {
		t.Fatalf("operations = %d after rejected requests", n)
	}
}

func TestInternalErrorFailsOperationInsteadOfRetrying(t *testing.T) {
	db := openDB(t)
	s, r, _ := newService(t, db, newFakeWorker())

	acc, err := s.Create(context.Background(), owner, "", travel)
	if err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if _, err := db.Exec(`INSERT INTO operations (id, owner_id, type, target_id, request_hash, params, state, created_at, updated_at)
		VALUES ('bogus-op', ?, 'environment.unknown', ?, '', '{}', 'queued', '2026-10-09T00:00:00Z', '2026-10-09T00:00:00Z')`, owner, acc.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	r.Kick()

	done := make(chan struct{})
	go func() { r.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runner kept retrying an operation that failed internally")
	}
	if op := mustOperation(t, s, "bogus-op"); op.State != "failed" || op.ErrorCode != "internal_error" {
		t.Fatalf("operation = %+v", op)
	}
}

func TestRetainedDiskCanBePurgedAfterDelete(t *testing.T) {
	db := openDB(t)
	w := newFakeWorker()
	s, r, _ := newService(t, db, w)

	acc, _ := s.Create(context.Background(), owner, "", travel)
	r.Wait()
	if _, err := s.Delete(context.Background(), owner, "", acc.EnvironmentID, false); err != nil {
		t.Fatal(err)
	}
	r.Wait()
	if _, err := s.Delete(context.Background(), owner, "", acc.EnvironmentID, false); !errors.Is(err, ErrEnvironmentDeleted) {
		t.Fatalf("second delete without purge: err = %v", err)
	}
	if _, err := s.Delete(context.Background(), owner, "", acc.EnvironmentID, true); err != nil {
		t.Fatalf("purge after delete: %v", err)
	}
	r.Wait()
	env := mustEnvironment(t, s, acc.EnvironmentID)
	if env.ObservedState != "deleted" || env.DiskRetention != "purged" || w.disks[env.DiskID] {
		t.Fatalf("environment = %+v, disks %v", env, w.disks)
	}
	if _, err := s.Delete(context.Background(), owner, "", acc.EnvironmentID, true); !errors.Is(err, ErrEnvironmentDeleted) {
		t.Fatalf("purge twice: err = %v", err)
	}
}

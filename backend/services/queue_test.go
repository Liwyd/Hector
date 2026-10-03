package services

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"hector/backend/config"
	"hector/backend/hetzner"
	"hector/backend/types"
)

// queueMock is a Hetzner that sells exactly one type — cx22 in fsn1 — plus a
// create endpoint whose behaviour each test dictates.
type queueMock struct {
	available atomic.Bool
	failCode  atomic.Value // string: when set, POST /servers answers with it
	types     atomic.Int64
	creates   atomic.Int64
}

func newQueueMock(t *testing.T) *queueMock {
	t.Helper()
	m := &queueMock{}
	m.failCode.Store("")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/server_types":
			m.types.Add(1)
			_, _ = w.Write([]byte(serverTypesJSON(m.available.Load())))
		case r.Method == http.MethodPost && r.URL.Path == "/servers":
			m.creates.Add(1)
			if code, _ := m.failCode.Load().(string); code != "" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				_, _ = w.Write([]byte(`{"error":{"code":"` + code + `","message":"nope"}}`))
				return
			}
			_, _ = w.Write([]byte(serverCreateJSON))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"no such endpoint"}}`))
		}
	}))

	prev := hcloud
	client, err := hetzner.New("test-token", "", srv.URL)
	if err != nil {
		t.Fatalf("hetzner.New: %v", err)
	}
	hcloud = client
	t.Cleanup(func() { hcloud = prev; srv.Close() })
	return m
}

func serverTypesJSON(available bool) string {
	return `{"server_types":[{` +
		`"id":1,"name":"cx22","description":"CX22","category":"shared",` +
		`"cores":2,"memory":4,"disk":40,"storage_type":"local","cpu_type":"shared",` +
		`"architecture":"x86","deprecated":false,"prices":[],` +
		`"locations":[{"id":1,"name":"fsn1","available":` + boolJSON(available) + `}]}],` +
		`"meta":{"pagination":{"page":1,"per_page":50,"next_page":null,"total_entries":1}}}`
}

const serverCreateJSON = `{
  "server": {
    "id": 101,
    "name": "queued-box",
    "status": "initializing",
    "created": "2026-09-01T12:00:00Z",
    "public_net": {"ipv4": null, "ipv6": null},
    "private_net": [],
    "server_type": {"id": 1, "name": "cx22", "cores": 2, "memory": 4, "disk": 40, "architecture": "x86"},
    "location": {"id": 1, "name": "fsn1", "city": "Falkenstein", "country": "DE", "network_zone": "eu-central"},
    "labels": {},
    "protection": {"delete": false, "rebuild": false}
  },
  "action": {"id": 7, "command": "create_server", "status": "success", "progress": 100, "started": "2026-09-01T12:00:00Z"},
  "root_password": "s3cr3t-pass"
}`

func boolJSON(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// resetQueue empties the package queue and lets it load again from disk, so
// tests cannot see each other's orders.
func resetQueue(t *testing.T, dataDir string) {
	t.Helper()
	prevCfg := config.Cfg
	config.Cfg = &config.Config{DataDir: dataDir, QueuePollSeconds: 300}

	queueMu.Lock()
	queueEntries = nil
	queueLastPoll = time.Time{}
	queueMu.Unlock()
	queueLoadOnce = sync.Once{}
	queueRunning.Store(false)

	t.Cleanup(func() {
		queueMu.Lock()
		queueEntries = nil
		queueLastPoll = time.Time{}
		queueMu.Unlock()
		queueLoadOnce = sync.Once{}
		queueRunning.Store(false)
		config.Cfg = prevCfg
	})
}

func queuedRequest() types.CreateRequest {
	return types.CreateRequest{
		Name:       "queued-box",
		ServerType: "cx22",
		Image:      "ubuntu-24.04",
		Location:   "fsn1",
		SSHKeys:    []int64{1},
	}
}

// TestQueueAddRejectsIncompleteRequest — a half-filled order must never reach
// the poller, where it would fail forever.
func TestQueueAddRejectsIncompleteRequest(t *testing.T) {
	resetQueue(t, t.TempDir())
	newQueueMock(t)

	_, err := QueueAdd(types.CreateRequest{Name: "x"})
	if err != ErrQueueInvalid {
		t.Fatalf("err = %v, want %v", err, ErrQueueInvalid)
	}
	if got := len(QueueList().Entries); got != 0 {
		t.Fatalf("queue holds %d entries, want 0", got)
	}
}

// TestQueueAddRefusesStock — ordering something that is on the shelf is a
// click on the wrong button, not an order.
func TestQueueAddRefusesStock(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)
	m.available.Store(true)

	if _, err := QueueAdd(queuedRequest()); err != ErrQueueAvailable {
		t.Fatalf("err = %v, want %v", err, ErrQueueAvailable)
	}
	if got := len(QueueList().Entries); got != 0 {
		t.Fatalf("queue holds %d entries, want 0", got)
	}
	if m.creates.Load() != 0 {
		t.Fatalf("POST /servers hit %d times, want 0", m.creates.Load())
	}
}

// TestQueueAddRefusesUnknownType keeps a typo from becoming a standing order.
func TestQueueAddRefusesUnknownType(t *testing.T) {
	resetQueue(t, t.TempDir())
	newQueueMock(t)

	req := queuedRequest()
	req.ServerType = "cx99-not-a-type"
	if _, err := QueueAdd(req); err != ErrQueueUnknownType {
		t.Fatalf("err = %v, want %v", err, ErrQueueUnknownType)
	}
}

// TestQueueAddRefusesDuplicate — one order per type and location.
func TestQueueAddRefusesDuplicate(t *testing.T) {
	resetQueue(t, t.TempDir())
	newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := QueueAdd(queuedRequest()); err != ErrQueueDuplicate {
		t.Fatalf("second add err = %v, want %v", err, ErrQueueDuplicate)
	}
	if got := len(QueueList().Entries); got != 1 {
		t.Fatalf("queue holds %d entries, want 1", got)
	}
}

// TestQueueIdleCycleCostsNoRequests is the efficiency guarantee: an empty
// queue must not spend a single Hetzner request on its timer.
func TestQueueIdleCycleCostsNoRequests(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)

	created, err := checkQueueOnce(context.Background())
	if err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	if created != 0 {
		t.Fatalf("created = %d, want 0", created)
	}
	if m.types.Load() != 0 || m.creates.Load() != 0 {
		t.Fatalf("requests = %d types / %d creates, want 0 / 0", m.types.Load(), m.creates.Load())
	}
}

// TestQueueWaitsWhileOutOfStock — the tick only reschedules; no create is
// attempted while Hetzner says the type is sold out.
func TestQueueWaitsWhileOutOfStock(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	due(t)

	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	if m.creates.Load() != 0 {
		t.Fatalf("POST /servers hit %d times, want 0", m.creates.Load())
	}

	e := only(t)
	if e.Status != queueStatusWaiting {
		t.Fatalf("status = %q, want %q", e.Status, queueStatusWaiting)
	}
	if e.Attempts != 1 {
		t.Fatalf("attempts = %d, want 1", e.Attempts)
	}
	if !e.NextCheckAt.After(time.Now()) {
		t.Fatalf("next check %v is not in the future", e.NextCheckAt)
	}
	if e.LastCheckAt.IsZero() {
		t.Fatal("the cycle must record when it last looked")
	}
}

// TestQueueBuildsWhenStockReturns is the whole feature in one test: the type
// was sold out, stock arrives, the confirmed payload becomes a server and the
// entry leaves the queue's polling.
func TestQueueBuildsWhenStockReturns(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	m.available.Store(true)
	due(t)

	created, err := checkQueueOnce(context.Background())
	if err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	if created != 1 {
		t.Fatalf("created = %d, want 1", created)
	}
	if m.creates.Load() != 1 {
		t.Fatalf("POST /servers hit %d times, want 1", m.creates.Load())
	}

	e := only(t)
	if e.Status != queueStatusDone {
		t.Fatalf("status = %q, want %q (last error %q)", e.Status, queueStatusDone, e.LastError)
	}
	if e.ServerID != 101 {
		t.Fatalf("server id = %d, want 101", e.ServerID)
	}
	if e.RootPassword != "s3cr3t-pass" {
		t.Fatalf("root password = %q, want the one Hetzner returned", e.RootPassword)
	}
	if !e.NextCheckAt.IsZero() {
		t.Fatalf("a finished order must stop being polled, next check = %v", e.NextCheckAt)
	}
	// a second cycle must be free: nothing is waiting any more
	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("second checkQueueOnce: %v", err)
	}
	if m.types.Load() != 2 {
		t.Fatalf("server_types read %d times, want 2 (one per due cycle)", m.types.Load())
	}
}

// TestQueueAddAcceptsAnotherOrderAfterBuilt — a built order stays in the log,
// but it must not hold the type and location: the next order needs the queue
// as soon as the type sells out again.
func TestQueueAddAcceptsAnotherOrderAfterBuilt(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	m.available.Store(true)
	due(t)
	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	if only(t).Status != queueStatusDone {
		t.Fatalf("first order was not built")
	}

	m.available.Store(false)
	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("add after a built order: %v", err)
	}
	if got := len(QueueList().Entries); got != 2 {
		t.Fatalf("queue holds %d entries, want 2 (the log and the new order)", got)
	}
	// the new entry is the one being polled
	due(t)
	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	if m.creates.Load() != 1 {
		t.Fatalf("POST /servers hit %d times, want 1 (still out of stock)", m.creates.Load())
	}
}

// TestQueueFullIgnoresFinishedOrders — the cap is on standing orders, so a
// queue of built ones can never be the reason the next order is refused.
func TestQueueFullIgnoresFinishedOrders(t *testing.T) {
	resetQueue(t, t.TempDir())
	newQueueMock(t)

	queueMu.Lock()
	for i := 0; i < queueMaxEntries; i++ {
		queueEntries = append(queueEntries, types.QueueEntry{
			ID:      newQueueID(),
			Status:  queueStatusDone,
			Request: queuedRequest(),
		})
	}
	queueMu.Unlock()

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("add to a queue of finished orders: %v", err)
	}
	if got := len(QueueList().Entries); got != queueMaxEntries+1 {
		t.Fatalf("queue holds %d entries, want %d", got, queueMaxEntries+1)
	}
}

// TestQueueFailsPermanently — waiting cannot fix a rejected payment, so the
// order stops instead of polling until the end of time.
func TestQueueFailsPermanently(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)
	m.failCode.Store("insufficient_funds")

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	m.available.Store(true)
	due(t)

	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	e := only(t)
	if e.Status != queueStatusFailed {
		t.Fatalf("status = %q, want %q", e.Status, queueStatusFailed)
	}
	if !strings.Contains(e.LastError, "insufficient_funds") {
		t.Fatalf("last error = %q, want the Hetzner code", e.LastError)
	}

	// the next cycle must not touch Hetzner for a dead order
	before := m.types.Load()
	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("second checkQueueOnce: %v", err)
	}
	if m.types.Load() != before || m.creates.Load() != 1 {
		t.Fatalf("cycle still polled (%d types / %d creates)", m.types.Load(), m.creates.Load())
	}
}

// TestQueueRetriesResourceUnavailable — the race between "available" and
// "create" is normal; it must not kill the order.
func TestQueueRetriesResourceUnavailable(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)
	m.failCode.Store("resource_unavailable")

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	m.available.Store(true)
	due(t)

	if _, err := checkQueueOnce(context.Background()); err != nil {
		t.Fatalf("checkQueueOnce: %v", err)
	}
	e := only(t)
	if e.Status != queueStatusWaiting {
		t.Fatalf("status = %q, want %q", e.Status, queueStatusWaiting)
	}
	if !e.NextCheckAt.After(time.Now()) {
		t.Fatalf("next check = %v, want a future retry", e.NextCheckAt)
	}
	if e.LastError == "" {
		t.Fatal("the failed create must be reported to the user")
	}
}

// TestQueuePersistsAcrossRestart is why the file exists at all.
func TestQueuePersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	resetQueue(t, dir)
	newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}

	// simulate the process going away and coming back
	queueMu.Lock()
	queueEntries = nil
	queueLoadOnce = sync.Once{}
	queueMu.Unlock()

	if got := len(QueueList().Entries); got != 1 {
		t.Fatalf("queue holds %d entries after reload, want 1", got)
	}
	info, err := os.Stat(filepath.Join(dir, "queue.json"))
	if err != nil {
		t.Fatalf("queue file: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("queue file mode = %o, want it private", perm)
	}
}

// TestQueueRecoversInterruptedOrder — a restart in the middle of a create may
// or may not have bought a server, so the entry stops rather than risk a
// second one.
func TestQueueRecoversInterruptedOrder(t *testing.T) {
	dir := t.TempDir()
	id := filepath.Join(dir, "queue.json")
	disk := persistedQueue{Entries: []types.QueueEntry{{
		ID:      "abc123",
		Status:  queueStatusCreating,
		Request: types.CreateRequest{Name: "x", ServerType: "cx22", Image: "ubuntu-24.04", Location: "fsn1"},
	}}}
	raw, err := json.Marshal(disk)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(id, raw, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	resetQueue(t, dir)
	e := only(t)
	if e.Status != queueStatusFailed {
		t.Fatalf("status = %q, want %q", e.Status, queueStatusFailed)
	}
	if !strings.Contains(e.LastError, "restart") {
		t.Fatalf("last error = %q, want it to mention the restart", e.LastError)
	}
}

// TestQueueRemove — leaving the queue is a first-class action.
func TestQueueRemove(t *testing.T) {
	resetQueue(t, t.TempDir())
	newQueueMock(t)

	entry, err := QueueAdd(queuedRequest())
	if err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	if err := QueueRemove(entry.ID); err != nil {
		t.Fatalf("QueueRemove: %v", err)
	}
	if got := len(QueueList().Entries); got != 0 {
		t.Fatalf("queue holds %d entries, want 0", got)
	}
	if err := QueueRemove(entry.ID); err != ErrQueueNotFound {
		t.Fatalf("second remove err = %v, want %v", err, ErrQueueNotFound)
	}
}

// TestQueueCheckNowIgnoresTheSchedule — the button the user actually presses.
func TestQueueCheckNowIgnoresTheSchedule(t *testing.T) {
	resetQueue(t, t.TempDir())
	m := newQueueMock(t)

	if _, err := QueueAdd(queuedRequest()); err != nil {
		t.Fatalf("QueueAdd: %v", err)
	}
	if !only(t).NextCheckAt.After(time.Now()) {
		t.Fatal("a fresh order should not be due yet")
	}

	m.available.Store(true)
	if err := QueueCheckNow(context.Background()); err != nil {
		t.Fatalf("QueueCheckNow: %v", err)
	}
	if m.creates.Load() != 1 {
		t.Fatalf("POST /servers hit %d times, want 1", m.creates.Load())
	}
	if only(t).Status != queueStatusDone {
		t.Fatalf("status = %q, want %q", only(t).Status, queueStatusDone)
	}
}

// ---- helpers -------------------------------------------------------------

// due makes every waiting order eligible for the next cycle.
func due(t *testing.T) {
	t.Helper()
	queueMu.Lock()
	defer queueMu.Unlock()
	for i := range queueEntries {
		queueEntries[i].NextCheckAt = time.Time{}
	}
}

func only(t *testing.T) types.QueueEntry {
	t.Helper()
	entries := QueueList().Entries
	if len(entries) != 1 {
		t.Fatalf("queue holds %d entries, want exactly 1", len(entries))
	}
	return entries[0]
}

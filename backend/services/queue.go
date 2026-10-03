package services

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"hector/backend/config"
	"hector/backend/hetzner"
	"hector/backend/types"
)

// The order queue holds out-of-stock server types until Hetzner sells them
// again, then builds them with exactly the payload the user confirmed. Stock
// is re-read from server_types — never from the catalog, which is cached for
// ten minutes and would hand out stale availability for the whole morning.

const (
	queueStatusWaiting  = "waiting"
	queueStatusCreating = "creating"
	queueStatusDone     = "done"
	queueStatusFailed   = "failed"

	// A queue is a shopping list, not a job queue: beyond this many orders
	// there is no realistic chance the poller serves them all in one run.
	queueMaxEntries = 25
)

var (
	ErrQueueInvalid     = errors.New("name, type, image and location are required")
	ErrQueueAvailable   = errors.New("that server type is in stock right now")
	ErrQueueDuplicate   = errors.New("that order is already queued")
	ErrQueueFull        = errors.New("the order queue is full")
	ErrQueueNotFound    = errors.New("no such queued order")
	ErrQueueUnknownType = errors.New("no such server type")
)

// permanentQueueErrors never resolve by waiting. Everything else — most
// importantly resource_unavailable and any transport failure — is retried on
// the next tick.
var permanentQueueErrors = map[string]bool{
	"invalid_server_type": true,
	"invalid_image":       true,
	"invalid_location":    true,
	"insufficient_funds":  true,
	"limit_reached":       true,
	"quota_exceeded":      true,
	"uniqueness_error":    true,
	"forbidden":           true,
	"conflict":            true,
}

var (
	queueMu       sync.Mutex
	queueEntries  []types.QueueEntry
	queueLastPoll time.Time
	queueLoadOnce sync.Once
	// queueRunning makes overlapping checks impossible: a manual "check now"
	// during a ticker run must never order the same entry twice.
	queueRunning atomic.Bool
)

// queueFile is the one file the panel writes. config.Cfg can be missing in
// unit tests, and then the queue simply lives in memory for that process.
func queueFile() string {
	if config.Cfg == nil || config.Cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(config.Cfg.DataDir, "queue.json")
}

func queueInterval() time.Duration {
	secs := 300
	if config.Cfg != nil && config.Cfg.QueuePollSeconds > 0 {
		secs = config.Cfg.QueuePollSeconds
	}
	return time.Duration(secs) * time.Second
}

// persisted is what lands on disk. RootPassword is deliberately inside it:
// Hetzner shows it exactly once, so a restart must not be allowed to eat it.
type persistedQueue struct {
	Entries    []types.QueueEntry `json:"entries"`
	LastPollAt time.Time          `json:"lastPollAt,omitempty"`
}

func loadQueue() {
	path := queueFile()
	if path == "" {
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("[queue] cannot read %s: %v", path, err)
		}
		return
	}
	var disk persistedQueue
	if err := json.Unmarshal(raw, &disk); err != nil {
		log.Printf("[queue] %s is unreadable, starting empty: %v", path, err)
		return
	}

	for i := range disk.Entries {
		e := &disk.Entries[i]
		if e.Status == queueStatusCreating {
			// The process died between "order it" and "it was ordered". The
			// create may well have reached Hetzner, so ordering again could
			// buy a second server — surface the doubt instead.
			e.Status = queueStatusFailed
			e.LastError = "interrupted by a restart — check the server list before ordering again"
		}
	}
	queueEntries = disk.Entries
	queueLastPoll = disk.LastPollAt
}

func saveQueue() {
	path := queueFile()
	if path == "" {
		return
	}
	raw, err := json.MarshalIndent(persistedQueue{Entries: queueEntries, LastPollAt: queueLastPoll}, "", "  ")
	if err != nil {
		log.Printf("[queue] cannot encode: %v", err)
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("[queue] cannot create %s: %v", filepath.Dir(path), err)
		return
	}
	// write-then-rename: a crash mid-write must not leave a half file behind
	tmp, err := os.CreateTemp(filepath.Dir(path), "queue-*.tmp")
	if err != nil {
		log.Printf("[queue] cannot write: %v", err)
		return
	}
	if err := os.WriteFile(tmp.Name(), raw, 0o600); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		log.Printf("[queue] cannot write: %v", err)
		return
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), path); err != nil {
		os.Remove(tmp.Name())
		log.Printf("[queue] cannot replace %s: %v", path, err)
	}
}

func loadQueueOnce() { queueLoadOnce.Do(loadQueue) }

// QueueList is the read model the panel polls, in the order the orders were
// taken. Entries is never null, whatever the disk held.
func QueueList() types.QueueView {
	loadQueueOnce()
	queueMu.Lock()
	defer queueMu.Unlock()

	out := make([]types.QueueEntry, len(queueEntries))
	copy(out, queueEntries)
	secs := int(queueInterval() / time.Second)
	return types.QueueView{Entries: out, IntervalSeconds: secs, LastPollAt: queueLastPoll}
}

// queueActive reports whether an entry still occupies a place in the queue:
// it is waiting to be built, or its create request is on the way. A built or
// failed order is history — the log the panel shows, never a standing order.
func queueActive(status string) bool {
	return status == queueStatusWaiting || status == queueStatusCreating
}

// QueueAdd parks one out-of-stock order. Stock is read here so the caller
// gets an answer instead of a queue entry that resolves a second later.
func QueueAdd(req types.CreateRequest) (types.QueueEntry, error) {
	loadQueueOnce()
	if req.Name == "" || req.ServerType == "" || req.Image == "" || req.Location == "" {
		return types.QueueEntry{}, ErrQueueInvalid
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	inStock, known, err := queueStock(ctx, req.ServerType, req.Location)
	switch {
	case err != nil:
		// A stock read that fails must not block the order: the poller will
		// establish availability on its own schedule.
		log.Printf("[queue] stock check for %s in %s failed: %v", req.ServerType, req.Location, err)
	case !known:
		return types.QueueEntry{}, ErrQueueUnknownType
	case inStock:
		return types.QueueEntry{}, ErrQueueAvailable
	}

	now := time.Now()
	queueMu.Lock()
	defer queueMu.Unlock()

	// One standing order per type and location: while an entry is still being
	// polled, a second one would only race it. Built and failed entries are
	// left in the log, but they hold nothing — otherwise the first success
	// would lock that type out of the queue forever.
	active := 0
	for _, e := range queueEntries {
		if !queueActive(e.Status) {
			continue
		}
		active++
		if e.Request.ServerType == req.ServerType && e.Request.Location == req.Location {
			return types.QueueEntry{}, ErrQueueDuplicate
		}
	}
	if active >= queueMaxEntries {
		return types.QueueEntry{}, ErrQueueFull
	}

	entry := types.QueueEntry{
		ID:          newQueueID(),
		CreatedAt:   now,
		Status:      queueStatusWaiting,
		Request:     req,
		NextCheckAt: now.Add(queueInterval()),
	}
	queueEntries = append(queueEntries, entry)
	saveQueue()
	return entry, nil
}

// QueueRemove drops an order, whether it is still waiting or already built.
func QueueRemove(id string) error {
	loadQueueOnce()
	queueMu.Lock()
	defer queueMu.Unlock()

	for i, e := range queueEntries {
		if e.ID != id {
			continue
		}
		queueEntries = append(queueEntries[:i], queueEntries[i+1:]...)
		saveQueue()
		return nil
	}
	return ErrQueueNotFound
}

// QueueForgetSecret wipes a root password that has been written down, so the
// file stops carrying it.
func QueueForgetSecret(id string) error {
	loadQueueOnce()
	queueMu.Lock()
	defer queueMu.Unlock()

	for i, e := range queueEntries {
		if e.ID != id {
			continue
		}
		e.RootPassword = ""
		queueEntries[i] = e
		saveQueue()
		return nil
	}
	return ErrQueueNotFound
}

// QueueCheckNow answers "is it in stock yet?" without waiting for the tick.
func QueueCheckNow(ctx context.Context) error {
	loadQueueOnce()
	queueMu.Lock()
	for i := range queueEntries {
		if queueEntries[i].Status == queueStatusWaiting {
			queueEntries[i].NextCheckAt = time.Time{}
		}
	}
	queueMu.Unlock()
	_, err := checkQueueOnce(ctx)
	return err
}

// queueStock is the single stock question: one server_types request.
func queueStock(ctx context.Context, typeName, location string) (inStock, known bool, err error) {
	typesRaw, err := hcloud.ServerTypes(ctx)
	if err != nil {
		return false, false, err
	}
	avail := newAvailabilityIndex(ctx, typesRaw)
	for _, st := range typesRaw {
		if st.Name == typeName {
			return avail.isAvailable(st, location), true, nil
		}
	}
	return false, false, nil
}

// checkQueueOnce runs one stock cycle. It returns without touching Hetzner
// when nothing is due, so an idle panel costs nothing.
func checkQueueOnce(ctx context.Context) (created int, err error) {
	if !queueRunning.CompareAndSwap(false, true) {
		return 0, nil // a cycle is already running
	}
	defer queueRunning.Store(false)

	now := time.Now()
	queueMu.Lock()
	due := make([]types.QueueEntry, 0, len(queueEntries))
	for _, e := range queueEntries {
		if e.Status == queueStatusWaiting && !e.NextCheckAt.After(now) {
			due = append(due, e)
		}
	}
	queueMu.Unlock()
	if len(due) == 0 {
		return 0, nil
	}

	typesRaw, err := hcloud.ServerTypes(ctx)
	if err != nil {
		return 0, err
	}
	avail := newAvailabilityIndex(ctx, typesRaw)
	byName := make(map[string]hetzner.ServerType, len(typesRaw))
	for _, st := range typesRaw {
		byName[st.Name] = st
	}

	queueMu.Lock()
	queueLastPoll = time.Now()
	queueMu.Unlock()

	interval := queueInterval()
	for _, e := range due {
		st, known := byName[e.Request.ServerType]
		switch {
		case !known:
			queueFail(e.ID, "server type no longer exists")
		case !avail.isAvailable(st, e.Request.Location):
			queueWait(e.ID, time.Now().Add(interval), "")
		default:
			// Commit the intent before the order leaves the process: if the
			// panel dies during the create, the restart must see an
			// interrupted entry, never a waiting one that buys twice.
			if !queueMarkCreating(e.ID) {
				continue // cancelled while this cycle was running
			}
			res, createErr := ServerCreate(ctx, e.Request)
			if createErr != nil {
				if queueRetryable(createErr) {
					queueWait(e.ID, time.Now().Add(interval), createErr.Error())
				} else {
					queueFail(e.ID, createErr.Error())
				}
				continue
			}
			queueDone(e.ID, res)
			// availability just moved — the New server screen must see it
			invalidateCatalog()
			created++
		}
	}

	queueMu.Lock()
	saveQueue()
	queueMu.Unlock()
	if created > 0 {
		log.Printf("[queue] %d order(s) became servers", created)
	}
	return created, nil
}

// queueMarkCreating moves an order to the state where the create request is
// on its way, and writes it out at once. It reports false when the entry is
// no longer in the queue (cancelled during this cycle).
func queueMarkCreating(id string) bool {
	queueMu.Lock()
	defer queueMu.Unlock()
	for i, e := range queueEntries {
		if e.ID != id {
			continue
		}
		e.Status = queueStatusCreating
		e.NextCheckAt = time.Time{}
		queueEntries[i] = e
		saveQueue()
		return true
	}
	return false
}

// queueWait records a cycle that ended without a server: stock is missing,
// or the create could not go through yet. lastError is what the panel shows
// for that cycle — empty when the only news is "still out of stock".
func queueWait(id string, next time.Time, lastError string) {
	queueMu.Lock()
	defer queueMu.Unlock()
	for i, e := range queueEntries {
		if e.ID == id {
			e.Status = queueStatusWaiting
			e.Attempts++
			e.LastCheckAt = time.Now()
			e.NextCheckAt = next
			e.LastError = lastError
			queueEntries[i] = e
			return
		}
	}
}

func queueFail(id, message string) {
	queueMu.Lock()
	defer queueMu.Unlock()
	for i, e := range queueEntries {
		if e.ID == id {
			e.Status = queueStatusFailed
			e.LastError = message
			e.LastCheckAt = time.Now()
			e.NextCheckAt = time.Time{}
			queueEntries[i] = e
			return
		}
	}
}

func queueDone(id string, res *types.CreateResult) {
	queueMu.Lock()
	defer queueMu.Unlock()
	for i, e := range queueEntries {
		if e.ID == id {
			e.Status = queueStatusDone
			e.Attempts++
			e.LastCheckAt = time.Now()
			e.NextCheckAt = time.Time{}
			e.LastError = ""
			e.ServerID = res.Server.ID
			e.ServerName = res.Server.Name
			e.RootPassword = res.RootPassword
			queueEntries[i] = e
			return
		}
	}
}

func queueRetryable(err error) bool {
	var apiErr *hetzner.APIError
	if errors.As(err, &apiErr) {
		return !permanentQueueErrors[apiErr.Code]
	}
	var urlErr *url.Error
	var netErr net.Error
	return errors.As(err, &urlErr) || errors.As(err, &netErr)
}

func newQueueID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// StartQueueWorker begins the stock poll. main calls it once, after
// services.Init — never from Init itself, so tests get no ticker they did
// not ask for. A cycle costs one Hetzner request at most, and none at all
// while no order is due, so an empty queue never spends the token quota.
func StartQueueWorker() {
	loadQueueOnce()

	interval := queueInterval()
	pending := 0
	for _, e := range QueueList().Entries {
		if e.Status == queueStatusWaiting {
			pending++
		}
	}
	log.Printf("[queue] stock poll every %s · %d order(s) waiting", interval, pending)

	go func() {
		// orders restored from disk are already due
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if _, err := checkQueueOnce(ctx); err != nil {
			log.Printf("[queue] stock check failed: %v", err)
		}
		cancel()

		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			c, stop := context.WithTimeout(context.Background(), 2*time.Minute)
			if _, err := checkQueueOnce(c); err != nil {
				log.Printf("[queue] stock check failed: %v", err)
			}
			stop()
		}
	}()
}

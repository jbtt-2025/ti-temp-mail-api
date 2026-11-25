package main

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countingStore struct {
	mu             sync.Mutex
	mailboxes      map[string]mailboxRec
	byToken        map[string]string
	emails         map[string]*Email
	mailboxTTL     time.Duration
	emailTTL       time.Duration
	nowFn          func() time.Time
	beforeMissHook func(op string)

	createCalls  atomic.Int64
	existsCalls  atomic.Int64
	byTokenCalls atomic.Int64
	saveCalls    atomic.Int64
	listCalls    atomic.Int64
	getCalls     atomic.Int64
	cleanupCalls atomic.Int64
}

type mailboxRec struct {
	token     string
	createdAt int64
}

func newCountingStore(mailboxTTL, emailTTL time.Duration, nowFn func() time.Time) *countingStore {
	return &countingStore{
		mailboxes:  make(map[string]mailboxRec),
		byToken:    make(map[string]string),
		emails:     make(map[string]*Email),
		mailboxTTL: mailboxTTL,
		emailTTL:   emailTTL,
		nowFn:      nowFn,
	}
}

func (s *countingStore) hook(op string) {
	if s.beforeMissHook != nil {
		s.beforeMissHook(op)
	}
}

func (s *countingStore) CreateMailbox(ctx context.Context, token, mailbox string) (bool, error) {
	ok, _, err := s.CreateMailboxWithMeta(ctx, token, mailbox)
	return ok, err
}

func (s *countingStore) CreateMailboxWithMeta(_ context.Context, token, mailbox string) (bool, int64, error) {
	s.createCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowFn()
	if existing, ok := s.mailboxes[mailbox]; ok {
		if cutoff, hasTTL := cutoffUnix(now, s.mailboxTTL); !hasTTL || existing.createdAt > cutoff {
			return false, 0, nil
		}
		delete(s.byToken, existing.token)
	}
	createdAt := now.Unix()
	s.mailboxes[mailbox] = mailboxRec{token: token, createdAt: createdAt}
	s.byToken[token] = mailbox
	return true, createdAt, nil
}

func (s *countingStore) MailboxExists(ctx context.Context, mailbox string) (bool, error) {
	ok, _, err := s.MailboxExistsWithMeta(ctx, mailbox)
	return ok, err
}

func (s *countingStore) MailboxExistsWithMeta(_ context.Context, mailbox string) (bool, int64, error) {
	s.existsCalls.Add(1)
	s.hook("exists")
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.mailboxes[mailbox]
	if !ok {
		return false, 0, nil
	}
	if cutoff, hasTTL := cutoffUnix(s.nowFn(), s.mailboxTTL); hasTTL && rec.createdAt <= cutoff {
		return false, 0, nil
	}
	return true, rec.createdAt, nil
}

func (s *countingStore) GetMailboxByToken(ctx context.Context, token string) (string, bool, error) {
	mbox, _, ok, err := s.GetMailboxByTokenWithMeta(ctx, token)
	return mbox, ok, err
}

func (s *countingStore) GetMailboxByTokenWithMeta(_ context.Context, token string) (string, int64, bool, error) {
	s.byTokenCalls.Add(1)
	s.hook("token")
	s.mu.Lock()
	defer s.mu.Unlock()
	addr, ok := s.byToken[token]
	if !ok {
		return "", 0, false, nil
	}
	rec := s.mailboxes[addr]
	if cutoff, hasTTL := cutoffUnix(s.nowFn(), s.mailboxTTL); hasTTL && rec.createdAt <= cutoff {
		return "", 0, false, nil
	}
	return addr, rec.createdAt, true, nil
}

func (s *countingStore) SaveEmail(_ context.Context, email *Email) (bool, error) {
	s.saveCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	copyEmail := *email
	s.emails[email.ID] = &copyEmail
	return true, nil
}

func (s *countingStore) ListEmailsByMailbox(_ context.Context, mailbox string) ([]*Email, error) {
	s.listCalls.Add(1)
	s.hook("list")
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff, hasTTL := cutoffUnix(s.nowFn(), s.emailTTL)
	var out []*Email
	for _, e := range s.emails {
		if e.Mailbox != mailbox {
			continue
		}
		if hasTTL && e.ReceivedAt <= cutoff {
			continue
		}
		copyEmail := *e
		out = append(out, &copyEmail)
	}
	return out, nil
}

func (s *countingStore) GetEmailByID(_ context.Context, id string) (*Email, bool, error) {
	s.getCalls.Add(1)
	s.hook("email")
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.emails[id]
	if !ok {
		return nil, false, nil
	}
	if cutoff, hasTTL := cutoffUnix(s.nowFn(), s.emailTTL); hasTTL && e.ReceivedAt <= cutoff {
		return nil, false, nil
	}
	copyEmail := *e
	return &copyEmail, true, nil
}

func (s *countingStore) CleanupExpired(ctx context.Context, now time.Time) (int64, int64, error) {
	res, err := s.CleanupExpiredWithHint(ctx, now)
	return res.DeletedEmails, res.DeletedMailboxes, err
}

func (s *countingStore) CleanupExpiredWithHint(_ context.Context, now time.Time) (cleanupResult, error) {
	s.cleanupCalls.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()

	var out cleanupResult
	if cutoff, ok := cutoffUnix(now, s.emailTTL); ok {
		var minTs *int64
		for id, e := range s.emails {
			if e.ReceivedAt <= cutoff {
				delete(s.emails, id)
				out.DeletedEmails++
			} else if minTs == nil || e.ReceivedAt < *minTs {
				v := e.ReceivedAt
				minTs = &v
			}
		}
		out.HasEmailHint = true
		out.EarliestEmailAt = minTs
	}
	if cutoff, ok := cutoffUnix(now, s.mailboxTTL); ok {
		var minTs *int64
		for addr, rec := range s.mailboxes {
			if rec.createdAt <= cutoff {
				delete(s.byToken, rec.token)
				delete(s.mailboxes, addr)
				out.DeletedMailboxes++
			} else if minTs == nil || rec.createdAt < *minTs {
				v := rec.createdAt
				minTs = &v
			}
		}
		out.HasMailboxHint = true
		out.EarliestMailboxAt = minTs
	}
	return out, nil
}

func (s *countingStore) Close() error { return nil }

func TestCachedStore_HitsAndWriteThrough(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowFn := func() time.Time { return now }
	backend := newCountingStore(time.Hour, time.Hour, nowFn)
	cfg := &Config{
		MailboxTTL:          time.Hour,
		EmailTTL:            time.Hour,
		CacheEnabled:        true,
		CacheMaxMailboxes:   100,
		CacheMaxEmailMemory: 1024 * 1024,
		CacheNegativeTTL:    time.Minute,
	}
	cs := NewCachedStore(backend, cfg)
	cs.nowFn = nowFn
	ctx := context.Background()

	// Create mailbox warms both addr and token caches.
	created, err := cs.CreateMailbox(ctx, "tok-1", "u@example.com")
	if err != nil || !created {
		t.Fatalf("CreateMailbox created=%v err=%v", created, err)
	}
	for i := 0; i < 5; i++ {
		if ok, err := cs.MailboxExists(ctx, "u@example.com"); err != nil || !ok {
			t.Fatalf("MailboxExists failed: ok=%v err=%v", ok, err)
		}
		if addr, ok, err := cs.GetMailboxByToken(ctx, "tok-1"); err != nil || !ok || addr != "u@example.com" {
			t.Fatalf("GetMailboxByToken failed: addr=%q ok=%v err=%v", addr, ok, err)
		}
	}
	if backend.existsCalls.Load() != 0 || backend.byTokenCalls.Load() != 0 {
		t.Fatalf("expected 0 backend reads after CreateMailbox, got exists=%d token=%d", backend.existsCalls.Load(), backend.byTokenCalls.Load())
	}

	// First ListEmailsByMailbox hits backend once; subsequent idle polls hit cache.
	for i := 0; i < 5; i++ {
		list, err := cs.ListEmailsByMailbox(ctx, "u@example.com")
		if err != nil || len(list) != 0 {
			t.Fatalf("unexpected empty list: %+v err=%v", list, err)
		}
	}
	if backend.listCalls.Load() != 1 {
		t.Fatalf("expected 1 backend List call, got %d", backend.listCalls.Load())
	}

	// SaveEmail writes through to cache and populates both inbox list and detail cache.
	e1 := &Email{ID: "m1", Mailbox: "u@example.com", ReceivedAt: now.Unix(), Subject: "s1", BodyHTML: "<p>1</p>"}
	e2 := &Email{ID: "m2", Mailbox: "u@example.com", ReceivedAt: now.Add(time.Second).Unix(), Subject: "s2", BodyHTML: "<p>2</p>"}
	_, _ = cs.SaveEmail(ctx, e1)
	_, _ = cs.SaveEmail(ctx, e2)

	list, err := cs.ListEmailsByMailbox(ctx, "u@example.com")
	if err != nil || len(list) != 2 || list[0].ID != "m2" || list[1].ID != "m1" {
		t.Fatalf("expected [m2, m1] from write-through cache, got %+v err=%v", list, err)
	}
	if backend.listCalls.Load() != 1 {
		t.Fatalf("expected ListEmailsByMailbox to hit cache after SaveEmail, got %d backend calls", backend.listCalls.Load())
	}

	got, ok, err := cs.GetEmailByID(ctx, "m2")
	if err != nil || !ok || got.BodyHTML != "<p>2</p>" {
		t.Fatalf("GetEmailByID got=%+v ok=%v err=%v", got, ok, err)
	}
	if backend.getCalls.Load() != 0 {
		t.Fatalf("expected 0 backend GetEmailByID calls, got %d", backend.getCalls.Load())
	}
}

func TestCachedStore_NegativeCacheInvalidatedOnCreate(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowFn := func() time.Time { return now }
	backend := newCountingStore(time.Hour, time.Hour, nowFn)
	cs := NewCachedStore(backend, &Config{
		MailboxTTL:          time.Hour,
		EmailTTL:            time.Hour,
		CacheMaxMailboxes:   100,
		CacheMaxEmailMemory: 1024 * 1024,
		CacheNegativeTTL:    30 * time.Second,
	})
	cs.nowFn = nowFn
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if ok, err := cs.MailboxExists(ctx, "scan@example.com"); err != nil || ok {
			t.Fatalf("expected missing mailbox false, got %v err=%v", ok, err)
		}
		if _, ok, err := cs.GetMailboxByToken(ctx, "invalid-tok"); err != nil || ok {
			t.Fatalf("expected invalid token false, got %v err=%v", ok, err)
		}
	}
	if backend.existsCalls.Load() != 1 || backend.byTokenCalls.Load() != 1 {
		t.Fatalf("expected negative cache to limit misses to 1, got exists=%d token=%d", backend.existsCalls.Load(), backend.byTokenCalls.Load())
	}

	// Allocating the scanned address immediately invalidates negAddr.
	if created, err := cs.CreateMailbox(ctx, "tok-scan", "scan@example.com"); err != nil || !created {
		t.Fatalf("CreateMailbox failed: %v %v", created, err)
	}
	if ok, err := cs.MailboxExists(ctx, "scan@example.com"); err != nil || !ok {
		t.Fatalf("expected newly created mailbox to bypass stale negative cache, ok=%v err=%v", ok, err)
	}

	// Advancing past negativeTTL lets unallocated addresses re-check backend once.
	_, _ = cs.MailboxExists(ctx, "other@example.com")
	now = now.Add(35 * time.Second)
	_, _ = cs.MailboxExists(ctx, "other@example.com")
	if backend.existsCalls.Load() != 3 {
		t.Fatalf("expected 3 total existsCalls (1 scan + 2 other), got %d", backend.existsCalls.Load())
	}
}

func TestCachedStore_EmailMemoryCapAndDynamicExpiration(t *testing.T) {
	now := time.Unix(1700000000, 0)
	nowFn := func() time.Time { return now }
	backend := newCountingStore(10*time.Second, 20*time.Second, nowFn)
	cs := NewCachedStore(backend, &Config{
		MailboxTTL:          10 * time.Second,
		EmailTTL:            20 * time.Second,
		CacheMaxMailboxes:   100,
		CacheMaxEmailMemory: 1200, // ~2 small emails fit comfortably, 3rd forces eviction
		CacheNegativeTTL:    time.Minute,
	})
	cs.nowFn = nowFn
	ctx := context.Background()

	_, _ = cs.CreateMailbox(ctx, "tok", "u@example.com")
	_, _ = cs.ListEmailsByMailbox(ctx, "u@example.com") // hydrate empty inbox
	payload := "<p>" + strings.Repeat("a", 300) + "</p>"
	for i := 1; i <= 3; i++ {
		_, _ = cs.SaveEmail(ctx, &Email{
			ID:         string(rune('0' + i)),
			Mailbox:    "u@example.com",
			ReceivedAt: now.Unix() + int64(i),
			BodyHTML:   payload,
		})
	}
	if cs.emailBytes > 1200 {
		t.Fatalf("emailBytes=%d exceeded CacheMaxEmailMemory=1200", cs.emailBytes)
	}

	// Eviction invalidated hydrated list, so ListEmailsByMailbox falls back cleanly to DB and returns all 3 emails.
	all, err := cs.ListEmailsByMailbox(ctx, "u@example.com")
	if err != nil || len(all) != 3 {
		t.Fatalf("expected all 3 emails after DB fallback, got %d err=%v", len(all), err)
	}

	// Dynamic expiration rejects expired mailbox locally without hitting DB.
	now = now.Add(15 * time.Second)
	prevExists := backend.existsCalls.Load()
	if ok, err := cs.MailboxExists(ctx, "u@example.com"); err != nil || ok {
		t.Fatalf("expected expired mailbox false, got %v err=%v", ok, err)
	}
	if _, ok, err := cs.GetMailboxByToken(ctx, "tok"); err != nil || ok {
		t.Fatalf("expected expired token false, got %v err=%v", ok, err)
	}
	if backend.existsCalls.Load() != prevExists {
		t.Fatalf("expected expired mailbox check to return locally without backend hit")
	}

	// Expired emails return empty/not-found without hitting DB when cached.
	now = now.Add(20 * time.Second)
	if _, ok, err := cs.GetEmailByID(ctx, "3"); err != nil || ok {
		t.Fatalf("expected expired email false, got %v err=%v", ok, err)
	}
}

func TestCachedStore_SingleflightAndSmartCleanupSkip(t *testing.T) {
	now := time.Unix(1700000000, 0)
	var clockMu sync.Mutex
	nowFn := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	backend := newCountingStore(10*time.Second, 20*time.Second, nowFn)
	gate := make(chan struct{})
	var gateOnce sync.Once
	backend.beforeMissHook = func(op string) {
		if op == "token" {
			<-gate
		}
	}
	_, _, _ = backend.CreateMailboxWithMeta(context.Background(), "cold-tok", "cold@example.com")

	cs := NewCachedStore(backend, &Config{
		MailboxTTL:          10 * time.Second,
		EmailTTL:            20 * time.Second,
		CacheMaxMailboxes:   100,
		CacheMaxEmailMemory: 1024 * 1024,
		CacheNegativeTTL:    time.Minute,
	})
	cs.nowFn = nowFn
	ctx := context.Background()

	// Coalesce 10 concurrent cold GetMailboxByToken calls into 1 backend query.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mbox, ok, err := cs.GetMailboxByToken(ctx, "cold-tok")
			if err != nil || !ok || mbox != "cold@example.com" {
				t.Errorf("concurrent GetMailboxByToken=%q ok=%v err=%v", mbox, ok, err)
			}
		}()
	}
	time.Sleep(25 * time.Millisecond)
	gateOnce.Do(func() { close(gate) })
	wg.Wait()

	if backend.byTokenCalls.Load() != 1 {
		t.Fatalf("expected singleflight to coalesce 10 cold lookups into 1 backend call, got %d", backend.byTokenCalls.Load())
	}

	// Smart cleanup skip:
	// 1st CleanupExpired runs backend and learns earliestMailboxAt = t0 (expires t0+10s).
	if _, _, err := cs.CleanupExpired(ctx, now); err != nil {
		t.Fatalf("CleanupExpired #1 failed: %v", err)
	}
	if backend.cleanupCalls.Load() != 1 {
		t.Fatalf("expected 1 cleanup call, got %d", backend.cleanupCalls.Load())
	}

	// Before t0+10s, CleanupExpired skips backend entirely.
	clockMu.Lock()
	now = now.Add(5 * time.Second)
	tCheck := now
	clockMu.Unlock()
	_, _, _ = cs.CleanupExpired(ctx, tCheck)
	if backend.cleanupCalls.Load() != 1 {
		t.Fatalf("expected idle CleanupExpired before expiry to skip backend, got %d calls", backend.cleanupCalls.Load())
	}

	// At t0+11s (past expiry), CleanupExpired hits backend, deletes cold@example.com, learns DB is now empty.
	clockMu.Lock()
	now = now.Add(6 * time.Second)
	tCheck2 := now
	clockMu.Unlock()
	_, delM, err := cs.CleanupExpired(ctx, tCheck2)
	if err != nil || delM != 1 {
		t.Fatalf("expected 1 deleted mailbox, got delM=%d err=%v", delM, err)
	}
	if backend.cleanupCalls.Load() != 2 {
		t.Fatalf("expected 2 cleanup calls after expiry, got %d", backend.cleanupCalls.Load())
	}

	// When DB is empty, subsequent CleanupExpired ticks skip backend again.
	clockMu.Lock()
	now = now.Add(time.Hour)
	tCheck3 := now
	clockMu.Unlock()
	_, _, _ = cs.CleanupExpired(ctx, tCheck3)
	if backend.cleanupCalls.Load() != 2 {
		t.Fatalf("expected empty-DB CleanupExpired to skip backend, got %d calls", backend.cleanupCalls.Load())
	}
}

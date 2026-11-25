package main

import (
	"context"
	"sort"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

const (
	defaultNegCacheSize     = 4096
	unknownCreatedFallbackTTL = 5 * time.Minute
)

type inflightCall struct {
	wg  sync.WaitGroup
	val any
	err error
}

// singleflightGroup coalesces concurrent cache-miss lookups for the same key.
type singleflightGroup struct {
	mu    sync.Mutex
	calls map[string]*inflightCall
}

func (g *singleflightGroup) Do(key string, fn func() (any, error)) (any, error) {
	g.mu.Lock()
	if g.calls == nil {
		g.calls = make(map[string]*inflightCall)
	}
	if c, ok := g.calls[key]; ok {
		g.mu.Unlock()
		c.wg.Wait()
		return c.val, c.err
	}
	c := &inflightCall{}
	c.wg.Add(1)
	g.calls[key] = c
	g.mu.Unlock()

	defer func() {
		g.mu.Lock()
		delete(g.calls, key)
		g.mu.Unlock()
		c.wg.Done()
	}()

	c.val, c.err = fn()
	return c.val, c.err
}

type cachedMailbox struct {
	mailbox   string
	createdAt int64
	cachedAt  time.Time
}

type cachedAddr struct {
	createdAt int64
	lastToken string
	cachedAt  time.Time
}

type cachedEmailEntry struct {
	email *Email
	size  int64
}

type cachedInbox struct {
	emailIDs []string
	hydrated bool
	cachedAt time.Time
}

// CachedStore wraps a persistent Store with bounded LRU + memory-capped caching,
// negative SMTP lookup caching, singleflight coalescing, and skip-empty cleanup scheduling.
type CachedStore struct {
	backend     Store
	mailboxTTL  time.Duration
	emailTTL    time.Duration
	listTTL     time.Duration
	negativeTTL time.Duration
	nowFn       func() time.Time

	sf singleflightGroup

	mboxMu   sync.Mutex
	byToken  *lru.Cache[string, cachedMailbox]
	byAddr   *lru.Cache[string, cachedAddr]
	negAddr  *lru.Cache[string, time.Time]
	negToken *lru.Cache[string, time.Time]

	emailMu            sync.Mutex
	emailEnabled       bool
	maxEmailBytes      int64
	emailBytes         int64
	suppressOnEvict    bool
	emailLRU           *lru.Cache[string, cachedEmailEntry]
	inboxLRU           *lru.Cache[string, *cachedInbox]

	cleanupMu          sync.Mutex
	cleanupKnownEmail  bool
	cleanupKnownMbox   bool
	nextEmailExpireAt  int64 // 0 when table is known empty
	nextMboxExpireAt   int64 // 0 when table is known empty
}

func NewCachedStore(backend Store, cfg *Config) *CachedStore {
	maxMailboxes := cfg.CacheMaxMailboxes
	if maxMailboxes <= 0 {
		maxMailboxes = 10000
	}
	maxEmailBytes := cfg.CacheMaxEmailMemory

	cs := &CachedStore{
		backend:       backend,
		mailboxTTL:    cfg.MailboxTTL,
		emailTTL:      cfg.EmailTTL,
		listTTL:       cfg.CacheListTTL,
		negativeTTL:   cfg.CacheNegativeTTL,
		nowFn:         time.Now,
		emailEnabled:  maxEmailBytes > 0,
		maxEmailBytes: maxEmailBytes,
	}

	cs.byToken, _ = lru.New[string, cachedMailbox](maxMailboxes)
	cs.byAddr, _ = lru.New[string, cachedAddr](maxMailboxes)
	if cs.negativeTTL > 0 {
		cs.negAddr, _ = lru.New[string, time.Time](defaultNegCacheSize)
		cs.negToken, _ = lru.New[string, time.Time](defaultNegCacheSize)
	}

	if cs.emailEnabled {
		inboxCap := maxMailboxes
		emailCap := maxMailboxes * 4
		if emailCap < 10000 {
			emailCap = 10000
		}
		cs.inboxLRU, _ = lru.New[string, *cachedInbox](inboxCap)
		cs.emailLRU, _ = lru.NewWithEvict[string, cachedEmailEntry](emailCap, func(_ string, evicted cachedEmailEntry) {
			cs.emailBytes -= evicted.size
			if !cs.suppressOnEvict && evicted.email != nil {
				if inbox, ok := cs.inboxLRU.Peek(evicted.email.Mailbox); ok {
					inbox.hydrated = false
				}
			}
		})
	}

	return cs
}

func (s *CachedStore) isMailboxRecordValid(createdAt int64, cachedAt, now time.Time) bool {
	if cutoff, hasTTL := cutoffUnix(now, s.mailboxTTL); hasTTL {
		if createdAt > 0 {
			return createdAt > cutoff
		}
		// Legacy backends without CreatedAt use a conservative short freshness window.
		window := s.mailboxTTL
		if window > unknownCreatedFallbackTTL {
			window = unknownCreatedFallbackTTL
		}
		return now.Sub(cachedAt) < window
	}
	return true
}

func (s *CachedStore) isEmailValid(email *Email, now time.Time) bool {
	if email == nil {
		return false
	}
	if cutoff, hasTTL := cutoffUnix(now, s.emailTTL); hasTTL {
		return email.ReceivedAt > cutoff
	}
	return true
}

func (s *CachedStore) recordMailboxCache(token, mailbox string, createdAt int64, now time.Time) {
	s.mboxMu.Lock()
	defer s.mboxMu.Unlock()

	if prev, ok := s.byAddr.Peek(mailbox); ok && prev.lastToken != "" && prev.lastToken != token {
		s.byToken.Remove(prev.lastToken)
	}
	s.byAddr.Add(mailbox, cachedAddr{
		createdAt: createdAt,
		lastToken: token,
		cachedAt:  now,
	})
	if token != "" {
		s.byToken.Add(token, cachedMailbox{
			mailbox:   mailbox,
			createdAt: createdAt,
			cachedAt:  now,
		})
	}
	if s.negAddr != nil {
		s.negAddr.Remove(mailbox)
	}
	if s.negToken != nil && token != "" {
		s.negToken.Remove(token)
	}
}

func (s *CachedStore) noteWriteExpiryLocked(next *int64, known bool, createdOrReceivedAt int64, ttl time.Duration) {
	if !known || ttl <= 0 {
		return
	}
	exp := time.Unix(createdOrReceivedAt, 0).Add(ttl).Unix()
	if *next == 0 || exp < *next {
		*next = exp
	}
}

func (s *CachedStore) CreateMailbox(ctx context.Context, token, mailbox string) (bool, error) {
	var created bool
	var createdAt int64
	var err error

	if meta, ok := s.backend.(mailboxMetaStore); ok {
		created, createdAt, err = meta.CreateMailboxWithMeta(ctx, token, mailbox)
	} else {
		created, err = s.backend.CreateMailbox(ctx, token, mailbox)
		createdAt = s.nowFn().Unix()
	}
	if err != nil || !created {
		return created, err
	}

	now := s.nowFn()
	s.recordMailboxCache(token, mailbox, createdAt, now)

	// Re-allocated expired mailboxes may still have valid emails when EmailTTL > 0, so clear stale list state.
	if s.emailEnabled {
		s.emailMu.Lock()
		s.inboxLRU.Remove(mailbox)
		s.emailMu.Unlock()
	}

	s.cleanupMu.Lock()
	s.noteWriteExpiryLocked(&s.nextMboxExpireAt, s.cleanupKnownMbox, createdAt, s.mailboxTTL)
	s.cleanupMu.Unlock()

	return true, nil
}

func (s *CachedStore) MailboxExists(ctx context.Context, mailbox string) (bool, error) {
	now := s.nowFn()

	s.mboxMu.Lock()
	if entry, ok := s.byAddr.Get(mailbox); ok {
		if s.isMailboxRecordValid(entry.createdAt, entry.cachedAt, now) {
			s.mboxMu.Unlock()
			return true, nil
		}
		s.byAddr.Remove(mailbox)
		if entry.lastToken != "" {
			s.byToken.Remove(entry.lastToken)
		}
		if s.listTTL == 0 && entry.createdAt > 0 {
			if s.negAddr != nil && s.negativeTTL > 0 {
				s.negAddr.Add(mailbox, now.Add(s.negativeTTL))
			}
			s.mboxMu.Unlock()
			return false, nil
		}
	}
	if s.negAddr != nil {
		if until, ok := s.negAddr.Get(mailbox); ok {
			if now.Before(until) {
				s.mboxMu.Unlock()
				return false, nil
			}
			s.negAddr.Remove(mailbox)
		}
	}
	s.mboxMu.Unlock()

	type existsRes struct {
		exists    bool
		createdAt int64
	}
	val, err := s.sf.Do("exists:"+mailbox, func() (any, error) {
		var exists bool
		var createdAt int64
		var err error
		if meta, ok := s.backend.(mailboxMetaStore); ok {
			exists, createdAt, err = meta.MailboxExistsWithMeta(ctx, mailbox)
		} else {
			exists, err = s.backend.MailboxExists(ctx, mailbox)
		}
		return existsRes{exists: exists, createdAt: createdAt}, err
	})
	if err != nil {
		return false, err
	}

	res := val.(existsRes)
	now = s.nowFn()
	if res.exists {
		s.mboxMu.Lock()
		var lastToken string
		if prev, ok := s.byAddr.Peek(mailbox); ok {
			lastToken = prev.lastToken
		}
		s.byAddr.Add(mailbox, cachedAddr{
			createdAt: res.createdAt,
			lastToken: lastToken,
			cachedAt:  now,
		})
		if s.negAddr != nil {
			s.negAddr.Remove(mailbox)
		}
		s.mboxMu.Unlock()
		return true, nil
	}

	if s.negAddr != nil && s.negativeTTL > 0 {
		s.mboxMu.Lock()
		s.negAddr.Add(mailbox, now.Add(s.negativeTTL))
		s.mboxMu.Unlock()
	}
	return false, nil
}

func (s *CachedStore) GetMailboxByToken(ctx context.Context, token string) (string, bool, error) {
	if token == "" {
		return "", false, nil
	}
	now := s.nowFn()

	s.mboxMu.Lock()
	if entry, ok := s.byToken.Get(token); ok {
		if s.isMailboxRecordValid(entry.createdAt, entry.cachedAt, now) {
			s.mboxMu.Unlock()
			return entry.mailbox, true, nil
		}
		s.byToken.Remove(token)
		s.byAddr.Remove(entry.mailbox)
		if s.listTTL == 0 && entry.createdAt > 0 {
			if s.negToken != nil && s.negativeTTL > 0 {
				s.negToken.Add(token, now.Add(s.negativeTTL))
			}
			s.mboxMu.Unlock()
			return "", false, nil
		}
	}
	if s.negToken != nil {
		if until, ok := s.negToken.Get(token); ok {
			if now.Before(until) {
				s.mboxMu.Unlock()
				return "", false, nil
			}
			s.negToken.Remove(token)
		}
	}
	s.mboxMu.Unlock()

	type tokenRes struct {
		mailbox   string
		createdAt int64
		ok        bool
	}
	val, err := s.sf.Do("token:"+token, func() (any, error) {
		var mailbox string
		var createdAt int64
		var found bool
		var err error
		if meta, ok := s.backend.(mailboxMetaStore); ok {
			mailbox, createdAt, found, err = meta.GetMailboxByTokenWithMeta(ctx, token)
		} else {
			mailbox, found, err = s.backend.GetMailboxByToken(ctx, token)
		}
		return tokenRes{mailbox: mailbox, createdAt: createdAt, ok: found}, err
	})
	if err != nil {
		return "", false, err
	}

	res := val.(tokenRes)
	if !res.ok {
		if s.negToken != nil && s.negativeTTL > 0 {
			s.mboxMu.Lock()
			s.negToken.Add(token, s.nowFn().Add(s.negativeTTL))
			s.mboxMu.Unlock()
		}
		return "", false, nil
	}
	s.recordMailboxCache(token, res.mailbox, res.createdAt, s.nowFn())
	return res.mailbox, true, nil
}

func (s *CachedStore) putEmailLocked(email *Email) bool {
	if email == nil || !s.emailEnabled {
		return false
	}
	size := emailMemoryBytes(email)
	if size > s.maxEmailBytes {
		return false
	}
	if prev, ok := s.emailLRU.Peek(email.ID); ok && prev.email != nil && prev.size == size && prev.email == email {
		s.emailLRU.Get(email.ID)
		return true
	}
	if s.emailLRU.Contains(email.ID) {
		s.suppressOnEvict = true
		s.emailLRU.Remove(email.ID)
		s.suppressOnEvict = false
	}
	for s.emailLRU.Len() > 0 && s.emailBytes+size > s.maxEmailBytes {
		s.emailLRU.RemoveOldest()
	}
	s.emailBytes += size
	s.emailLRU.Add(email.ID, cachedEmailEntry{email: email, size: size})
	return true
}

func (s *CachedStore) SaveEmail(ctx context.Context, email *Email) (bool, error) {
	saved, err := s.backend.SaveEmail(ctx, email)
	if err != nil || !saved || email == nil {
		return saved, err
	}

	s.cleanupMu.Lock()
	s.noteWriteExpiryLocked(&s.nextEmailExpireAt, s.cleanupKnownEmail, email.ReceivedAt, s.emailTTL)
	s.cleanupMu.Unlock()

	if !s.emailEnabled {
		return true, nil
	}

	s.emailMu.Lock()
	defer s.emailMu.Unlock()

	if !s.putEmailLocked(email) {
		// Oversized email stored in DB but not cached; invalidate mailbox list so DB stays authoritative.
		if inbox, ok := s.inboxLRU.Peek(email.Mailbox); ok {
			inbox.hydrated = false
		}
		return true, nil
	}

	if inbox, ok := s.inboxLRU.Get(email.Mailbox); ok && inbox.hydrated {
		inbox.emailIDs = insertEmailIDSorted(inbox.emailIDs, email, s.emailLRU)
	}
	return true, nil
}

func insertEmailIDSorted(ids []string, email *Email, cache *lru.Cache[string, cachedEmailEntry]) []string {
	for _, id := range ids {
		if id == email.ID {
			return ids
		}
	}
	idx := sort.Search(len(ids), func(i int) bool {
		entry, ok := cache.Peek(ids[i])
		if !ok || entry.email == nil {
			return false
		}
		if entry.email.ReceivedAt != email.ReceivedAt {
			return entry.email.ReceivedAt < email.ReceivedAt
		}
		return entry.email.ID <= email.ID
	})
	next := make([]string, len(ids)+1)
	copy(next, ids[:idx])
	next[idx] = email.ID
	copy(next[idx+1:], ids[idx:])
	return next
}

func (s *CachedStore) ListEmailsByMailbox(ctx context.Context, mailbox string) ([]*Email, error) {
	now := s.nowFn()

	if s.emailEnabled {
		s.emailMu.Lock()
		if inbox, ok := s.inboxLRU.Get(mailbox); ok && inbox.hydrated {
			if s.listTTL <= 0 || now.Sub(inbox.cachedAt) < s.listTTL {
				result, complete := s.resolveValidEmailsLocked(inbox, now)
				if complete {
					s.emailMu.Unlock()
					return result, nil
				}
				inbox.hydrated = false
			}
		}
		s.emailMu.Unlock()
	}

	val, err := s.sf.Do("list:"+mailbox, func() (any, error) {
		return s.backend.ListEmailsByMailbox(ctx, mailbox)
	})
	if err != nil {
		return nil, err
	}

	emails := val.([]*Email)
	if !s.emailEnabled {
		return emails, nil
	}

	s.emailMu.Lock()
	ids := make([]string, 0, len(emails))
	allCached := true
	for _, e := range emails {
		if e == nil {
			continue
		}
		if !s.putEmailLocked(e) {
			allCached = false
			continue
		}
		ids = append(ids, e.ID)
	}
	if allCached {
		for _, id := range ids {
			if !s.emailLRU.Contains(id) {
				allCached = false
				break
			}
		}
	}
	if allCached {
		s.inboxLRU.Add(mailbox, &cachedInbox{
			emailIDs: ids,
			hydrated: true,
			cachedAt: s.nowFn(),
		})
	} else {
		s.inboxLRU.Remove(mailbox)
	}
	s.emailMu.Unlock()

	return emails, nil
}

func (s *CachedStore) resolveValidEmailsLocked(inbox *cachedInbox, now time.Time) ([]*Email, bool) {
	result := make([]*Email, 0, len(inbox.emailIDs))
	validIDs := make([]string, 0, len(inbox.emailIDs))
	s.suppressOnEvict = true
	defer func() { s.suppressOnEvict = false }()
	for _, id := range inbox.emailIDs {
		entry, ok := s.emailLRU.Get(id)
		if !ok || entry.email == nil {
			// Evicted under memory cap -> force clean DB reload.
			return nil, false
		}
		if !s.isEmailValid(entry.email, now) {
			s.emailLRU.Remove(id)
			continue
		}
		validIDs = append(validIDs, id)
		result = append(result, entry.email)
	}
	if len(validIDs) != len(inbox.emailIDs) {
		inbox.emailIDs = validIDs
	}
	return result, true
}

func (s *CachedStore) GetEmailByID(ctx context.Context, id string) (*Email, bool, error) {
	now := s.nowFn()

	if s.emailEnabled {
		s.emailMu.Lock()
		if entry, ok := s.emailLRU.Get(id); ok && entry.email != nil {
			if s.isEmailValid(entry.email, now) {
				email := entry.email
				s.emailMu.Unlock()
				return email, true, nil
			}
			s.removeExpiredEmailLocked(id, entry.email)
			if s.listTTL == 0 {
				s.emailMu.Unlock()
				return nil, false, nil
			}
		}
		s.emailMu.Unlock()
	}

	type getEmailRes struct {
		email *Email
		ok    bool
	}
	val, err := s.sf.Do("email:"+id, func() (any, error) {
		email, found, err := s.backend.GetEmailByID(ctx, id)
		return getEmailRes{email: email, ok: found}, err
	})
	if err != nil {
		return nil, false, err
	}

	res := val.(getEmailRes)
	if !res.ok || res.email == nil {
		return nil, false, nil
	}
	if s.emailEnabled {
		s.emailMu.Lock()
		s.putEmailLocked(res.email)
		s.emailMu.Unlock()
	}
	return res.email, true, nil
}

func (s *CachedStore) CleanupExpired(ctx context.Context, now time.Time) (int64, int64, error) {
	nowUnix := now.Unix()

	// Evict any expired items held in memory regardless of whether backend needs work.
	s.pruneMemoryCaches(now)

	// In single-instance mode (listTTL == 0), skip redundant cleanup calls until next known expiry arrives.
	if s.listTTL == 0 {
		s.cleanupMu.Lock()
		emailReady := s.emailTTL <= 0 || s.cleanupKnownEmail
		mboxReady := s.mailboxTTL <= 0 || s.cleanupKnownMbox
		needEmail := s.emailTTL > 0 && s.nextEmailExpireAt > 0 && nowUnix >= s.nextEmailExpireAt
		needMbox := s.mailboxTTL > 0 && s.nextMboxExpireAt > 0 && nowUnix >= s.nextMboxExpireAt
		if emailReady && mboxReady && !needEmail && !needMbox {
			s.cleanupMu.Unlock()
			return 0, 0, nil
		}
		s.cleanupMu.Unlock()
	}

	var res cleanupResult
	var err error
	if hintStore, ok := s.backend.(cleanupHintStore); ok {
		res, err = hintStore.CleanupExpiredWithHint(ctx, now)
	} else {
		var delE, delM int64
		delE, delM, err = s.backend.CleanupExpired(ctx, now)
		res = cleanupResult{DeletedEmails: delE, DeletedMailboxes: delM}
	}
	if err != nil {
		return res.DeletedEmails, res.DeletedMailboxes, err
	}

	s.cleanupMu.Lock()
	if res.HasEmailHint {
		s.cleanupKnownEmail = true
		if res.EarliestEmailAt == nil || s.emailTTL <= 0 {
			s.nextEmailExpireAt = 0
		} else {
			s.nextEmailExpireAt = time.Unix(*res.EarliestEmailAt, 0).Add(s.emailTTL).Unix()
		}
	}
	if res.HasMailboxHint {
		s.cleanupKnownMbox = true
		if res.EarliestMailboxAt == nil || s.mailboxTTL <= 0 {
			s.nextMboxExpireAt = 0
		} else {
			s.nextMboxExpireAt = time.Unix(*res.EarliestMailboxAt, 0).Add(s.mailboxTTL).Unix()
		}
	}
	s.cleanupMu.Unlock()

	return res.DeletedEmails, res.DeletedMailboxes, nil
}

func (s *CachedStore) pruneMemoryCaches(now time.Time) {
	s.mboxMu.Lock()
	for _, token := range s.byToken.Keys() {
		if entry, ok := s.byToken.Peek(token); ok && !s.isMailboxRecordValid(entry.createdAt, entry.cachedAt, now) {
			s.byToken.Remove(token)
		}
	}
	for _, addr := range s.byAddr.Keys() {
		if entry, ok := s.byAddr.Peek(addr); ok && !s.isMailboxRecordValid(entry.createdAt, entry.cachedAt, now) {
			s.byAddr.Remove(addr)
		}
	}
	if s.negAddr != nil {
		for _, addr := range s.negAddr.Keys() {
			if until, ok := s.negAddr.Peek(addr); ok && !now.Before(until) {
				s.negAddr.Remove(addr)
			}
		}
	}
	if s.negToken != nil {
		for _, tok := range s.negToken.Keys() {
			if until, ok := s.negToken.Peek(tok); ok && !now.Before(until) {
				s.negToken.Remove(tok)
			}
		}
	}
	s.mboxMu.Unlock()

	if s.emailEnabled {
		s.emailMu.Lock()
		for _, id := range s.emailLRU.Keys() {
			if entry, ok := s.emailLRU.Peek(id); ok && !s.isEmailValid(entry.email, now) {
				s.removeExpiredEmailLocked(id, entry.email)
			}
		}
		s.emailMu.Unlock()
	}
}

func (s *CachedStore) removeExpiredEmailLocked(id string, email *Email) {
	s.suppressOnEvict = true
	if email != nil {
		if inbox, ok := s.inboxLRU.Peek(email.Mailbox); ok {
			inbox.emailIDs = removeEmailIDFromSlice(inbox.emailIDs, id)
		}
	}
	s.emailLRU.Remove(id)
	s.suppressOnEvict = false
}

func removeEmailIDFromSlice(ids []string, target string) []string {
	for i, id := range ids {
		if id == target {
			out := make([]string, 0, len(ids)-1)
			out = append(out, ids[:i]...)
			out = append(out, ids[i+1:]...)
			return out
		}
	}
	return ids
}

func (s *CachedStore) Close() error {
	return s.backend.Close()
}

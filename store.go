package main

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// Store abstracts mailbox allocation, email persistence, and expired record reclamation.
type Store interface {
	CreateMailbox(ctx context.Context, token, mailbox string) (bool, error)
	MailboxExists(ctx context.Context, mailbox string) (bool, error)
	GetMailboxByToken(ctx context.Context, token string) (string, bool, error)
	SaveEmail(ctx context.Context, email *Email) (bool, error)
	ListEmailsByMailbox(ctx context.Context, mailbox string) ([]*Email, error)
	GetEmailByID(ctx context.Context, id string) (*Email, bool, error)
	CleanupExpired(ctx context.Context, now time.Time) (deletedEmails int64, deletedMailboxes int64, err error)
	Close() error
}

// NewStore builds the configured storage backend.
func NewStore(cfg *Config) (Store, error) {
	switch cfg.StorageType {
	case "", "memory":
		return NewMemoryStore(cfg.MaxMailboxes, cfg.MaxEmails, cfg.MaxEmailMemory), nil
	case "sqlite", "mysql", "postgres":
		return NewGormStore(cfg)
	case "d1":
		return NewD1Store(cfg)
	default:
		return nil, fmt.Errorf("unsupported storage type: %s", cfg.StorageType)
	}
}

// MemoryStore wraps MailboxStore and EmailStore for in-memory operation.
type MemoryStore struct {
	ms *MailboxStore
	es *EmailStore
}

func NewMemoryStore(maxMailboxes, maxEmails int, maxEmailBytes int64) *MemoryStore {
	return &MemoryStore{
		ms: NewMailboxStore(maxMailboxes),
		es: NewEmailStore(maxEmails, maxEmailBytes),
	}
}

func (m *MemoryStore) CreateMailbox(_ context.Context, token, mailbox string) (bool, error) {
	return m.ms.CreateIfAbsent(token, mailbox), nil
}

func (m *MemoryStore) MailboxExists(_ context.Context, mailbox string) (bool, error) {
	return m.ms.Exists(mailbox), nil
}

func (m *MemoryStore) GetMailboxByToken(_ context.Context, token string) (string, bool, error) {
	mailbox, ok := m.ms.GetByToken(token)
	return mailbox, ok, nil
}

func (m *MemoryStore) SaveEmail(_ context.Context, email *Email) (bool, error) {
	return m.es.Add(email), nil
}

func (m *MemoryStore) ListEmailsByMailbox(_ context.Context, mailbox string) ([]*Email, error) {
	return m.es.ListByMailbox(mailbox), nil
}

func (m *MemoryStore) GetEmailByID(_ context.Context, id string) (*Email, bool, error) {
	email, ok := m.es.GetByID(id)
	return email, ok, nil
}

func (m *MemoryStore) CleanupExpired(_ context.Context, _ time.Time) (int64, int64, error) {
	return 0, 0, nil
}

func (m *MemoryStore) Close() error {
	return nil
}

type MailboxStore struct {
	mu      sync.RWMutex
	byToken *lru.Cache[string, string] // token → mailbox
	byAddr  map[string]string          // mailbox → token
}

func NewMailboxStore(capacity int) *MailboxStore {
	if capacity <= 0 {
		capacity = 1
	}
	s := &MailboxStore{
		byAddr: make(map[string]string),
	}
	s.byToken, _ = lru.NewWithEvict[string, string](capacity, func(_ string, mailbox string) {
		delete(s.byAddr, mailbox)
	})
	return s
}

func (s *MailboxStore) Set(token, mailbox string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if oldMailbox, ok := s.byToken.Peek(token); ok && oldMailbox != mailbox {
		delete(s.byAddr, oldMailbox)
	}
	if oldToken, ok := s.byAddr[mailbox]; ok && oldToken != token {
		s.byToken.Remove(oldToken)
	}
	s.byToken.Add(token, mailbox)
	s.byAddr[mailbox] = token
}

func (s *MailboxStore) CreateIfAbsent(token, mailbox string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byAddr[mailbox]; exists {
		return false
	}
	if oldMailbox, ok := s.byToken.Peek(token); ok && oldMailbox != mailbox {
		delete(s.byAddr, oldMailbox)
	}
	s.byToken.Add(token, mailbox)
	s.byAddr[mailbox] = token
	return true
}

func (s *MailboxStore) GetByToken(token string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byToken.Get(token)
}

func (s *MailboxStore) GetByAddr(mailbox string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	token, ok := s.byAddr[mailbox]
	return token, ok
}

func (s *MailboxStore) Exists(mailbox string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.byAddr[mailbox]
	return ok
}

// emailEntryOverheadBytes approximates per-entry struct, map, heap, and index slice overhead.
const emailEntryOverheadBytes int64 = 256

type emailEntry struct {
	email *Email
	size  int64
	seq   uint64
	index int
}

type emailHeap []*emailEntry

func (h emailHeap) Len() int { return len(h) }

func (h emailHeap) Less(i, j int) bool {
	if h[i].email.ReceivedAt != h[j].email.ReceivedAt {
		return h[i].email.ReceivedAt < h[j].email.ReceivedAt
	}
	return h[i].seq < h[j].seq
}

func (h emailHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *emailHeap) Push(x any) {
	entry := x.(*emailEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *emailHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[:n-1]
	return entry
}

func emailMemoryBytes(e *Email) int64 {
	return emailEntryOverheadBytes +
		int64(len(e.ID)) +
		int64(len(e.Mailbox)) +
		int64(len(e.From)) +
		int64(len(e.Subject)) +
		int64(len(e.BodyHTML))
}

type EmailStore struct {
	mu         sync.RWMutex
	maxCount   int
	maxBytes   int64
	totalBytes int64
	seq        uint64
	byID       map[string]*emailEntry // emailID → entry
	byMbox     map[string][]string    // mailbox → []emailID (descending by receivedAt)
	order      emailHeap              // min-heap ordered by (receivedAt, seq)
}

func NewEmailStore(maxCount int, maxBytes int64) *EmailStore {
	return &EmailStore{
		maxCount: maxCount,
		maxBytes: maxBytes,
		byID:     make(map[string]*emailEntry),
		byMbox:   make(map[string][]string),
	}
}

// Add inserts an email and evicts the oldest emails by ReceivedAt (then insertion order)
// until both maxCount and maxBytes limits are satisfied. Returns false if the email
// itself exceeds maxBytes and cannot be stored.
func (s *EmailStore) Add(email *Email) bool {
	if email == nil {
		return false
	}

	size := emailMemoryBytes(email)

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.maxBytes > 0 && size > s.maxBytes {
		return false
	}

	if existing, ok := s.byID[email.ID]; ok {
		s.removeEntryLocked(existing)
	}

	for s.order.Len() > 0 &&
		((s.maxCount > 0 && len(s.byID) >= s.maxCount) ||
			(s.maxBytes > 0 && s.totalBytes+size > s.maxBytes)) {
		oldest := heap.Pop(&s.order).(*emailEntry)
		s.removeEntryLocked(oldest)
	}

	entry := &emailEntry{
		email: email,
		size:  size,
		seq:   s.seq,
	}
	s.seq++
	heap.Push(&s.order, entry)
	s.byID[email.ID] = entry
	s.totalBytes += size

	ids := s.byMbox[email.Mailbox]
	inserted := false
	for i, id := range ids {
		if existing, ok := s.byID[id]; ok && existing.email.ReceivedAt <= email.ReceivedAt {
			newIDs := make([]string, len(ids)+1)
			copy(newIDs, ids[:i])
			newIDs[i] = email.ID
			copy(newIDs[i+1:], ids[i:])
			s.byMbox[email.Mailbox] = newIDs
			inserted = true
			break
		}
	}
	if !inserted {
		s.byMbox[email.Mailbox] = append(ids, email.ID)
	}
	return true
}

func (s *EmailStore) removeEntryLocked(entry *emailEntry) {
	if entry.index >= 0 {
		heap.Remove(&s.order, entry.index)
	}
	delete(s.byID, entry.email.ID)
	s.totalBytes -= entry.size

	ids := s.byMbox[entry.email.Mailbox]
	for i := len(ids) - 1; i >= 0; i-- {
		if ids[i] == entry.email.ID {
			if len(ids) == 1 {
				delete(s.byMbox, entry.email.Mailbox)
			} else {
				copy(ids[i:], ids[i+1:])
				ids[len(ids)-1] = ""
				ids = ids[:len(ids)-1]
				if cap(ids) > 16 && len(ids)*4 <= cap(ids) {
					shrunk := make([]string, len(ids))
					copy(shrunk, ids)
					ids = shrunk
				}
				s.byMbox[entry.email.Mailbox] = ids
			}
			break
		}
	}
}

func (s *EmailStore) GetByID(id string) (*Email, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	return entry.email, true
}

func (s *EmailStore) ListByMailbox(mailbox string) []*Email {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byMbox[mailbox]
	result := make([]*Email, 0, len(ids))
	for _, id := range ids {
		if entry, ok := s.byID[id]; ok {
			result = append(result, entry.email)
		}
	}
	return result
}

func (s *EmailStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byID)
}

func (s *EmailStore) TotalBytes() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.totalBytes
}

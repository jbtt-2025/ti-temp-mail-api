package main

import (
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"
)

type MailboxStore struct {
	mu      sync.RWMutex
	byToken *lru.Cache[string, string] // token → mailbox
	byAddr  *lru.Cache[string, string] // mailbox → token
}

func NewMailboxStore(capacity int) *MailboxStore {
	t, _ := lru.New[string, string](capacity)
	a, _ := lru.New[string, string](capacity)
	return &MailboxStore{byToken: t, byAddr: a}
}

func (s *MailboxStore) Set(token, mailbox string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byToken.Add(token, mailbox)
	s.byAddr.Add(mailbox, token)
}

func (s *MailboxStore) GetByToken(token string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byToken.Get(token)
}

func (s *MailboxStore) GetByAddr(mailbox string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byAddr.Get(mailbox)
}

func (s *MailboxStore) Exists(mailbox string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byAddr.Contains(mailbox)
}

type EmailStore struct {
	mu     sync.RWMutex
	byID   *lru.Cache[string, *Email] // emailID → Email
	byMbox map[string][]string        // mailbox → []emailID (descending by receivedAt)
}

func NewEmailStore(capacity int) *EmailStore {
	c, _ := lru.New[string, *Email](capacity)
	return &EmailStore{byID: c, byMbox: make(map[string][]string)}
}

func (s *EmailStore) Add(email *Email) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID.Add(email.ID, email)
	ids := s.byMbox[email.Mailbox]
	inserted := false
	for i, id := range ids {
		if e, ok := s.byID.Peek(id); ok && e.ReceivedAt < email.ReceivedAt {
			newIds := make([]string, len(ids)+1)
			copy(newIds, ids[:i])
			newIds[i] = email.ID
			copy(newIds[i+1:], ids[i:])
			s.byMbox[email.Mailbox] = newIds
			inserted = true
			break
		}
	}
	if !inserted {
		s.byMbox[email.Mailbox] = append(ids, email.ID)
	}
}

func (s *EmailStore) GetByID(id string) (*Email, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byID.Get(id)
}

func (s *EmailStore) ListByMailbox(mailbox string) []*Email {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := s.byMbox[mailbox]
	result := make([]*Email, 0, len(ids))
	for _, id := range ids {
		if e, ok := s.byID.Peek(id); ok {
			result = append(result, e)
		}
	}
	return result
}

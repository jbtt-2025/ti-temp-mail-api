package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func makeTestEmail(id, mailbox string, receivedAt int64, bodySize int) *Email {
	return &Email{
		ID:         id,
		ReceivedAt: receivedAt,
		Mailbox:    mailbox,
		From:       "sender@example.com",
		Subject:    "test",
		BodyHTML:   strings.Repeat("a", bodySize),
	}
}

func TestEmailStore_EvictByTimeAndMemoryLimit(t *testing.T) {
	e1 := makeTestEmail("e1", "m1@example.com", 100, 300)
	e2 := makeTestEmail("e2", "m2@example.com", 200, 300)
	e3 := makeTestEmail("e3", "m3@example.com", 150, 300) // out-of-order: older than e2, newer than e1

	// Capacity fits two emails (each ~600B), not three (~1800B).
	limit := emailMemoryBytes(e1) + emailMemoryBytes(e2)
	es := NewEmailStore(0, limit)

	if !es.Add(e1) || !es.Add(e2) {
		t.Fatal("expected e1 and e2 to fit")
	}
	if !es.Add(e3) {
		t.Fatal("expected e3 to evict e1 and fit")
	}

	if _, ok := es.GetByID("e1"); ok {
		t.Fatal("expected oldest email e1 (t=100) to be evicted")
	}
	if _, ok := es.GetByID("e2"); !ok {
		t.Fatal("expected e2 (t=200) to remain")
	}
	if _, ok := es.GetByID("e3"); !ok {
		t.Fatal("expected e3 (t=150) to remain")
	}
	if len(es.byMbox) != 2 {
		t.Fatalf("expected byMbox to clean up m1, got len=%d", len(es.byMbox))
	}

	// Adding e4 should evict e3 (t=150) before e2 (t=200).
	e4 := makeTestEmail("e4", "m4@example.com", 250, 300)
	if !es.Add(e4) {
		t.Fatal("expected e4 to fit after evicting e3")
	}
	if _, ok := es.GetByID("e3"); ok {
		t.Fatal("expected e3 (t=150) to be evicted before e2 (t=200)")
	}
	if _, ok := es.GetByID("e2"); !ok {
		t.Fatal("expected e2 (t=200) to remain")
	}
	if es.TotalBytes() > limit {
		t.Fatalf("TotalBytes=%d exceeded limit=%d", es.TotalBytes(), limit)
	}
}

func TestEmailStore_FIFOTieBreakOnEqualTimestamp(t *testing.T) {
	e1 := makeTestEmail("e1", "m1@example.com", 500, 100)
	e2 := makeTestEmail("e2", "m1@example.com", 500, 100)
	e3 := makeTestEmail("e3", "m1@example.com", 500, 100)

	es := NewEmailStore(2, 0)
	es.Add(e1)
	es.Add(e2)
	es.Add(e3)

	if _, ok := es.GetByID("e1"); ok {
		t.Fatal("expected first-inserted e1 to be evicted")
	}
	list := es.ListByMailbox("m1@example.com")
	if len(list) != 2 || list[0].ID != "e3" || list[1].ID != "e2" {
		t.Fatalf("unexpected mailbox order/content: %+v", list)
	}
}

func TestEmailStore_ByMboxNoLeak(t *testing.T) {
	es := NewEmailStore(5, 0)
	for i := 0; i < 100; i++ {
		mb := fmt.Sprintf("user%d@example.com", i)
		es.Add(makeTestEmail(fmt.Sprintf("id-%d", i), mb, int64(1000+i), 64))
	}
	if es.Len() != 5 {
		t.Fatalf("Len=%d, want 5", es.Len())
	}
	if len(es.byMbox) != 5 {
		t.Fatalf("byMbox leaked keys: len=%d, want 5", len(es.byMbox))
	}
}

func TestEmailStore_OversizedSingleEmailRejected(t *testing.T) {
	small := makeTestEmail("small", "m@example.com", 100, 100)
	limit := emailMemoryBytes(small) + 50
	es := NewEmailStore(10, limit)
	if !es.Add(small) {
		t.Fatal("expected small email to succeed")
	}

	huge := makeTestEmail("huge", "m@example.com", 200, 500)
	if es.Add(huge) {
		t.Fatal("expected oversized single email to be rejected")
	}
	if _, ok := es.GetByID("small"); !ok {
		t.Fatal("expected existing email not to be evicted by rejected oversized email")
	}
	if es.TotalBytes() != emailMemoryBytes(small) {
		t.Fatalf("TotalBytes=%d, want %d", es.TotalBytes(), emailMemoryBytes(small))
	}
}

func TestEmailStore_ConcurrentAccess(t *testing.T) {
	es := NewEmailStore(50, 32*1024)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("w%d-%d", worker, i)
				mb := fmt.Sprintf("u%d@example.com", i%10)
				es.Add(makeTestEmail(id, mb, int64(i), 200))
				es.GetByID(id)
				es.ListByMailbox(mb)
			}
		}(g)
	}
	wg.Wait()
	if es.Len() > 50 || es.TotalBytes() > 32*1024 {
		t.Fatalf("limits violated: len=%d bytes=%d", es.Len(), es.TotalBytes())
	}
}

func TestMailboxStore_SyncEviction(t *testing.T) {
	ms := NewMailboxStore(2)
	ms.Set("t1", "m1@example.com")
	ms.Set("t2", "m2@example.com")

	// Access t1 so t2 becomes least-recently-used.
	if mb, ok := ms.GetByToken("t1"); !ok || mb != "m1@example.com" {
		t.Fatalf("unexpected GetByToken(t1): %q, %v", mb, ok)
	}

	ms.Set("t3", "m3@example.com")

	if !ms.Exists("m1@example.com") {
		t.Fatal("expected hot mailbox m1 to still exist")
	}
	if ms.Exists("m2@example.com") {
		t.Fatal("expected evicted mailbox m2 to be removed from byAddr")
	}
	if _, ok := ms.GetByToken("t2"); ok {
		t.Fatal("expected t2 to be evicted")
	}
}

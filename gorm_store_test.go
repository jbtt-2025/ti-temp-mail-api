package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestGormStore_SQLiteLifecycleAndIndependentExpiration(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "nested", "test.db")
	now := time.Unix(1700000000, 0)

	cfg := &Config{
		StorageType:     "sqlite",
		DBDSN:           dbPath,
		MailboxTTL:      10 * time.Second, // shorter mailbox TTL
		EmailTTL:        30 * time.Second, // longer independent email TTL
		CleanupInterval: time.Minute,
	}

	rawStore, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore(sqlite) failed: %v", err)
	}
	defer rawStore.Close()

	gs := rawStore.(*GormStore)
	gs.nowFn = func() time.Time { return now }

	ctx := context.Background()

	// Create mailbox and verify duplicate address detection.
	created, err := gs.CreateMailbox(ctx, "tok-1", "a@example.com")
	if err != nil || !created {
		t.Fatalf("CreateMailbox(a) created=%v err=%v", created, err)
	}
	dup, err := gs.CreateMailbox(ctx, "tok-2", "a@example.com")
	if err != nil || dup {
		t.Fatalf("expected duplicate address to return created=false, err=nil; got created=%v err=%v", dup, err)
	}

	// Verify existence and token lookup while active.
	if exists, err := gs.MailboxExists(ctx, "a@example.com"); err != nil || !exists {
		t.Fatalf("MailboxExists=%v err=%v", exists, err)
	}
	if mbox, ok, err := gs.GetMailboxByToken(ctx, "tok-1"); err != nil || !ok || mbox != "a@example.com" {
		t.Fatalf("GetMailboxByToken=%q ok=%v err=%v", mbox, ok, err)
	}

	// Save two emails for a@example.com at t=now and t=now+5s.
	e1 := &Email{
		ID:               "msg-1",
		ReceivedAt:       now.Unix(),
		Mailbox:          "a@example.com",
		From:             "s1@example.com",
		Subject:          "first",
		BodyHTML:         "<p>1</p>",
		AttachmentsCount: 0,
	}
	e2 := &Email{
		ID:               "msg-2",
		ReceivedAt:       now.Add(5 * time.Second).Unix(),
		Mailbox:          "a@example.com",
		From:             "s2@example.com",
		Subject:          "second",
		BodyHTML:         "<p>2</p>",
		AttachmentsCount: 1,
	}
	if ok, err := gs.SaveEmail(ctx, e1); err != nil || !ok {
		t.Fatalf("SaveEmail(e1) ok=%v err=%v", ok, err)
	}
	if ok, err := gs.SaveEmail(ctx, e2); err != nil || !ok {
		t.Fatalf("SaveEmail(e2) ok=%v err=%v", ok, err)
	}

	list, err := gs.ListEmailsByMailbox(ctx, "a@example.com")
	if err != nil || len(list) != 2 || list[0].ID != "msg-2" || list[1].ID != "msg-1" {
		t.Fatalf("unexpected ListEmailsByMailbox result: %+v err=%v", list, err)
	}

	// Advance clock to t+15s: mailbox expired (TTL 10s), emails still active (TTL 30s).
	now = now.Add(15 * time.Second)

	if exists, err := gs.MailboxExists(ctx, "a@example.com"); err != nil || exists {
		t.Fatalf("expected expired mailbox to return exists=false, got %v err=%v", exists, err)
	}
	if _, ok, err := gs.GetMailboxByToken(ctx, "tok-1"); err != nil || ok {
		t.Fatalf("expected expired token lookup to return ok=false, got %v err=%v", ok, err)
	}

	// Create a new active mailbox b@example.com before cleanup so we verify cleanup spares active rows.
	if created, err := gs.CreateMailbox(ctx, "tok-b", "b@example.com"); err != nil || !created {
		t.Fatalf("CreateMailbox(b) created=%v err=%v", created, err)
	}

	delEmails, delMailboxes, err := gs.CleanupExpired(ctx, now)
	if err != nil {
		t.Fatalf("CleanupExpired failed: %v", err)
	}
	if delEmails != 0 || delMailboxes != 1 {
		t.Fatalf("CleanupExpired deletedEmails=%d deletedMailboxes=%d, want 0, 1", delEmails, delMailboxes)
	}

	// Verify emails survived mailbox cleanup because EMAIL_TTL (30s) has not elapsed yet.
	if _, ok, err := gs.GetEmailByID(ctx, "msg-1"); err != nil || !ok {
		t.Fatalf("expected msg-1 to remain until EmailTTL expires, ok=%v err=%v", ok, err)
	}

	// Advance clock to t+31s: e1 (expires at t+30) expired, e2 (expires at t+35) and b@example.com (expires at t+25? wait t+15+10=t+25!)
	// Note b@example.com was created at t+15 with MailboxTTL 10s -> expires at t+25.
	now = time.Unix(1700000000, 0).Add(31 * time.Second)
	delEmails, delMailboxes, err = gs.CleanupExpired(ctx, now)
	if err != nil {
		t.Fatalf("CleanupExpired step 2 failed: %v", err)
	}
	if delEmails != 1 || delMailboxes != 1 {
		t.Fatalf("CleanupExpired step 2 deletedEmails=%d deletedMailboxes=%d, want 1, 1", delEmails, delMailboxes)
	}

	if _, ok, err := gs.GetEmailByID(ctx, "msg-1"); err != nil || ok {
		t.Fatalf("expected msg-1 expired & deleted")
	}
	if got, ok, err := gs.GetEmailByID(ctx, "msg-2"); err != nil || !ok || got.BodyHTML != "<p>2</p>" {
		t.Fatalf("expected msg-2 still active, got=%+v ok=%v err=%v", got, ok, err)
	}

	// Changing TTL dynamically takes effect immediately on existing records without migrating DB rows:
	// e2 was received at t+5s and current time is t+31s (age 26s); tightening EmailTTL to 20s expires e2 immediately.
	gs.emailTTL = 20 * time.Second
	if _, ok, err := gs.GetEmailByID(ctx, "msg-2"); err != nil || ok {
		t.Fatalf("expected msg-2 to expire immediately after tightening EmailTTL, ok=%v err=%v", ok, err)
	}
	// Disabling EmailTTL (0) restores visibility before cleanup runs.
	gs.emailTTL = 0
	if _, ok, err := gs.GetEmailByID(ctx, "msg-2"); err != nil || !ok {
		t.Fatalf("expected msg-2 to remain visible when EmailTTL=0, ok=%v err=%v", ok, err)
	}
}

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	gosmtp "github.com/emersion/go-smtp"
)

func TestNewSMTPServer_MaxMessageBytes(t *testing.T) {
	cfg := &Config{
		MailDomains:    []string{"example.com"},
		SMTPPort:       2525,
		MaxEmails:      100,
		MaxEmailMemory: 1024 * 1024,
		MaxMailboxes:   100,
	}
	store := NewMemoryStore(cfg.MaxMailboxes, cfg.MaxEmails, cfg.MaxEmailMemory)
	s := NewSMTPServer(cfg, store)

	if s.MaxMessageBytes != 20*1024*1024 {
		t.Fatalf("MaxMessageBytes = %d, want %d", s.MaxMessageBytes, 20*1024*1024)
	}
}

func TestSMTPSession_RcptAndDataLimits(t *testing.T) {
	cfg := &Config{MailDomains: []string{"example.com"}}
	ctx := context.Background()

	t.Run("rcpt validates allocated mailbox", func(t *testing.T) {
		store := NewMemoryStore(10, 10, 64*1024*1024)
		_, _ = store.CreateMailbox(ctx, "tok", "u@example.com")
		sess := &smtpSession{backend: &smtpBackend{cfg: cfg, store: store}}

		if err := sess.Rcpt("missing@example.com", nil); err == nil {
			t.Fatal("expected 550 error for unallocated mailbox")
		}
		if err := sess.Rcpt("u@example.com", nil); err != nil {
			t.Fatalf("expected allocated mailbox to be accepted, got %v", err)
		}
	})

	t.Run("rejects message over 20MB", func(t *testing.T) {
		store := NewMemoryStore(10, 10, 64*1024*1024)
		_, _ = store.CreateMailbox(ctx, "tok", "u@example.com")
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, store: store},
			rcptTo:  "u@example.com",
		}

		oversized := bytes.NewReader(make([]byte, maxMessageBytes+10))
		err := sess.Data(oversized)
		if !errors.Is(err, gosmtp.ErrDataTooLarge) {
			t.Fatalf("expected ErrDataTooLarge, got %v", err)
		}
		if store.es.Len() != 0 {
			t.Fatalf("expected no email stored, got %d", store.es.Len())
		}
	})

	t.Run("rejects parsed email larger than MAX_EMAIL_MEMORY", func(t *testing.T) {
		store := NewMemoryStore(10, 10, 1024) // 1KB store cap
		_, _ = store.CreateMailbox(ctx, "tok", "u@example.com")
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, store: store},
			rcptTo:  "u@example.com",
		}

		raw := "From: a@example.com\r\nSubject: big\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>" +
			strings.Repeat("x", 4096) + "</p>\r\n"
		err := sess.Data(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error when parsed email exceeds MAX_EMAIL_MEMORY")
		}
		if store.es.Len() != 0 {
			t.Fatalf("expected no email stored, got %d", store.es.Len())
		}
	})

	t.Run("stores valid MIME email without retaining BodyText", func(t *testing.T) {
		store := NewMemoryStore(10, 10, 1024*1024)
		_, _ = store.CreateMailbox(ctx, "tok", "u@example.com")
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, store: store},
			rcptTo:  "u@example.com",
		}

		raw := "From: sender@example.com\r\nSubject: hi\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>hello</b>\r\n"
		if err := sess.Data(strings.NewReader(raw)); err != nil {
			t.Fatalf("Data failed: %v", err)
		}
		msgs, err := store.ListEmailsByMailbox(ctx, "u@example.com")
		if err != nil {
			t.Fatalf("ListEmailsByMailbox failed: %v", err)
		}
		if len(msgs) != 1 || msgs[0].BodyHTML == "" || msgs[0].Subject != "hi" {
			t.Fatalf("unexpected stored email: %+v", msgs)
		}
	})
}

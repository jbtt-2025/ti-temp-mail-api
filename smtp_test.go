package main

import (
	"bytes"
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
	ms := NewMailboxStore(cfg.MaxMailboxes)
	es := NewEmailStore(cfg.MaxEmails, cfg.MaxEmailMemory)
	s := NewSMTPServer(cfg, ms, es)

	if s.MaxMessageBytes != 20*1024*1024 {
		t.Fatalf("MaxMessageBytes = %d, want %d", s.MaxMessageBytes, 20*1024*1024)
	}
}

func TestSMTPSession_DataLimits(t *testing.T) {
	cfg := &Config{MailDomains: []string{"example.com"}}
	ms := NewMailboxStore(10)
	ms.Set("tok", "u@example.com")

	t.Run("rejects message over 20MB", func(t *testing.T) {
		es := NewEmailStore(10, 64*1024*1024)
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, ms: ms, es: es},
			rcptTo:  "u@example.com",
		}

		oversized := bytes.NewReader(make([]byte, maxMessageBytes+10))
		err := sess.Data(oversized)
		if !errors.Is(err, gosmtp.ErrDataTooLarge) {
			t.Fatalf("expected ErrDataTooLarge, got %v", err)
		}
		if es.Len() != 0 {
			t.Fatalf("expected no email stored, got %d", es.Len())
		}
	})

	t.Run("rejects parsed email larger than MAX_EMAIL_MEMORY", func(t *testing.T) {
		es := NewEmailStore(10, 1024) // 1KB store cap
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, ms: ms, es: es},
			rcptTo:  "u@example.com",
		}

		raw := "From: a@example.com\r\nSubject: big\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>" +
			strings.Repeat("x", 4096) + "</p>\r\n"
		err := sess.Data(strings.NewReader(raw))
		if err == nil {
			t.Fatal("expected error when parsed email exceeds MAX_EMAIL_MEMORY")
		}
		if es.Len() != 0 {
			t.Fatalf("expected no email stored, got %d", es.Len())
		}
	})

	t.Run("stores valid MIME email without retaining BodyText", func(t *testing.T) {
		es := NewEmailStore(10, 1024*1024)
		sess := &smtpSession{
			backend: &smtpBackend{cfg: cfg, ms: ms, es: es},
			rcptTo:  "u@example.com",
		}

		raw := "From: sender@example.com\r\nSubject: hi\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<b>hello</b>\r\n"
		if err := sess.Data(strings.NewReader(raw)); err != nil {
			t.Fatalf("Data failed: %v", err)
		}
		msgs := es.ListByMailbox("u@example.com")
		if len(msgs) != 1 || msgs[0].BodyHTML == "" || msgs[0].Subject != "hi" {
			t.Fatalf("unexpected stored email: %+v", msgs)
		}
	})
}

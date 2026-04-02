package main

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	gosmtp "github.com/emersion/go-smtp"
)

type smtpBackend struct {
	cfg *Config
	ms  *MailboxStore
	es  *EmailStore
}

type smtpSession struct {
	backend *smtpBackend
	rcptTo  string
	buf     bytes.Buffer
}

func (b *smtpBackend) NewSession(c *gosmtp.Conn) (gosmtp.Session, error) {
	return &smtpSession{backend: b}, nil
}

func (s *smtpSession) AuthPlain(username, password string) error {
	return nil
}

func (s *smtpSession) Mail(from string, opts *gosmtp.MailOptions) error {
	return nil
}

func (s *smtpSession) Rcpt(to string, opts *gosmtp.RcptOptions) error {
	parts := strings.SplitN(to, "@", 2)
	if len(parts) != 2 {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: "Invalid recipient"}
	}

	if !s.backend.ms.Exists(to) {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: "Mailbox does not exist"}
	}

	s.rcptTo = to
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	s.buf.Reset()
	if _, err := io.Copy(&s.buf, r); err != nil {
		return err
	}
	rawData := s.buf.Bytes()

	parsed, parseErr := ParseMIME(rawData)
	if parseErr != nil {
		log.Printf("SMTP: MIME parse error for %s: %v", s.rcptTo, parseErr)
	}

	email := &Email{
		ID:               newUUID(),
		ReceivedAt:       time.Now().Unix(),
		Mailbox:          s.rcptTo,
		From:             parsed.From,
		Subject:          parsed.Subject,
		BodyText:         parsed.BodyText,
		BodyHTML:         parsed.BodyHTML,
		AttachmentsCount: parsed.AttachmentsCount,
	}
	s.backend.es.Add(email)
	return nil
}

func (s *smtpSession) Reset() {
	s.rcptTo = ""
	s.buf.Reset()
}

func (s *smtpSession) Logout() error {
	return nil
}

func newUUID() string {
	var uuid [16]byte
	rand.Read(uuid[:])
	uuid[6] = (uuid[6] & 0x0f) | 0x40 // version 4
	uuid[8] = (uuid[8] & 0x3f) | 0x80 // variant bits
	return fmt.Sprintf("%x-%x-%x-%x-%x", uuid[0:4], uuid[4:6], uuid[6:8], uuid[8:10], uuid[10:])
}

func NewSMTPServer(cfg *Config, ms *MailboxStore, es *EmailStore) *gosmtp.Server {
	be := &smtpBackend{cfg: cfg, ms: ms, es: es}
	s := gosmtp.NewServer(be)
	s.Addr = fmt.Sprintf(":%d", cfg.SMTPPort)
	s.Domain = "localhost"
	s.AllowInsecureAuth = true
	return s
}

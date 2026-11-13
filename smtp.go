package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	gosmtp "github.com/emersion/go-smtp"
)

const maxMessageBytes int64 = 20 * 1024 * 1024 // 20MB

type smtpBackend struct {
	cfg   *Config
	store Store
}

type smtpSession struct {
	backend *smtpBackend
	rcptTo  string
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

	exists, err := s.backend.store.MailboxExists(context.Background(), to)
	if err != nil {
		log.Printf("SMTP: MailboxExists error for %s: %v", to, err)
		return &gosmtp.SMTPError{Code: 451, EnhancedCode: gosmtp.EnhancedCode{4, 3, 0}, Message: "Temporary storage error"}
	}
	if !exists {
		return &gosmtp.SMTPError{Code: 550, EnhancedCode: gosmtp.EnhancedCode{5, 1, 1}, Message: "Mailbox does not exist"}
	}

	s.rcptTo = to
	return nil
}

func (s *smtpSession) Data(r io.Reader) error {
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, io.LimitReader(r, maxMessageBytes+1)); err != nil {
		return err
	}
	if int64(buf.Len()) > maxMessageBytes {
		return gosmtp.ErrDataTooLarge
	}
	rawData := buf.Bytes()

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
		BodyHTML:         parsed.BodyHTML,
		AttachmentsCount: parsed.AttachmentsCount,
	}
	saved, err := s.backend.store.SaveEmail(context.Background(), email)
	if err != nil {
		log.Printf("SMTP: SaveEmail error for %s: %v", s.rcptTo, err)
		return &gosmtp.SMTPError{
			Code:         451,
			EnhancedCode: gosmtp.EnhancedCode{4, 3, 0},
			Message:      "Temporary storage error",
		}
	}
	if !saved {
		log.Printf("SMTP: message for %s exceeds email memory limit (%d bytes)", s.rcptTo, emailMemoryBytes(email))
		return &gosmtp.SMTPError{
			Code:         552,
			EnhancedCode: gosmtp.EnhancedCode{5, 3, 4},
			Message:      "Message exceeds server email memory limit",
		}
	}
	return nil
}

func (s *smtpSession) Reset() {
	s.rcptTo = ""
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

func NewSMTPServer(cfg *Config, store Store) *gosmtp.Server {
	be := &smtpBackend{cfg: cfg, store: store}
	s := gosmtp.NewServer(be)
	s.Addr = fmt.Sprintf(":%d", cfg.SMTPPort)
	s.Domain = "localhost"
	s.MaxMessageBytes = maxMessageBytes
	s.AllowInsecureAuth = true
	return s
}

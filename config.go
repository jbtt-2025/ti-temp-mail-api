package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	MailDomains  []string // MAIL_DOMAIN, comma-separated, required
	SMTPPort     int      // SMTP_PORT, default 25
	HTTPPort     int      // HTTP_PORT, default 8080
	MaxEmails    int      // MAX_EMAILS, default 10000
	MaxMailboxes int      // MAX_MAILBOXES, default 10000
	CreateToken  string   // CREATE_TOKEN, optional
}

func LoadConfig() (*Config, error) {
	mailDomain := os.Getenv("MAIL_DOMAIN")
	if strings.TrimSpace(mailDomain) == "" {
		return nil, errors.New("MAIL_DOMAIN environment variable is required")
	}

	domains := []string{}
	for _, d := range strings.Split(mailDomain, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			domains = append(domains, d)
		}
	}

	cfg := &Config{
		MailDomains:  domains,
		SMTPPort:     25,
		HTTPPort:     8080,
		MaxEmails:    10000,
		MaxMailboxes: 10000,
		CreateToken:  os.Getenv("CREATE_TOKEN"),
	}

	if v := os.Getenv("SMTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid SMTP_PORT: %w", err)
		}
		cfg.SMTPPort = n
	}

	if v := os.Getenv("HTTP_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid HTTP_PORT: %w", err)
		}
		cfg.HTTPPort = n
	}

	if v := os.Getenv("MAX_EMAILS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_EMAILS: %w", err)
		}
		cfg.MaxEmails = n
	}

	if v := os.Getenv("MAX_MAILBOXES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_MAILBOXES: %w", err)
		}
		cfg.MaxMailboxes = n
	}

	return cfg, nil
}

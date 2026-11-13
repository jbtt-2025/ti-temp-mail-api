package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
)

const defaultMaxEmailMemory int64 = 100 * 1024 * 1024 // 100MB

type Config struct {
	MailDomains    []string // MAIL_DOMAIN, comma-separated, required
	SMTPPort       int      // SMTP_PORT, default 25
	HTTPPort       int      // HTTP_PORT, default 8080
	MaxEmails      int      // MAX_EMAILS, default 10000
	MaxEmailMemory int64    // MAX_EMAIL_MEMORY, default 100MB (bytes, 0 = unlimited)
	MaxMailboxes   int      // MAX_MAILBOXES, default 10000
	CreateToken    string   // CREATE_TOKEN, optional
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
	if len(domains) == 0 {
		return nil, errors.New("MAIL_DOMAIN environment variable is required")
	}

	cfg := &Config{
		MailDomains:    domains,
		SMTPPort:       25,
		HTTPPort:       8080,
		MaxEmails:      10000,
		MaxEmailMemory: defaultMaxEmailMemory,
		MaxMailboxes:   10000,
		CreateToken:    os.Getenv("CREATE_TOKEN"),
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
		if n < 0 {
			return nil, errors.New("invalid MAX_EMAILS: must be non-negative")
		}
		cfg.MaxEmails = n
	}

	if v := os.Getenv("MAX_EMAIL_MEMORY"); v != "" {
		n, err := parseByteSize(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_EMAIL_MEMORY: %w", err)
		}
		cfg.MaxEmailMemory = n
	}

	if cfg.MaxEmails == 0 && cfg.MaxEmailMemory == 0 {
		return nil, errors.New("at least one of MAX_EMAILS or MAX_EMAIL_MEMORY must be greater than 0")
	}

	if v := os.Getenv("MAX_MAILBOXES"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAX_MAILBOXES: %w", err)
		}
		if n <= 0 {
			return nil, errors.New("invalid MAX_MAILBOXES: must be greater than 0")
		}
		cfg.MaxMailboxes = n
	}

	return cfg, nil
}

// parseByteSize parses an integer byte count or size string with B/K/KB/KiB/M/MB/MiB/G/GB/GiB suffix.
func parseByteSize(raw string) (int64, error) {
	s := strings.TrimSpace(strings.ToUpper(raw))
	if s == "" {
		return 0, errors.New("empty size")
	}

	idx := 0
	for idx < len(s) {
		c := s[idx]
		if (c >= '0' && c <= '9') || c == '.' || (idx == 0 && (c == '+' || c == '-')) {
			idx++
			continue
		}
		break
	}
	numStr := strings.TrimSpace(s[:idx])
	unitStr := strings.TrimSpace(s[idx:])
	if numStr == "" {
		return 0, fmt.Errorf("missing numeric value in %q", raw)
	}

	var multiplier int64
	switch unitStr {
	case "", "B":
		multiplier = 1
	case "K", "KB", "KIB":
		multiplier = 1024
	case "M", "MB", "MIB":
		multiplier = 1024 * 1024
	case "G", "GB", "GIB":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown size unit %q", unitStr)
	}

	if strings.Contains(numStr, ".") {
		if unitStr == "" || unitStr == "B" {
			return 0, fmt.Errorf("fractional bytes not supported in %q", raw)
		}
		f, err := strconv.ParseFloat(numStr, 64)
		if err != nil {
			return 0, err
		}
		if f < 0 || math.IsNaN(f) || math.IsInf(f, 0) {
			return 0, fmt.Errorf("size cannot be negative or infinite: %q", raw)
		}
		val := f * float64(multiplier)
		if val > float64(math.MaxInt64) {
			return 0, fmt.Errorf("size overflows int64: %q", raw)
		}
		return int64(val), nil
	}

	n, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, err
	}
	if n < 0 {
		return 0, fmt.Errorf("size cannot be negative: %q", raw)
	}
	if n > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("size overflows int64: %q", raw)
	}
	return n * multiplier, nil
}

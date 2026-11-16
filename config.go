package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxEmailMemory  int64         = 100 * 1024 * 1024 // 100MB
	defaultMailboxTTL      time.Duration = 24 * time.Hour
	defaultEmailTTL        time.Duration = 24 * time.Hour
	defaultCleanupInterval time.Duration = 5 * time.Minute
	defaultSQLiteDSN                     = "temp-mail.db"
)

type Config struct {
	MailDomains     []string      // MAIL_DOMAIN, comma-separated, required
	MXDomain        string        // MX_DOMAIN, optional, defaults to MailDomains[0]
	SMTPPort        int           // SMTP_PORT, default 25
	HTTPPort        int           // HTTP_PORT, default 8080
	MaxEmails       int           // MAX_EMAILS, default 10000 (memory mode)
	MaxEmailMemory  int64         // MAX_EMAIL_MEMORY, default 100MB (memory mode)
	MaxMailboxes    int           // MAX_MAILBOXES, default 10000 (memory mode)
	CreateToken     string        // CREATE_TOKEN, optional
	StorageType     string        // STORAGE_TYPE: memory|sqlite|mysql|postgres|d1, default memory
	DBDSN           string        // DB_DSN (or SQLITE_PATH for sqlite)
	D1APIURL        string        // D1_API_URL (required when STORAGE_TYPE=d1)
	D1APIToken      string        // D1_API_TOKEN (required when STORAGE_TYPE=d1)
	MailboxTTL      time.Duration // MAILBOX_TTL, default 24h (non-memory, 0 = never expire)
	EmailTTL        time.Duration // EMAIL_TTL, default 24h (non-memory, 0 = never expire)
	CleanupInterval time.Duration // CLEANUP_INTERVAL, default 5m (non-memory)
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

	mxDomain := strings.TrimSuffix(strings.TrimSpace(os.Getenv("MX_DOMAIN")), ".")
	if mxDomain == "" {
		mxDomain = domains[0]
	}

	cfg := &Config{
		MailDomains:     domains,
		MXDomain:        mxDomain,
		SMTPPort:        25,
		HTTPPort:        8080,
		MaxEmails:       10000,
		MaxEmailMemory:  defaultMaxEmailMemory,
		MaxMailboxes:    10000,
		CreateToken:     os.Getenv("CREATE_TOKEN"),
		StorageType:     "memory",
		MailboxTTL:      defaultMailboxTTL,
		EmailTTL:        defaultEmailTTL,
		CleanupInterval: defaultCleanupInterval,
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

	if v := os.Getenv("STORAGE_TYPE"); v != "" {
		st, err := normalizeStorageType(v)
		if err != nil {
			return nil, err
		}
		cfg.StorageType = st
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

	if cfg.StorageType == "memory" && cfg.MaxEmails == 0 && cfg.MaxEmailMemory == 0 {
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

	if v := os.Getenv("MAILBOX_TTL"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid MAILBOX_TTL: %w", err)
		}
		cfg.MailboxTTL = d
	}

	if v := os.Getenv("EMAIL_TTL"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid EMAIL_TTL: %w", err)
		}
		cfg.EmailTTL = d
	}

	if v := os.Getenv("CLEANUP_INTERVAL"); v != "" {
		d, err := parseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("invalid CLEANUP_INTERVAL: %w", err)
		}
		if d <= 0 {
			return nil, errors.New("invalid CLEANUP_INTERVAL: must be greater than 0")
		}
		cfg.CleanupInterval = d
	}

	cfg.DBDSN = strings.TrimSpace(os.Getenv("DB_DSN"))
	if cfg.StorageType == "sqlite" && cfg.DBDSN == "" {
		cfg.DBDSN = strings.TrimSpace(os.Getenv("SQLITE_PATH"))
		if cfg.DBDSN == "" {
			cfg.DBDSN = defaultSQLiteDSN
		}
	}
	if (cfg.StorageType == "mysql" || cfg.StorageType == "postgres") && cfg.DBDSN == "" {
		return nil, fmt.Errorf("DB_DSN environment variable is required for %s storage", cfg.StorageType)
	}

	cfg.D1APIURL = strings.TrimRight(strings.TrimSpace(os.Getenv("D1_API_URL")), "/")
	cfg.D1APIToken = strings.TrimSpace(os.Getenv("D1_API_TOKEN"))
	if cfg.StorageType == "d1" {
		if cfg.D1APIURL == "" {
			return nil, errors.New("D1_API_URL environment variable is required for d1 storage")
		}
		if cfg.D1APIToken == "" {
			return nil, errors.New("D1_API_TOKEN environment variable is required for d1 storage")
		}
	}

	return cfg, nil
}

func normalizeStorageType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "memory", "mem":
		return "memory", nil
	case "sqlite", "sqlite3":
		return "sqlite", nil
	case "mysql":
		return "mysql", nil
	case "pg", "postgres", "postgresql":
		return "postgres", nil
	case "d1":
		return "d1", nil
	default:
		return "", fmt.Errorf("invalid STORAGE_TYPE %q: must be one of memory, sqlite, mysql, postgres, d1", raw)
	}
}

// parseDuration parses "0", standard Go durations (e.g. "30m", "24h"), or day-suffixed strings (e.g. "7d").
func parseDuration(raw string) (time.Duration, error) {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return 0, errors.New("empty duration")
	}
	if s == "0" {
		return 0, nil
	}
	if strings.HasSuffix(s, "d") {
		numStr := strings.TrimSpace(strings.TrimSuffix(s, "d"))
		days, err := strconv.ParseFloat(numStr, 64)
		if err != nil || math.IsNaN(days) || math.IsInf(days, 0) {
			return 0, fmt.Errorf("invalid day duration %q", raw)
		}
		if days < 0 {
			return 0, fmt.Errorf("duration cannot be negative: %q", raw)
		}
		ns := days * float64(24*time.Hour)
		if ns > float64(math.MaxInt64) {
			return 0, fmt.Errorf("duration overflows int64: %q", raw)
		}
		return time.Duration(ns), nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("duration cannot be negative: %q", raw)
	}
	return d, nil
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

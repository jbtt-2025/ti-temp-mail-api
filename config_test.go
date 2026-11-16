package main

import (
	"testing"
	"time"
)

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		{"0", 0, false},
		{"1024", 1024, false},
		{"1024B", 1024, false},
		{"512K", 512 * 1024, false},
		{"512KB", 512 * 1024, false},
		{"512KiB", 512 * 1024, false},
		{"100M", 100 * 1024 * 1024, false},
		{"100MB", 100 * 1024 * 1024, false},
		{" 256 mib ", 256 * 1024 * 1024, false},
		{"1G", 1024 * 1024 * 1024, false},
		{"1.5MB", int64(1.5 * 1024 * 1024), false},
		{"", 0, true},
		{"-10MB", 0, true},
		{"10TB", 0, true},
		{"1.5B", 0, true},
		{"1.5", 0, true},
		{"abc", 0, true},
		{"999999999999GB", 0, true},
	}

	for _, tc := range tests {
		got, err := parseByteSize(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseByteSize(%q) error = %v, wantErr=%v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseByteSize(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("MAX_EMAIL_MEMORY", "")
		t.Setenv("MAX_EMAILS", "")
		t.Setenv("MAX_MAILBOXES", "")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.MaxEmailMemory != 100*1024*1024 {
			t.Fatalf("MaxEmailMemory = %d, want %d", cfg.MaxEmailMemory, 100*1024*1024)
		}
		if cfg.MaxEmails != 10000 {
			t.Fatalf("MaxEmails = %d, want 10000", cfg.MaxEmails)
		}
		if cfg.MXDomain != "example.com" {
			t.Fatalf("MXDomain = %q, want %q", cfg.MXDomain, "example.com")
		}
	})

	t.Run("explicit MX_DOMAIN trims whitespace and trailing dot", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com,alt.example.org")
		t.Setenv("MX_DOMAIN", "  mx.mailhost.net.  ")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.MXDomain != "mx.mailhost.net" {
			t.Fatalf("MXDomain = %q, want %q", cfg.MXDomain, "mx.mailhost.net")
		}
	})

	t.Run("custom MAX_EMAIL_MEMORY and zero MAX_EMAILS", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("MAX_EMAIL_MEMORY", "64MB")
		t.Setenv("MAX_EMAILS", "0")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.MaxEmailMemory != 64*1024*1024 {
			t.Fatalf("MaxEmailMemory = %d, want %d", cfg.MaxEmailMemory, 64*1024*1024)
		}
		if cfg.MaxEmails != 0 {
			t.Fatalf("MaxEmails = %d, want 0", cfg.MaxEmails)
		}
	})

	t.Run("reject both zero limits", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("MAX_EMAIL_MEMORY", "0")
		t.Setenv("MAX_EMAILS", "0")

		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error when both MAX_EMAILS and MAX_EMAIL_MEMORY are 0")
		}
	})

	t.Run("reject invalid domain list", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", " , ")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for empty domain entries")
		}
	})
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input   string
		want    time.Duration
		wantErr bool
	}{
		{"0", 0, false},
		{"30m", 30 * time.Minute, false},
		{"24h", 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"1.5d", 36 * time.Hour, false},
		{"", 0, true},
		{"-1h", 0, true},
		{"-2d", 0, true},
		{"bad", 0, true},
	}
	for _, tc := range tests {
		got, err := parseDuration(tc.input)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseDuration(%q) err=%v, wantErr=%v", tc.input, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("parseDuration(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestLoadConfig_StorageModes(t *testing.T) {
	t.Run("sqlite with independent TTLs and zero memory limits", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("STORAGE_TYPE", "sqlite")
		t.Setenv("SQLITE_PATH", "data/test.db")
		t.Setenv("MAX_EMAILS", "0")
		t.Setenv("MAX_EMAIL_MEMORY", "0")
		t.Setenv("MAILBOX_TTL", "7d")
		t.Setenv("EMAIL_TTL", "12h")
		t.Setenv("CLEANUP_INTERVAL", "2m")

		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.StorageType != "sqlite" || cfg.DBDSN != "data/test.db" {
			t.Fatalf("unexpected storage config: %+v", cfg)
		}
		if cfg.MailboxTTL != 7*24*time.Hour || cfg.EmailTTL != 12*time.Hour || cfg.CleanupInterval != 2*time.Minute {
			t.Fatalf("unexpected TTL/cleanup config: %+v", cfg)
		}
	})

	t.Run("pg alias requires DB_DSN", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("STORAGE_TYPE", "pg")
		t.Setenv("DB_DSN", "")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error when DB_DSN missing for pg")
		}

		t.Setenv("DB_DSN", "host=localhost dbname=test")
		cfg, err := LoadConfig()
		if err != nil || cfg.StorageType != "postgres" {
			t.Fatalf("expected postgres storage type, got %v, err=%v", cfg, err)
		}
	})

	t.Run("d1 requires url and token", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("STORAGE_TYPE", "d1")
		t.Setenv("D1_API_URL", "https://example.workers.dev/")
		t.Setenv("D1_API_TOKEN", "")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error when D1_API_TOKEN missing")
		}

		t.Setenv("D1_API_TOKEN", "secret")
		cfg, err := LoadConfig()
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.D1APIURL != "https://example.workers.dev" {
			t.Fatalf("expected trailing slash trimmed, got %q", cfg.D1APIURL)
		}
	})

	t.Run("reject invalid storage type and zero cleanup interval", func(t *testing.T) {
		t.Setenv("MAIL_DOMAIN", "example.com")
		t.Setenv("STORAGE_TYPE", "redis")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for invalid STORAGE_TYPE")
		}

		t.Setenv("STORAGE_TYPE", "sqlite")
		t.Setenv("CLEANUP_INTERVAL", "0")
		if _, err := LoadConfig(); err == nil {
			t.Fatal("expected error for CLEANUP_INTERVAL=0")
		}
	})
}

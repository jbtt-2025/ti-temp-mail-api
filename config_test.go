package main

import (
	"testing"
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

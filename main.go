package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	cfg, err := LoadConfig()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	store, err := NewStore(cfg)
	if err != nil {
		log.Fatalf("Failed to initialize storage (%s): %v", cfg.StorageType, err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			log.Printf("Storage close error: %v", err)
		}
	}()

	if cfg.StorageType == "memory" {
		log.Printf("Storage: memory (maxMailboxes=%d maxEmails=%d maxEmailMemory=%dB maxMessageBytes=%dB)",
			cfg.MaxMailboxes, cfg.MaxEmails, cfg.MaxEmailMemory, maxMessageBytes)
	} else {
		log.Printf("Storage: %s (mailboxTTL=%s emailTTL=%s cleanupInterval=%s maxMessageBytes=%dB)",
			cfg.StorageType, cfg.MailboxTTL, cfg.EmailTTL, cfg.CleanupInterval, maxMessageBytes)
	}

	httpServer := NewHTTPServer(cfg, store)
	smtpServer := NewSMTPServer(cfg, store)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

	if cfg.StorageType != "memory" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runCleanupLoop(ctx, store, cfg.CleanupInterval)
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("HTTP server listening on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		log.Printf("SMTP server listening on %s", smtpServer.Addr)
		if err := smtpServer.ListenAndServe(); err != nil {
			log.Printf("SMTP server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("Shutting down...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	httpServer.Shutdown(shutdownCtx)
	smtpServer.Shutdown(shutdownCtx)

	wg.Wait()
	log.Println("Shutdown complete")
}

func runCleanupLoop(ctx context.Context, store Store, interval time.Duration) {
	runOnce := func() {
		cleanupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		deletedEmails, deletedMailboxes, err := store.CleanupExpired(cleanupCtx, time.Now())
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("Cleanup error: %v", err)
			}
			return
		}
		if deletedEmails > 0 || deletedMailboxes > 0 {
			log.Printf("Cleanup reclaimed %d expired emails, %d expired mailboxes", deletedEmails, deletedMailboxes)
		}
	}

	runOnce()

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runOnce()
		}
	}
}

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

	ms := NewMailboxStore(cfg.MaxMailboxes)
	es := NewEmailStore(cfg.MaxEmails)

	httpServer := NewHTTPServer(cfg, ms, es)
	smtpServer := NewSMTPServer(cfg, ms, es)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup

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

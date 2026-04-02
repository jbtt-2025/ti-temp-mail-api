package main

import (
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
)

const letters = "abcdefghijklmnopqrstuvwxyz"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[mathrand.Intn(len(letters))]
	}
	return string(b)
}


func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func NewHTTPServer(cfg *Config, ms *MailboxStore, es *EmailStore) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /mailbox", func(w http.ResponseWriter, r *http.Request) {
		handleCreateMailbox(w, r, cfg, ms)
	})
	mux.HandleFunc("POST /mailbox", func(w http.ResponseWriter, r *http.Request) {
		handleCreateMailbox(w, r, cfg, ms)
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		handleListMessages(w, r, ms, es)
	})
	mux.HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleGetMessage(w, r, ms, es)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	})

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: mux,
	}
}

type createMailboxRequest struct {
	Domain string `json:"domain"`
	Type   string `json:"type"`
}

func handleCreateMailbox(w http.ResponseWriter, r *http.Request, cfg *Config, ms *MailboxStore) {
	// Check CREATE_TOKEN if set
	if cfg.CreateToken != "" {
		if r.Header.Get("Authorization") != cfg.CreateToken {
			writeError(w, http.StatusForbidden, "Forbidden")
			return
		}
	}

	// Parse optional JSON body
	var req createMailboxRequest
	if r.Body != nil && r.ContentLength != 0 {
		json.NewDecoder(r.Body).Decode(&req)
	}

	// Determine domain
	domain := req.Domain
	if domain == "" {
		domain = cfg.MailDomains[mathrand.Intn(len(cfg.MailDomains))]
	}

	// Generate mailbox address with up to 10 retries
	var mailbox string
	for i := 0; i < 10; i++ {
		var addr string
		if req.Type == "subdomain" {
			addr = fmt.Sprintf("%s@%s.%s", randString(10), randString(8), domain)
		} else {
			addr = fmt.Sprintf("%s@%s", randString(10), domain)
		}
		if !ms.Exists(addr) {
			mailbox = addr
			break
		}
	}

	if mailbox == "" {
		writeError(w, http.StatusInternalServerError, "Failed to generate unique mailbox address")
		return
	}

	token := newUUID()
	ms.Set(token, mailbox)

	writeJSON(w, http.StatusCreated, CreateMailboxResponse{
		Token:   token,
		Mailbox: mailbox,
	})
}

func handleListMessages(w http.ResponseWriter, r *http.Request, ms *MailboxStore, es *EmailStore) {
	token := r.Header.Get("Authorization")
	mailbox, ok := ms.GetByToken(token)
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "Unauthorized: Invalid or missing token.")
		return
	}

	emails := es.ListByMailbox(mailbox)
	summaries := make([]MessageSummary, 0, len(emails))
	for _, e := range emails {
		summaries = append(summaries, MessageSummary{
			ID:               e.ID,
			ReceivedAt:       e.ReceivedAt,
			From:             e.From,
			Subject:          e.Subject,
			BodyPreview:      bodyPreview(e.BodyHTML),
			AttachmentsCount: e.AttachmentsCount,
		})
	}

	writeJSON(w, http.StatusOK, ListMessagesResponse{
		Mailbox:  mailbox,
		Messages: summaries,
	})
}

func handleGetMessage(w http.ResponseWriter, r *http.Request, ms *MailboxStore, es *EmailStore) {
	token := r.Header.Get("Authorization")
	mailbox, ok := ms.GetByToken(token)
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "Unauthorized: Invalid or missing token.")
		return
	}

	id := r.PathValue("id")
	email, ok := es.GetByID(id)
	if !ok || email.Mailbox != mailbox {
		writeError(w, http.StatusNotFound, "Message not found or access denied.")
		return
	}

	writeJSON(w, http.StatusOK, MessageDetail{
		ID:               email.ID,
		ReceivedAt:       email.ReceivedAt,
		Mailbox:          email.Mailbox,
		From:             email.From,
		Subject:          email.Subject,
		BodyPreview:      bodyPreview(email.BodyHTML),
		BodyHTML:         email.BodyHTML,
		AttachmentsCount: email.AttachmentsCount,
		Attachments:      []interface{}{},
	})
}

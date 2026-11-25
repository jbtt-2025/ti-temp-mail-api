package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestD1Store_WorkerAPIProtocol(t *testing.T) {
	const expectedToken = "worker-secret-token"

	var mu sync.Mutex
	mailboxes := make(map[string]struct {
		token     string
		createdAt int64
	})
	tokens := make(map[string]string)
	emails := make(map[string]*Email)

	now := time.Unix(1700000000, 0)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Unauthorized"}`))
			return
		}

		mu.Lock()
		defer mu.Unlock()

		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/mailboxes":
			var body struct {
				Token         string `json:"token"`
				Mailbox       string `json:"mailbox"`
				CreatedAt     int64  `json:"createdAt"`
				ExpiredBefore *int64 `json:"expiredBefore"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if existing, exists := mailboxes[body.Mailbox]; exists {
				if body.ExpiredBefore != nil && existing.createdAt <= *body.ExpiredBefore {
					delete(tokens, existing.token)
					delete(mailboxes, body.Mailbox)
				} else {
					w.WriteHeader(http.StatusConflict)
					_, _ = w.Write([]byte(`{"created":false}`))
					return
				}
			}
			mailboxes[body.Mailbox] = struct {
				token     string
				createdAt int64
			}{token: body.Token, createdAt: body.CreatedAt}
			tokens[body.Token] = body.Mailbox
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"created":true}`))

		case r.Method == http.MethodGet && r.URL.Path == "/mailboxes/exists":
			addr := r.URL.Query().Get("address")
			rec, ok := mailboxes[addr]
			exists := ok
			if exists && r.URL.Query().Has("minCreatedAt") {
				minCreatedAt, _ := strconv.ParseInt(r.URL.Query().Get("minCreatedAt"), 10, 64)
				exists = rec.createdAt > minCreatedAt
			}
			var createdAtPtr *int64
			if exists {
				v := rec.createdAt
				createdAtPtr = &v
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exists": exists, "createdAt": createdAtPtr})

		case r.Method == http.MethodPost && r.URL.Path == "/mailboxes/by-token":
			var body struct {
				Token        string `json:"token"`
				MinCreatedAt *int64 `json:"minCreatedAt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addr, ok := tokens[body.Token]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			rec := mailboxes[addr]
			if body.MinCreatedAt != nil && rec.createdAt <= *body.MinCreatedAt {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"mailbox": addr, "createdAt": rec.createdAt})

		case r.Method == http.MethodPost && r.URL.Path == "/emails":
			var body struct {
				ID               string `json:"id"`
				Mailbox          string `json:"mailbox"`
				ReceivedAt       int64  `json:"receivedAt"`
				From             string `json:"from"`
				Subject          string `json:"subject"`
				BodyHTML         string `json:"bodyHtml"`
				AttachmentsCount int    `json:"attachmentsCount"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			emails[body.ID] = &Email{
				ID:               body.ID,
				ReceivedAt:       body.ReceivedAt,
				Mailbox:          body.Mailbox,
				From:             body.From,
				Subject:          body.Subject,
				BodyHTML:         body.BodyHTML,
				AttachmentsCount: body.AttachmentsCount,
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"saved":true}`))

		case r.Method == http.MethodGet && r.URL.Path == "/emails":
			mbox := r.URL.Query().Get("mailbox")
			var minReceivedAt *int64
			if r.URL.Query().Has("minReceivedAt") {
				v, _ := strconv.ParseInt(r.URL.Query().Get("minReceivedAt"), 10, 64)
				minReceivedAt = &v
			}
			var list []*Email
			for _, e := range emails {
				if e.Mailbox == mbox && (minReceivedAt == nil || e.ReceivedAt > *minReceivedAt) {
					list = append(list, e)
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"emails": list})

		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/emails/"):
			id := strings.TrimPrefix(r.URL.Path, "/emails/")
			e, ok := emails[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			if r.URL.Query().Has("minReceivedAt") {
				v, _ := strconv.ParseInt(r.URL.Query().Get("minReceivedAt"), 10, 64)
				if e.ReceivedAt <= v {
					w.WriteHeader(http.StatusNotFound)
					return
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"email": e})

		case r.Method == http.MethodPost && r.URL.Path == "/cleanup":
			var body struct {
				EmailBefore   *int64 `json:"emailBefore"`
				MailboxBefore *int64 `json:"mailboxBefore"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			var delE, delM int64
			var earliestE, earliestM *int64
			if body.EmailBefore != nil {
				for id, e := range emails {
					if e.ReceivedAt <= *body.EmailBefore {
						delete(emails, id)
						delE++
					} else if earliestE == nil || e.ReceivedAt < *earliestE {
						v := e.ReceivedAt
						earliestE = &v
					}
				}
			}
			if body.MailboxBefore != nil {
				for addr, rec := range mailboxes {
					if rec.createdAt <= *body.MailboxBefore {
						delete(tokens, rec.token)
						delete(mailboxes, addr)
						delM++
					} else if earliestM == nil || rec.createdAt < *earliestM {
						v := rec.createdAt
						earliestM = &v
					}
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"deletedEmails":     delE,
				"deletedMailboxes":  delM,
				"earliestEmailAt":   earliestE,
				"earliestMailboxAt": earliestM,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	store, err := NewStore(&Config{
		StorageType: "d1",
		D1APIURL:    srv.URL,
		D1APIToken:  expectedToken,
		MailboxTTL:  10 * time.Second,
		EmailTTL:    20 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewStore(d1) failed: %v", err)
	}
	defer store.Close()

	d1 := store.(*D1Store)
	d1.nowFn = func() time.Time { return now }
	ctx := context.Background()

	created, err := d1.CreateMailbox(ctx, "tok-1", "u1@example.com")
	if err != nil || !created {
		t.Fatalf("CreateMailbox failed: created=%v err=%v", created, err)
	}
	dup, err := d1.CreateMailbox(ctx, "tok-2", "u1@example.com")
	if err != nil || dup {
		t.Fatalf("expected conflict to return created=false, err=nil; got created=%v err=%v", dup, err)
	}

	if exists, err := d1.MailboxExists(ctx, "u1@example.com"); err != nil || !exists {
		t.Fatalf("MailboxExists=%v err=%v", exists, err)
	}
	if mbox, ok, err := d1.GetMailboxByToken(ctx, "tok-1"); err != nil || !ok || mbox != "u1@example.com" {
		t.Fatalf("GetMailboxByToken=%q ok=%v err=%v", mbox, ok, err)
	}

	email := &Email{
		ID:         "e1",
		ReceivedAt: now.Unix(),
		Mailbox:    "u1@example.com",
		From:       "sender@example.com",
		Subject:    "hello d1",
		BodyHTML:   "<h1>hi</h1>",
	}
	if saved, err := d1.SaveEmail(ctx, email); err != nil || !saved {
		t.Fatalf("SaveEmail=%v err=%v", saved, err)
	}

	list, err := d1.ListEmailsByMailbox(ctx, "u1@example.com")
	if err != nil || len(list) != 1 || list[0].BodyHTML != "<h1>hi</h1>" {
		t.Fatalf("ListEmailsByMailbox=%+v err=%v", list, err)
	}
	got, ok, err := d1.GetEmailByID(ctx, "e1")
	if err != nil || !ok || got.Subject != "hello d1" {
		t.Fatalf("GetEmailByID=%+v ok=%v err=%v", got, ok, err)
	}

	// Advance time to +15s: mailbox expired (TTL 10s), email still active (TTL 20s).
	now = now.Add(15 * time.Second)
	if exists, err := d1.MailboxExists(ctx, "u1@example.com"); err != nil || exists {
		t.Fatalf("expected mailbox expired at +15s, exists=%v err=%v", exists, err)
	}
	if _, ok, err := d1.GetEmailByID(ctx, "e1"); err != nil || !ok {
		t.Fatalf("expected email still active at +15s, ok=%v err=%v", ok, err)
	}
	if recreated, err := d1.CreateMailbox(ctx, "tok-1b", "u1@example.com"); err != nil || !recreated {
		t.Fatalf("expected expired mailbox re-creation to succeed, recreated=%v err=%v", recreated, err)
	}
	if dupActive, err := d1.CreateMailbox(ctx, "tok-1c", "u1@example.com"); err != nil || dupActive {
		t.Fatalf("expected active re-created mailbox to reject duplicate, dupActive=%v err=%v", dupActive, err)
	}

	// Advance time past both (+11s -> +26s): both expired, trigger Go-driven cleanup.
	now = now.Add(11 * time.Second)
	delE, delM, err := d1.CleanupExpired(ctx, now)
	if err != nil || delE != 1 || delM != 1 {
		t.Fatalf("CleanupExpired=(%d,%d,%v), want (1,1,nil)", delE, delM, err)
	}

	// Verify bad token returns error.
	badStore, _ := NewD1Store(&Config{D1APIURL: srv.URL, D1APIToken: "wrong"})
	if _, err := badStore.MailboxExists(ctx, "u1@example.com"); err == nil {
		t.Fatal("expected auth failure error with wrong token")
	}
}

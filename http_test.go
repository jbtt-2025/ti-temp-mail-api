package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPServer_WithSQLiteAndExpiration(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cfg := &Config{
		MailDomains:     []string{"example.com"},
		StorageType:     "sqlite",
		DBDSN:           ":memory:",
		MailboxTTL:      20 * time.Second,
		EmailTTL:        10 * time.Second,
		CleanupInterval: time.Minute,
	}

	store, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore failed: %v", err)
	}
	defer store.Close()

	gs := store.(*GormStore)
	gs.nowFn = func() time.Time { return now }

	srv := NewHTTPServer(cfg, store)

	// 1. Create mailbox.
	req := httptest.NewRequest(http.MethodPost, "/mailbox", nil)
	rec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /mailbox status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var created CreateMailboxResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode CreateMailboxResponse: %v", err)
	}

	// 2. Save an email for that mailbox.
	ok, err := store.SaveEmail(context.Background(), &Email{
		ID:         "msg-100",
		ReceivedAt: now.Unix(),
		Mailbox:    created.Mailbox,
		From:       "noreply@example.com",
		Subject:    "Verify Code",
		BodyHTML:   "<p>123456</p>",
	})
	if err != nil || !ok {
		t.Fatalf("SaveEmail ok=%v err=%v", ok, err)
	}

	// 3. List & get detail while both mailbox and email are valid.
	reqList := httptest.NewRequest(http.MethodGet, "/messages", nil)
	reqList.Header.Set("Authorization", created.Token)
	recList := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recList, reqList)
	if recList.Code != http.StatusOK {
		t.Fatalf("GET /messages status = %d", recList.Code)
	}
	var listResp ListMessagesResponse
	_ = json.Unmarshal(recList.Body.Bytes(), &listResp)
	if len(listResp.Messages) != 1 || listResp.Messages[0].ID != "msg-100" {
		t.Fatalf("unexpected list response: %+v", listResp)
	}

	reqDetail := httptest.NewRequest(http.MethodGet, "/messages/msg-100", nil)
	reqDetail.Header.Set("Authorization", created.Token)
	recDetail := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recDetail, reqDetail)
	if recDetail.Code != http.StatusOK {
		t.Fatalf("GET /messages/msg-100 status = %d", recDetail.Code)
	}
	var detailResp MessageDetail
	_ = json.Unmarshal(recDetail.Body.Bytes(), &detailResp)
	if detailResp.BodyHTML != "<p>123456</p>" {
		t.Fatalf("unexpected BodyHTML: %q", detailResp.BodyHTML)
	}

	// 4. Advance past EmailTTL (10s) while MailboxTTL (20s) is still active.
	now = now.Add(15 * time.Second)
	recList2 := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recList2, reqList)
	if recList2.Code != http.StatusOK {
		t.Fatalf("expected 200 with empty list after EmailTTL, got %d", recList2.Code)
	}
	var listResp2 ListMessagesResponse
	_ = json.Unmarshal(recList2.Body.Bytes(), &listResp2)
	if len(listResp2.Messages) != 0 {
		t.Fatalf("expected expired email hidden, got %+v", listResp2.Messages)
	}

	recDetail2 := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recDetail2, reqDetail)
	if recDetail2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for expired email, got %d", recDetail2.Code)
	}

	// 5. Advance past MailboxTTL (20s): token should now return 401.
	now = now.Add(10 * time.Second)
	recList3 := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recList3, reqList)
	if recList3.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired mailbox, got %d", recList3.Code)
	}
}

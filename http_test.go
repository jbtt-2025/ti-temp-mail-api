package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
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

func TestHTTPServer_CORS_Info_And_ExplicitMailboxReapply(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cfg := &Config{
		MailDomains:     []string{"a.example.com", "b.example.com"},
		MXDomain:        "mx.serverhost.net",
		CreateToken:     "secret-create",
		StorageType:     "sqlite",
		DBDSN:           ":memory:",
		MailboxTTL:      15 * time.Second,
		EmailTTL:        30 * time.Second,
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

	// 1. CORS preflight OPTIONS request succeeds with required headers, and GET / renders MX_DOMAIN DNS tutorial.
	homeReq := httptest.NewRequest(http.MethodGet, "/", nil)
	homeRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(homeRec, homeReq)
	if homeRec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d", homeRec.Code)
	}
	homeBody := homeRec.Body.String()
	for _, want := range []string{
		"a.example.com.   MX   10   mx.serverhost.net.",
		"*.a.example.com.   MX   10   mx.serverhost.net.",
	} {
		if !strings.Contains(homeBody, want) {
			t.Fatalf("GET / HTML missing %q", want)
		}
	}

	optReq := httptest.NewRequest(http.MethodOptions, "/mailbox", nil)
	optRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(optRec, optReq)
	if optRec.Code != http.StatusNoContent {
		t.Fatalf("OPTIONS /mailbox status = %d, want 204", optRec.Code)
	}
	if got := optRec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if got := optRec.Header().Get("Access-Control-Allow-Headers"); got == "" {
		t.Fatal("expected non-empty Access-Control-Allow-Headers")
	}

	// 2. GET /info returns configured domains and requiresCreateToken=true.
	infoReq := httptest.NewRequest(http.MethodGet, "/info", nil)
	infoRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(infoRec, infoReq)
	if infoRec.Code != http.StatusOK {
		t.Fatalf("GET /info status = %d", infoRec.Code)
	}
	if got := infoRec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("GET /info CORS header = %q, want *", got)
	}
	var info ServerInfoResponse
	if err := json.Unmarshal(infoRec.Body.Bytes(), &info); err != nil {
		t.Fatalf("decode /info: %v", err)
	}
	if !info.RequiresCreateToken || len(info.Domains) != 2 || info.Domains[0] != "a.example.com" || info.Domains[1] != "b.example.com" {
		t.Fatalf("unexpected /info: %+v", info)
	}

	// 3. Explicit mailbox create on subdomain under configured domain succeeds.
	body := []byte(`{"mailbox":"User1@sub.b.example.com"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/mailbox", bytes.NewReader(body))
	createReq.Header.Set("Authorization", "secret-create")
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("explicit POST /mailbox status = %d, body = %s", createRec.Code, createRec.Body.String())
	}
	var first CreateMailboxResponse
	_ = json.Unmarshal(createRec.Body.Bytes(), &first)
	if first.Mailbox != "user1@sub.b.example.com" || first.Token == "" {
		t.Fatalf("unexpected explicit create response: %+v", first)
	}

	// 4. Re-applying for the same mailbox BEFORE expiration fails with 409 Conflict.
	dupReq := httptest.NewRequest(http.MethodPost, "/mailbox", bytes.NewReader([]byte(`{"mailbox":"user1@sub.b.example.com"}`)))
	dupReq.Header.Set("Authorization", "secret-create")
	dupRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(dupRec, dupReq)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("expected 409 Conflict for non-expired duplicate mailbox, got %d (%s)", dupRec.Code, dupRec.Body.String())
	}

	// 5. Malformed mailbox address is rejected with 400 Bad Request, while valid custom MX domain outside MAIL_DOMAIN is accepted.
	badReq := httptest.NewRequest(http.MethodPost, "/mailbox", bytes.NewReader([]byte(`{"mailbox":"invalid-mailbox"}`)))
	badReq.Header.Set("Authorization", "secret-create")
	badRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for malformed mailbox, got %d", badRec.Code)
	}

	customReq := httptest.NewRequest(http.MethodPost, "/mailbox", bytes.NewReader([]byte(`{"mailbox":"user1@custom-mx.org"}`)))
	customReq.Header.Set("Authorization", "secret-create")
	customRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(customRec, customReq)
	if customRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created for valid custom domain outside MAIL_DOMAIN, got %d (%s)", customRec.Code, customRec.Body.String())
	}

	// 6. After MailboxTTL expires, re-applying yields a fresh token for the same mailbox.
	now = now.Add(20 * time.Second)
	reapplyReq := httptest.NewRequest(http.MethodPost, "/mailbox", bytes.NewReader([]byte(`{"mailbox":"user1@sub.b.example.com"}`)))
	reapplyReq.Header.Set("Authorization", "secret-create")
	reapplyRec := httptest.NewRecorder()
	srv.Handler.ServeHTTP(reapplyRec, reapplyReq)
	if reapplyRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 Created after expiration, got %d (%s)", reapplyRec.Code, reapplyRec.Body.String())
	}
	var second CreateMailboxResponse
	_ = json.Unmarshal(reapplyRec.Body.Bytes(), &second)
	if second.Mailbox != first.Mailbox || second.Token == "" || second.Token == first.Token {
		t.Fatalf("expected fresh token for same mailbox after expiration, first=%+v second=%+v", first, second)
	}
}

func TestHTTPServer_WithCachedD1_ReducesWorkerRequests(t *testing.T) {
	var workerReqs atomic.Int64
	var mu sync.Mutex
	mailboxes := make(map[string]struct {
		token     string
		createdAt int64
	})
	tokens := make(map[string]string)
	emails := make(map[string]*Email)

	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		workerReqs.Add(1)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/mailboxes":
			var body struct {
				Token     string `json:"token"`
				Mailbox   string `json:"mailbox"`
				CreatedAt int64  `json:"createdAt"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
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
			var createdAt *int64
			if ok {
				v := rec.createdAt
				createdAt = &v
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"exists": ok, "createdAt": createdAt})

		case r.Method == http.MethodPost && r.URL.Path == "/mailboxes/by-token":
			var body struct {
				Token string `json:"token"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			addr, ok := tokens[body.Token]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			rec := mailboxes[addr]
			_ = json.NewEncoder(w).Encode(map[string]any{"mailbox": addr, "createdAt": rec.createdAt})

		case r.Method == http.MethodPost && r.URL.Path == "/emails":
			var body Email
			_ = json.NewDecoder(r.Body).Decode(&body)
			copyE := body
			emails[body.ID] = &copyE
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"saved":true}`))

		case r.Method == http.MethodGet && r.URL.Path == "/emails":
			mbox := r.URL.Query().Get("mailbox")
			var list []*Email
			for _, e := range emails {
				if e.Mailbox == mbox {
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
			_ = json.NewEncoder(w).Encode(map[string]any{"email": e})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()

	cfg := &Config{
		MailDomains:         []string{"example.com"},
		StorageType:         "d1",
		D1APIURL:            worker.URL,
		D1APIToken:          "tok",
		MailboxTTL:          time.Hour,
		EmailTTL:            time.Hour,
		CleanupInterval:     time.Hour,
		CacheEnabled:        true,
		CacheMaxMailboxes:   100,
		CacheMaxEmailMemory: 1024 * 1024,
		CacheNegativeTTL:    time.Minute,
	}
	store, err := NewStore(cfg)
	if err != nil {
		t.Fatalf("NewStore(d1 cached) failed: %v", err)
	}
	defer store.Close()

	srv := NewHTTPServer(cfg, store)

	// 1. Create mailbox -> 1 Worker call.
	reqCreate := httptest.NewRequest(http.MethodPost, "/mailbox", nil)
	recCreate := httptest.NewRecorder()
	srv.Handler.ServeHTTP(recCreate, reqCreate)
	if recCreate.Code != http.StatusCreated {
		t.Fatalf("POST /mailbox = %d", recCreate.Code)
	}
	var mbox CreateMailboxResponse
	_ = json.Unmarshal(recCreate.Body.Bytes(), &mbox)

	// 2. Poll empty inbox 10 times -> only 1 initial Worker list call (by-token hits cache!).
	for i := 0; i < 10; i++ {
		reqList := httptest.NewRequest(http.MethodGet, "/messages", nil)
		reqList.Header.Set("Authorization", mbox.Token)
		recList := httptest.NewRecorder()
		srv.Handler.ServeHTTP(recList, reqList)
		if recList.Code != http.StatusOK {
			t.Fatalf("GET /messages = %d", recList.Code)
		}
	}
	// Total so far: 1 (create) + 1 (initial list) = 2 Worker calls instead of 21!
	if got := workerReqs.Load(); got != 2 {
		t.Fatalf("expected 2 Worker requests after create + 10 empty polls, got %d", got)
	}

	// 3. Deliver email via SMTP/Store -> 0 exists calls (cached) + 1 SaveEmail write = 3 total.
	if exists, err := store.MailboxExists(context.Background(), mbox.Mailbox); err != nil || !exists {
		t.Fatalf("MailboxExists=%v err=%v", exists, err)
	}
	_, err = store.SaveEmail(context.Background(), &Email{
		ID:         "msg-cached-1",
		ReceivedAt: time.Now().Unix(),
		Mailbox:    mbox.Mailbox,
		From:       "a@example.com",
		Subject:    "OTP",
		BodyHTML:   "<b>998877</b>",
	})
	if err != nil {
		t.Fatalf("SaveEmail: %v", err)
	}

	// 4. Poll list 5 times + fetch detail 5 times -> 0 extra Worker requests!
	for i := 0; i < 5; i++ {
		reqList := httptest.NewRequest(http.MethodGet, "/messages", nil)
		reqList.Header.Set("Authorization", mbox.Token)
		recList := httptest.NewRecorder()
		srv.Handler.ServeHTTP(recList, reqList)
		if recList.Code != http.StatusOK {
			t.Fatalf("GET /messages = %d", recList.Code)
		}

		reqDet := httptest.NewRequest(http.MethodGet, "/messages/msg-cached-1", nil)
		reqDet.Header.Set("Authorization", mbox.Token)
		recDet := httptest.NewRecorder()
		srv.Handler.ServeHTTP(recDet, reqDet)
		if recDet.Code != http.StatusOK {
			t.Fatalf("GET /messages/msg-cached-1 = %d", recDet.Code)
		}
	}

	// Expected total Worker calls across 31+ HTTP/SMTP ops: 1 (create) + 1 (initial list) + 1 (save) = 3!
	if got := workerReqs.Load(); got != 3 {
		t.Fatalf("expected exactly 3 total Worker HTTP requests, got %d", got)
	}
}

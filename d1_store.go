package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// D1Store persists mailboxes and emails through a Cloudflare Worker backed by D1.
type D1Store struct {
	baseURL    string
	token      string
	client     *http.Client
	mailboxTTL time.Duration
	emailTTL   time.Duration
	nowFn      func() time.Time
}

func NewD1Store(cfg *Config) (*D1Store, error) {
	if cfg.D1APIURL == "" {
		return nil, fmt.Errorf("D1_API_URL is required")
	}
	if cfg.D1APIToken == "" {
		return nil, fmt.Errorf("D1_API_TOKEN is required")
	}
	return &D1Store{
		baseURL:    cfg.D1APIURL,
		token:      cfg.D1APIToken,
		client:     &http.Client{Timeout: 15 * time.Second},
		mailboxTTL: cfg.MailboxTTL,
		emailTTL:   cfg.EmailTTL,
		nowFn:      time.Now,
	}, nil
}

func (s *D1Store) doJSON(ctx context.Context, method, path string, reqBody, respBody any) (int, error) {
	var bodyReader io.Reader
	if reqBody != nil {
		payload, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		bodyReader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, s.baseURL+path, bodyReader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024*1024))
	if err != nil {
		return resp.StatusCode, err
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 && respBody != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, respBody); err != nil {
			return resp.StatusCode, fmt.Errorf("decode worker response (%s %s): %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (s *D1Store) CreateMailbox(ctx context.Context, token, mailbox string) (bool, error) {
	now := s.nowFn()
	payload := map[string]any{
		"token":     token,
		"mailbox":   mailbox,
		"createdAt": now.Unix(),
	}
	if cutoff, ok := cutoffUnix(now, s.mailboxTTL); ok {
		payload["expiredBefore"] = cutoff
	}
	status, err := s.doJSON(ctx, http.MethodPost, "/mailboxes", payload, nil)
	if err != nil {
		return false, err
	}
	if status == http.StatusConflict {
		return false, nil
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return false, fmt.Errorf("d1 worker CreateMailbox returned status %d", status)
	}
	return true, nil
}

func (s *D1Store) MailboxExists(ctx context.Context, mailbox string) (bool, error) {
	var res struct {
		Exists bool `json:"exists"`
	}
	path := "/mailboxes/exists?address=" + url.QueryEscape(mailbox)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.mailboxTTL); ok {
		path += "&minCreatedAt=" + strconv.FormatInt(cutoff, 10)
	}
	status, err := s.doJSON(ctx, http.MethodGet, path, nil, &res)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK {
		return false, fmt.Errorf("d1 worker MailboxExists returned status %d", status)
	}
	return res.Exists, nil
}

func (s *D1Store) GetMailboxByToken(ctx context.Context, token string) (string, bool, error) {
	var res struct {
		Mailbox string `json:"mailbox"`
	}
	payload := map[string]any{"token": token}
	if cutoff, ok := cutoffUnix(s.nowFn(), s.mailboxTTL); ok {
		payload["minCreatedAt"] = cutoff
	}
	status, err := s.doJSON(ctx, http.MethodPost, "/mailboxes/by-token", payload, &res)
	if err != nil {
		return "", false, err
	}
	if status == http.StatusNotFound {
		return "", false, nil
	}
	if status != http.StatusOK {
		return "", false, fmt.Errorf("d1 worker GetMailboxByToken returned status %d", status)
	}
	return res.Mailbox, true, nil
}

func (s *D1Store) SaveEmail(ctx context.Context, email *Email) (bool, error) {
	if email == nil {
		return false, nil
	}
	payload := map[string]any{
		"id":               email.ID,
		"mailbox":          email.Mailbox,
		"receivedAt":       email.ReceivedAt,
		"from":             email.From,
		"subject":          email.Subject,
		"bodyHtml":         email.BodyHTML,
		"attachmentsCount": email.AttachmentsCount,
	}
	status, err := s.doJSON(ctx, http.MethodPost, "/emails", payload, nil)
	if err != nil {
		return false, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return false, fmt.Errorf("d1 worker SaveEmail returned status %d", status)
	}
	return true, nil
}

func (s *D1Store) ListEmailsByMailbox(ctx context.Context, mailbox string) ([]*Email, error) {
	var res struct {
		Emails []*Email `json:"emails"`
	}
	path := "/emails?mailbox=" + url.QueryEscape(mailbox)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.emailTTL); ok {
		path += "&minReceivedAt=" + strconv.FormatInt(cutoff, 10)
	}
	status, err := s.doJSON(ctx, http.MethodGet, path, nil, &res)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("d1 worker ListEmailsByMailbox returned status %d", status)
	}
	if res.Emails == nil {
		return []*Email{}, nil
	}
	return res.Emails, nil
}

func (s *D1Store) GetEmailByID(ctx context.Context, id string) (*Email, bool, error) {
	var res struct {
		Email *Email `json:"email"`
	}
	path := "/emails/" + url.PathEscape(id)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.emailTTL); ok {
		path += "?minReceivedAt=" + strconv.FormatInt(cutoff, 10)
	}
	status, err := s.doJSON(ctx, http.MethodGet, path, nil, &res)
	if err != nil {
		return nil, false, err
	}
	if status == http.StatusNotFound {
		return nil, false, nil
	}
	if status != http.StatusOK || res.Email == nil {
		return nil, false, fmt.Errorf("d1 worker GetEmailByID returned status %d", status)
	}
	return res.Email, true, nil
}

func (s *D1Store) CleanupExpired(ctx context.Context, now time.Time) (int64, int64, error) {
	payload := map[string]any{}
	if cutoff, ok := cutoffUnix(now, s.emailTTL); ok {
		payload["emailBefore"] = cutoff
	}
	if cutoff, ok := cutoffUnix(now, s.mailboxTTL); ok {
		payload["mailboxBefore"] = cutoff
	}
	if len(payload) == 0 {
		return 0, 0, nil
	}
	var res struct {
		DeletedEmails    int64 `json:"deletedEmails"`
		DeletedMailboxes int64 `json:"deletedMailboxes"`
	}
	status, err := s.doJSON(ctx, http.MethodPost, "/cleanup", payload, &res)
	if err != nil {
		return 0, 0, err
	}
	if status != http.StatusOK {
		return 0, 0, fmt.Errorf("d1 worker CleanupExpired returned status %d", status)
	}
	return res.DeletedEmails, res.DeletedMailboxes, nil
}

func (s *D1Store) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

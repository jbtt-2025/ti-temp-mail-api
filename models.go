package main

// Email stored in Email_Store
type Email struct {
	ID               string `json:"_id"`
	ReceivedAt       int64  `json:"receivedAt"` // Unix timestamp
	Mailbox          string `json:"mailbox"`
	From             string `json:"from"`
	Subject          string `json:"subject"`
	BodyText         string `json:"-"`
	BodyHTML         string `json:"bodyHtml"`
	AttachmentsCount int    `json:"attachmentsCount"`
}

// bodyPreview truncates html to 100 runes, appending "..." if truncated.
func bodyPreview(html string) string {
	r := []rune(html)
	if len(r) > 100 {
		return string(r[:100]) + "..."
	}
	return html
}

// CreateMailboxResponse is returned by POST /mailbox
type CreateMailboxResponse struct {
	Token   string `json:"token"`
	Mailbox string `json:"mailbox"`
}

// ListMessagesResponse is returned by GET /messages
type ListMessagesResponse struct {
	Mailbox  string           `json:"mailbox"`
	Messages []MessageSummary `json:"messages"`
}

// MessageSummary is a single entry in ListMessagesResponse
type MessageSummary struct {
	ID               string `json:"_id"`
	ReceivedAt       int64  `json:"receivedAt"`
	From             string `json:"from"`
	Subject          string `json:"subject"`
	BodyPreview      string `json:"bodyPreview"`
	AttachmentsCount int    `json:"attachmentsCount"`
}

// MessageDetail is returned by GET /messages/{id}
type MessageDetail struct {
	ID               string        `json:"_id"`
	ReceivedAt       int64         `json:"receivedAt"`
	Mailbox          string        `json:"mailbox"`
	From             string        `json:"from"`
	Subject          string        `json:"subject"`
	BodyPreview      string        `json:"bodyPreview"`
	BodyHTML         string        `json:"bodyHtml"`
	AttachmentsCount int           `json:"attachmentsCount"`
	Attachments      []interface{} `json:"attachments"`
}

// ErrorResponse is used for all error responses
type ErrorResponse struct {
	Error string `json:"error"`
}

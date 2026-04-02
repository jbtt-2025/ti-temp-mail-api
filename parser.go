package main

import (
	"bytes"

	"github.com/jhillyerd/enmime"
)

// ParsedEmail holds the extracted fields from a raw MIME email.
type ParsedEmail struct {
	From             string
	Subject          string
	BodyText         string
	BodyHTML         string
	AttachmentsCount int
}

// ParseMIME parses a raw MIME email and returns a ParsedEmail.
// If parsing fails, it returns a partial result alongside the error so callers
// can still use whatever fields were successfully extracted.
func ParseMIME(rawData []byte) (*ParsedEmail, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(rawData))
	if err != nil {
		return &ParsedEmail{}, err
	}

	return &ParsedEmail{
		From:             env.GetHeader("From"),
		Subject:          env.GetHeader("Subject"),
		BodyText:         env.Text,
		BodyHTML:         env.HTML,
		AttachmentsCount: len(env.Attachments),
	}, nil
}

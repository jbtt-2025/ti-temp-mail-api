CREATE TABLE IF NOT EXISTS mailboxes (
  mailbox TEXT PRIMARY KEY,
  token TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_mailboxes_created_at ON mailboxes (created_at);

CREATE TABLE IF NOT EXISTS emails (
  id TEXT PRIMARY KEY,
  mailbox TEXT NOT NULL,
  received_at INTEGER NOT NULL,
  from_addr TEXT NOT NULL DEFAULT '',
  subject TEXT NOT NULL DEFAULT '',
  body_html TEXT NOT NULL DEFAULT '',
  attachments_count INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_emails_mailbox_received ON emails (mailbox, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_emails_received_at ON emails (received_at);

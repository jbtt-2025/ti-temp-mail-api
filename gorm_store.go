package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// MailboxRecord stores allocated mailbox addresses, tokens, and creation timestamps.
// Expiration is evaluated dynamically from CreatedAt + configured MailboxTTL.
type MailboxRecord struct {
	Mailbox   string `gorm:"column:mailbox;primaryKey;size:191"`
	Token     string `gorm:"column:token;uniqueIndex;size:64;not null"`
	CreatedAt int64  `gorm:"column:created_at;index;not null"`
}

func (MailboxRecord) TableName() string {
	return "mailboxes"
}

// EmailRecord stores parsed email payloads indexed by mailbox and receipt timestamp.
// Expiration is evaluated dynamically from ReceivedAt + configured EmailTTL.
type EmailRecord struct {
	ID               string `gorm:"column:id;primaryKey;size:64"`
	Mailbox          string `gorm:"column:mailbox;index:idx_emails_mailbox_received,priority:1;size:191;not null"`
	ReceivedAt       int64  `gorm:"column:received_at;index:idx_emails_mailbox_received,priority:2;index:idx_emails_received_at;not null"`
	FromAddr         string `gorm:"column:from_addr;not null"`
	Subject          string `gorm:"column:subject;not null"`
	BodyHTML         string `gorm:"column:body_html;not null"`
	AttachmentsCount int    `gorm:"column:attachments_count;not null"`
}

func (EmailRecord) TableName() string {
	return "emails"
}

func (r EmailRecord) toEmail() *Email {
	return &Email{
		ID:               r.ID,
		ReceivedAt:       r.ReceivedAt,
		Mailbox:          r.Mailbox,
		From:             r.FromAddr,
		Subject:          r.Subject,
		BodyHTML:         r.BodyHTML,
		AttachmentsCount: r.AttachmentsCount,
	}
}

type GormStore struct {
	db         *gorm.DB
	mailboxTTL time.Duration
	emailTTL   time.Duration
	nowFn      func() time.Time
}

func NewGormStore(cfg *Config) (*GormStore, error) {
	dialector, err := buildDialector(cfg)
	if err != nil {
		return nil, err
	}

	db, err := gorm.Open(dialector, &gorm.Config{
		TranslateError: true,
		Logger:         logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("open %s database: %w", cfg.StorageType, err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("get underlying sql.DB: %w", err)
	}

	if cfg.StorageType == "sqlite" {
		sqlDB.SetMaxOpenConns(1)
		db.Exec("PRAGMA journal_mode=WAL;")
		db.Exec("PRAGMA busy_timeout=5000;")
	} else {
		sqlDB.SetMaxOpenConns(25)
		sqlDB.SetMaxIdleConns(5)
		sqlDB.SetConnMaxLifetime(time.Hour)
	}

	if err := db.AutoMigrate(&MailboxRecord{}, &EmailRecord{}); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("auto migrate %s schema: %w", cfg.StorageType, err)
	}

	return &GormStore{
		db:         db,
		mailboxTTL: cfg.MailboxTTL,
		emailTTL:   cfg.EmailTTL,
		nowFn:      time.Now,
	}, nil
}

func buildDialector(cfg *Config) (gorm.Dialector, error) {
	switch cfg.StorageType {
	case "sqlite":
		dsn := cfg.DBDSN
		if dsn != ":memory:" && !strings.HasPrefix(dsn, "file:") {
			if dir := filepath.Dir(dsn); dir != "" && dir != "." {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return nil, fmt.Errorf("create sqlite directory: %w", err)
				}
			}
		}
		return sqlite.Open(dsn), nil
	case "mysql":
		return mysql.Open(cfg.DBDSN), nil
	case "postgres":
		return postgres.Open(cfg.DBDSN), nil
	default:
		return nil, fmt.Errorf("unsupported gorm storage type: %s", cfg.StorageType)
	}
}

func cutoffUnix(now time.Time, ttl time.Duration) (int64, bool) {
	if ttl <= 0 {
		return 0, false
	}
	return now.Add(-ttl).Unix(), true
}

func (s *GormStore) CreateMailbox(ctx context.Context, token, mailbox string) (bool, error) {
	now := s.nowFn()
	rec := &MailboxRecord{
		Mailbox:   mailbox,
		Token:     token,
		CreatedAt: now.Unix(),
	}
	cutoff, hasTTL := cutoffUnix(now, s.mailboxTTL)

	var created bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Remove an expired record for this address so it can be re-allocated, while preserving active records.
		if hasTTL {
			if err := tx.Where("mailbox = ? AND created_at <= ?", mailbox, cutoff).Delete(&MailboxRecord{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(rec).Error; err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				created = false
				return nil
			}
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return created, nil
}

func (s *GormStore) MailboxExists(ctx context.Context, mailbox string) (bool, error) {
	q := s.db.WithContext(ctx).Model(&MailboxRecord{}).Where("mailbox = ?", mailbox)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.mailboxTTL); ok {
		q = q.Where("created_at > ?", cutoff)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (s *GormStore) GetMailboxByToken(ctx context.Context, token string) (string, bool, error) {
	q := s.db.WithContext(ctx).Select("mailbox").Where("token = ?", token)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.mailboxTTL); ok {
		q = q.Where("created_at > ?", cutoff)
	}
	var rec MailboxRecord
	err := q.Take(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return rec.Mailbox, true, nil
}

func (s *GormStore) SaveEmail(ctx context.Context, email *Email) (bool, error) {
	if email == nil {
		return false, nil
	}
	rec := &EmailRecord{
		ID:               email.ID,
		Mailbox:          email.Mailbox,
		ReceivedAt:       email.ReceivedAt,
		FromAddr:         email.From,
		Subject:          email.Subject,
		BodyHTML:         email.BodyHTML,
		AttachmentsCount: email.AttachmentsCount,
	}
	if err := s.db.WithContext(ctx).Create(rec).Error; err != nil {
		return false, err
	}
	return true, nil
}

func (s *GormStore) ListEmailsByMailbox(ctx context.Context, mailbox string) ([]*Email, error) {
	q := s.db.WithContext(ctx).Where("mailbox = ?", mailbox)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.emailTTL); ok {
		q = q.Where("received_at > ?", cutoff)
	}
	var recs []EmailRecord
	if err := q.Order("received_at DESC, id DESC").Find(&recs).Error; err != nil {
		return nil, err
	}
	emails := make([]*Email, len(recs))
	for i := range recs {
		emails[i] = recs[i].toEmail()
	}
	return emails, nil
}

func (s *GormStore) GetEmailByID(ctx context.Context, id string) (*Email, bool, error) {
	q := s.db.WithContext(ctx).Where("id = ?", id)
	if cutoff, ok := cutoffUnix(s.nowFn(), s.emailTTL); ok {
		q = q.Where("received_at > ?", cutoff)
	}
	var rec EmailRecord
	err := q.Take(&rec).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return rec.toEmail(), true, nil
}

func (s *GormStore) CleanupExpired(ctx context.Context, now time.Time) (int64, int64, error) {
	var deletedEmails, deletedMailboxes int64

	if cutoff, ok := cutoffUnix(now, s.emailTTL); ok {
		res := s.db.WithContext(ctx).
			Where("received_at <= ?", cutoff).
			Delete(&EmailRecord{})
		if res.Error != nil {
			return 0, 0, res.Error
		}
		deletedEmails = res.RowsAffected
	}

	if cutoff, ok := cutoffUnix(now, s.mailboxTTL); ok {
		res := s.db.WithContext(ctx).
			Where("created_at <= ?", cutoff).
			Delete(&MailboxRecord{})
		if res.Error != nil {
			return deletedEmails, 0, res.Error
		}
		deletedMailboxes = res.RowsAffected
	}

	return deletedEmails, deletedMailboxes, nil
}

func (s *GormStore) Close() error {
	sqlDB, err := s.db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

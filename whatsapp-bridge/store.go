package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// MessageStore wraps messages.db, the local history the MCP server reads.
type MessageStore struct {
	db *sql.DB
}

// StoredMessage is one row of the messages table.
type StoredMessage struct {
	ID            string
	ChatJID       string
	Sender        string
	SenderName    string
	Content       string
	Timestamp     time.Time
	IsFromMe      bool
	MediaType     string
	Filename      string
	URL           string
	MediaKey      []byte
	FileSHA256    []byte
	FileEncSHA256 []byte
	FileLength    uint64
	QuotedID      string
}

// ScheduledMessage is a message the bridge sends later on its own.
type ScheduledMessage struct {
	ID        int64      `json:"id"`
	Recipient string     `json:"recipient"`
	Message   string     `json:"message"`
	MediaPath string     `json:"media_path,omitempty"`
	ReplyTo   string     `json:"reply_to,omitempty"`
	SendAt    time.Time  `json:"send_at"`
	Status    string     `json:"status"`
	Error     string     `json:"error,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	SentAt    *time.Time `json:"sent_at,omitempty"`
}

const baseSchema = `
CREATE TABLE IF NOT EXISTS chats (
	jid TEXT PRIMARY KEY,
	name TEXT,
	last_message_time TIMESTAMP
);
CREATE TABLE IF NOT EXISTS messages (
	id TEXT,
	chat_jid TEXT,
	sender TEXT,
	content TEXT,
	timestamp TIMESTAMP,
	is_from_me BOOLEAN,
	media_type TEXT,
	filename TEXT,
	url TEXT,
	media_key BLOB,
	file_sha256 BLOB,
	file_enc_sha256 BLOB,
	file_length INTEGER,
	PRIMARY KEY (id, chat_jid),
	FOREIGN KEY (chat_jid) REFERENCES chats(jid)
);
CREATE TABLE IF NOT EXISTS contacts (
	jid TEXT PRIMARY KEY,
	name TEXT,
	push_name TEXT,
	updated_at TIMESTAMP
);
CREATE TABLE IF NOT EXISTS reactions (
	chat_jid TEXT,
	message_id TEXT,
	sender TEXT,
	emoji TEXT,
	timestamp TIMESTAMP,
	PRIMARY KEY (chat_jid, message_id, sender)
);
CREATE TABLE IF NOT EXISTS scheduled_messages (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	recipient TEXT NOT NULL,
	message TEXT,
	media_path TEXT,
	reply_to TEXT,
	send_at TIMESTAMP NOT NULL,
	status TEXT NOT NULL DEFAULT 'pending',
	error TEXT,
	created_at TIMESTAMP NOT NULL,
	sent_at TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages(chat_jid, timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_time ON messages(timestamp);
CREATE INDEX IF NOT EXISTS idx_messages_sender ON messages(sender);
CREATE INDEX IF NOT EXISTS idx_scheduled_due ON scheduled_messages(status, send_at);
`

// columns added after the original schema; added in place so existing
// databases from older versions keep working.
var addedMessageColumns = map[string]string{
	"quoted_id":   "TEXT",
	"sender_name": "TEXT",
}

// NewMessageStore opens (and migrates) <dir>/messages.db.
func NewMessageStore(dir string) (*MessageStore, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}
	dsn := "file:" + filepath.ToSlash(filepath.Join(dir, "messages.db")) + "?_foreign_keys=on&_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open message database: %w", err)
	}
	store := &MessageStore{db: db}
	if err := store.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

func (s *MessageStore) migrate() error {
	if _, err := s.db.Exec(baseSchema); err != nil {
		return fmt.Errorf("failed to create tables: %w", err)
	}
	existing := map[string]bool{}
	rows, err := s.db.Query("PRAGMA table_info(messages)")
	if err != nil {
		return err
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return err
		}
		existing[name] = true
	}
	rows.Close()
	for col, typ := range addedMessageColumns {
		if !existing[col] {
			if _, err := s.db.Exec(fmt.Sprintf("ALTER TABLE messages ADD COLUMN %s %s", col, typ)); err != nil {
				return fmt.Errorf("failed to add column %s: %w", col, err)
			}
		}
	}
	return nil
}

// Close closes the database.
func (s *MessageStore) Close() error { return s.db.Close() }

// ChatName returns the stored name of a chat, or "" if unknown.
func (s *MessageStore) ChatName(jid string) string {
	var name sql.NullString
	if err := s.db.QueryRow("SELECT name FROM chats WHERE jid = ?", jid).Scan(&name); err != nil {
		return ""
	}
	return name.String
}

// StoreChat inserts or updates a chat. The name is only replaced by a non-empty
// value and last_message_time only ever moves forward, so replaying old history
// cannot make a chat look older than it is.
func (s *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	var existingName sql.NullString
	var existingTime sql.NullTime
	err := s.db.QueryRow("SELECT name, last_message_time FROM chats WHERE jid = ?", jid).Scan(&existingName, &existingTime)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.Exec("INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", jid, name, lastMessageTime)
		return err
	}
	if err != nil {
		return err
	}
	if name == "" {
		name = existingName.String
	}
	t := lastMessageTime
	if existingTime.Valid && existingTime.Time.After(t) {
		t = existingTime.Time
	}
	_, err = s.db.Exec("UPDATE chats SET name = ?, last_message_time = ? WHERE jid = ?", name, t, jid)
	return err
}

// RenameChat changes a chat's name (group subject changes, contact renames).
func (s *MessageStore) RenameChat(jid, name string) error {
	if name == "" {
		return nil
	}
	_, err := s.db.Exec("UPDATE chats SET name = ? WHERE jid = ?", name, jid)
	return err
}

// StoreMessage saves a message. Messages without text and without media are skipped.
func (s *MessageStore) StoreMessage(m StoredMessage) error {
	if m.Content == "" && m.MediaType == "" {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length, quoted_id, sender_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.ChatJID, m.Sender, m.Content, m.Timestamp, m.IsFromMe, m.MediaType, m.Filename, m.URL,
		m.MediaKey, m.FileSHA256, m.FileEncSHA256, m.FileLength, nullIfEmpty(m.QuotedID), nullIfEmpty(m.SenderName),
	)
	return err
}

// UpdateMessageContent applies an edit made in WhatsApp to a stored message.
func (s *MessageStore) UpdateMessageContent(chatJID, id, content string) error {
	_, err := s.db.Exec("UPDATE messages SET content = ? WHERE id = ? AND chat_jid = ?", content, id, chatJID)
	return err
}

// MarkMessageDeleted replaces the text of a message that was deleted for everyone.
func (s *MessageStore) MarkMessageDeleted(chatJID, id string) error {
	_, err := s.db.Exec("UPDATE messages SET content = '[message deleted]' WHERE id = ? AND chat_jid = ?", id, chatJID)
	return err
}

// GetMessage loads one message with its media info.
func (s *MessageStore) GetMessage(chatJID, id string) (StoredMessage, error) {
	var m StoredMessage
	var mediaType, filename, url, quoted, sender, content sql.NullString
	var length sql.NullInt64
	err := s.db.QueryRow(
		`SELECT id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length, quoted_id
		FROM messages WHERE id = ? AND chat_jid = ?`, id, chatJID,
	).Scan(&m.ID, &m.ChatJID, &sender, &content, &m.Timestamp, &m.IsFromMe, &mediaType, &filename, &url,
		&m.MediaKey, &m.FileSHA256, &m.FileEncSHA256, &length, &quoted)
	if err != nil {
		return m, err
	}
	m.Sender, m.Content = sender.String, content.String
	m.MediaType, m.Filename, m.URL, m.QuotedID = mediaType.String, filename.String, url.String, quoted.String
	if length.Valid && length.Int64 > 0 {
		m.FileLength = uint64(length.Int64)
	}
	return m, nil
}

// UpsertContact stores the best known display name for a user.
func (s *MessageStore) UpsertContact(jid, name, pushName string) error {
	if jid == "" || (name == "" && pushName == "") {
		return nil
	}
	_, err := s.db.Exec(`INSERT INTO contacts (jid, name, push_name, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
			name = COALESCE(NULLIF(excluded.name, ''), contacts.name),
			push_name = COALESCE(NULLIF(excluded.push_name, ''), contacts.push_name),
			updated_at = excluded.updated_at`,
		jid, name, pushName, time.Now())
	return err
}

// StoreReaction saves (or removes, for an empty emoji) a reaction.
func (s *MessageStore) StoreReaction(chatJID, messageID, sender, emoji string, ts time.Time) error {
	if emoji == "" {
		_, err := s.db.Exec("DELETE FROM reactions WHERE chat_jid = ? AND message_id = ? AND sender = ?", chatJID, messageID, sender)
		return err
	}
	_, err := s.db.Exec("INSERT OR REPLACE INTO reactions (chat_jid, message_id, sender, emoji, timestamp) VALUES (?, ?, ?, ?, ?)",
		chatJID, messageID, sender, emoji, ts)
	return err
}

// Stats returns simple counters for the dashboard.
func (s *MessageStore) Stats() (chats, messages, contacts int64, last *time.Time) {
	s.db.QueryRow("SELECT COUNT(*) FROM chats").Scan(&chats)
	s.db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&messages)
	s.db.QueryRow("SELECT COUNT(*) FROM contacts").Scan(&contacts)
	var t sql.NullTime
	if err := s.db.QueryRow("SELECT MAX(last_message_time) FROM chats").Scan(&t); err == nil && t.Valid {
		last = &t.Time
	} else {
		// MAX() over TIMESTAMP columns comes back as text with go-sqlite3.
		var raw sql.NullString
		if err := s.db.QueryRow("SELECT MAX(last_message_time) FROM chats").Scan(&raw); err == nil && raw.Valid {
			if parsed, ok := parseSQLiteTime(raw.String); ok {
				last = &parsed
			}
		}
	}
	return
}

// --- scheduled messages ---

// AddScheduled stores a new pending scheduled message.
func (s *MessageStore) AddScheduled(m ScheduledMessage) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO scheduled_messages (recipient, message, media_path, reply_to, send_at, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'pending', ?)`, m.Recipient, m.Message, nullIfEmpty(m.MediaPath), nullIfEmpty(m.ReplyTo), m.SendAt.UTC(), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ListScheduled returns scheduled messages, newest send time last. Empty status means all.
func (s *MessageStore) ListScheduled(status string) ([]ScheduledMessage, error) {
	q := `SELECT id, recipient, COALESCE(message,''), COALESCE(media_path,''), COALESCE(reply_to,''), send_at, status, COALESCE(error,''), created_at, sent_at
		FROM scheduled_messages`
	var args []any
	if status != "" {
		q += " WHERE status = ?"
		args = append(args, status)
	}
	q += " ORDER BY send_at ASC LIMIT 500"
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ScheduledMessage{}
	for rows.Next() {
		var m ScheduledMessage
		var sent sql.NullTime
		if err := rows.Scan(&m.ID, &m.Recipient, &m.Message, &m.MediaPath, &m.ReplyTo, &m.SendAt, &m.Status, &m.Error, &m.CreatedAt, &sent); err != nil {
			return nil, err
		}
		if sent.Valid {
			t := sent.Time
			m.SentAt = &t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DueScheduled returns pending messages whose send time has passed.
func (s *MessageStore) DueScheduled(now time.Time) ([]ScheduledMessage, error) {
	all, err := s.ListScheduled("pending")
	if err != nil {
		return nil, err
	}
	due := []ScheduledMessage{}
	for _, m := range all {
		if !m.SendAt.After(now) {
			due = append(due, m)
		}
	}
	return due, nil
}

// FinishScheduled records the outcome of a send attempt.
func (s *MessageStore) FinishScheduled(id int64, sendErr error) error {
	if sendErr != nil {
		_, err := s.db.Exec("UPDATE scheduled_messages SET status = 'failed', error = ? WHERE id = ?", sendErr.Error(), id)
		return err
	}
	_, err := s.db.Exec("UPDATE scheduled_messages SET status = 'sent', sent_at = ?, error = NULL WHERE id = ?", time.Now().UTC(), id)
	return err
}

// CancelScheduled cancels a pending message. It returns false if nothing was pending under that id.
func (s *MessageStore) CancelScheduled(id int64) (bool, error) {
	res, err := s.db.Exec("UPDATE scheduled_messages SET status = 'cancelled' WHERE id = ? AND status = 'pending'", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// parseSQLiteTime parses the text formats go-sqlite3 writes for time.Time values.
func parseSQLiteTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	layouts := []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

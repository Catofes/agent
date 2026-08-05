package store

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = sql.ErrNoRows

type Store struct{ db *sql.DB }

type Run struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"`
	Locked    bool       `json:"locked"`
	CreatedAt time.Time  `json:"created_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

type Student struct {
	RunID     string    `json:"-"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type Session struct {
	TokenHash  string
	RunID      string
	StudentID  string
	IsTeacher  bool
	ExpiresAt  time.Time
	CreatedAt  time.Time
	LastSeenAt time.Time
	RevokedAt  *time.Time
}

type Design struct {
	RunID     string    `json:"-"`
	StudentID string    `json:"-"`
	Persona   string    `json:"persona"`
	SkillMD   string    `json:"skill_md"`
	Tools     []string  `json:"tools"`
	MaxTurns  int       `json:"max_turns"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Conversation struct {
	ID           string    `json:"id"`
	RunID        string    `json:"-"`
	StudentID    string    `json:"-"`
	Title        string    `json:"title"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type Message struct {
	ID             int64     `json:"id"`
	RunID          string    `json:"-"`
	StudentID      string    `json:"-"`
	ConversationID string    `json:"conversation_id"`
	TurnID         string    `json:"turn_id"`
	Role           string    `json:"role"`
	Content        string    `json:"content"`
	ToolCalls      string    `json:"tool_calls,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Usage struct {
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	Images        int64   `json:"images"`
	Searches      int64   `json:"searches"`
	EstimatedCost float64 `json:"estimated_cost"`
}

type WallStudent struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	LoggedIn   bool       `json:"logged_in"`
	HasPersona bool       `json:"has_persona"`
	HasSkill   bool       `json:"has_skill"`
	ChatTurns  int        `json:"chat_turns"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
}

func Open(path string) (*Store, error) {
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(1)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(5000)")
	db, err := sql.Open("sqlite", "file:"+path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)
	for _, q := range []string{
		"PRAGMA foreign_keys = ON",
		"PRAGMA journal_mode = WAL",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(q); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite setup: %w", err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error                   { return s.db.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

func (s *Store) migrate(ctx context.Context) error {
	var version int
	if err := s.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read database version: %w", err)
	}
	if version > 2 {
		return fmt.Errorf("database version %d is newer than supported version 2", version)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS runs(
 id TEXT PRIMARY KEY, name TEXT NOT NULL, status TEXT NOT NULL CHECK(status IN ('active','ended')),
 locked INTEGER NOT NULL DEFAULT 0 CHECK(locked IN (0,1)), created_at TEXT NOT NULL, ended_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS one_active_run ON runs(status) WHERE status='active';
CREATE TABLE IF NOT EXISTS students(
 run_id TEXT NOT NULL REFERENCES runs(id), id TEXT NOT NULL, name TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,id)
);
CREATE TABLE IF NOT EXISTS sessions(
 token TEXT PRIMARY KEY, run_id TEXT, student_id TEXT, is_teacher INTEGER NOT NULL DEFAULT 0 CHECK(is_teacher IN (0,1)),
 expires_at TEXT NOT NULL, created_at TEXT NOT NULL, last_seen_at TEXT NOT NULL, revoked_at TEXT,
 FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
);
CREATE INDEX IF NOT EXISTS sessions_student ON sessions(run_id,student_id,is_teacher);
CREATE TABLE IF NOT EXISTS designs(
 run_id TEXT NOT NULL, student_id TEXT NOT NULL, persona TEXT NOT NULL DEFAULT '', skill_md TEXT NOT NULL DEFAULT '',
 tools TEXT NOT NULL DEFAULT '[]', max_turns INTEGER NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
);
CREATE TABLE IF NOT EXISTS messages(
 id INTEGER PRIMARY KEY AUTOINCREMENT, run_id TEXT NOT NULL, student_id TEXT NOT NULL, turn_id TEXT NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('user','assistant','tool')), content TEXT NOT NULL DEFAULT '', tool_calls TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL,
 FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
);
CREATE INDEX IF NOT EXISTS messages_student_turn ON messages(run_id,student_id,turn_id,id);
CREATE TABLE IF NOT EXISTS usage(
 run_id TEXT NOT NULL, student_id TEXT NOT NULL, tokens_in INTEGER NOT NULL DEFAULT 0 CHECK(tokens_in>=0),
 tokens_out INTEGER NOT NULL DEFAULT 0 CHECK(tokens_out>=0), images INTEGER NOT NULL DEFAULT 0 CHECK(images>=0),
 searches INTEGER NOT NULL DEFAULT 0 CHECK(searches>=0), estimated_cost REAL NOT NULL DEFAULT 0 CHECK(estimated_cost>=0),
 PRIMARY KEY(run_id,student_id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
);
PRAGMA user_version = 1;`
	if version == 0 {
		if _, err := s.db.ExecContext(ctx, schema); err != nil {
			return fmt.Errorf("migrate database to version 1: %w", err)
		}
		version = 1
	}
	if version < 2 {
		if err := s.migrateConversations(ctx); err != nil {
			return fmt.Errorf("migrate conversations: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateConversations(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(messages)`)
	if err != nil {
		return err
	}
	hasConversationID := false
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			rows.Close()
			return err
		}
		if name == "conversation_id" {
			hasConversationID = true
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversations(
 id TEXT NOT NULL, run_id TEXT NOT NULL, student_id TEXT NOT NULL, title TEXT NOT NULL DEFAULT '新对话',
 created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id,id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`); err != nil {
		return err
	}
	if !hasConversationID {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN conversation_id TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO conversations(id,run_id,student_id,title,created_at,updated_at)
 SELECT 'legacy_' || min(id),run_id,student_id,'历史对话',min(created_at),max(created_at)
 FROM messages WHERE conversation_id='' GROUP BY run_id,student_id`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET conversation_id=(
 SELECT c.id FROM conversations c WHERE c.run_id=messages.run_id AND c.student_id=messages.student_id AND c.title='历史对话'
 ORDER BY c.created_at LIMIT 1) WHERE conversation_id=''`); err != nil {
		return err
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS conversations_student_updated ON conversations(run_id,student_id,updated_at DESC,id)`,
		`CREATE INDEX IF NOT EXISTS messages_conversation ON messages(run_id,student_id,conversation_id,id)`,
		`PRAGMA user_version = 2`,
	} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EnsureActiveRun(ctx context.Context, id, name string) (Run, error) {
	r, err := s.ActiveRun(ctx)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Run{}, err
	}
	now := time.Now().UTC()
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id,name,status,locked,created_at) VALUES(?,?,'active',0,?)`, id, name, formatTime(now))
	if err != nil {
		return Run{}, err
	}
	return Run{ID: id, Name: name, Status: "active", CreatedAt: now}, nil
}

func (s *Store) ActiveRun(ctx context.Context) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT id,name,status,locked,created_at,ended_at FROM runs WHERE status='active' LIMIT 1`))
}

func (s *Store) Run(ctx context.Context, id string) (Run, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT id,name,status,locked,created_at,ended_at FROM runs WHERE id=?`, id))
}

type rowScanner interface{ Scan(...any) error }

func scanRun(row rowScanner) (Run, error) {
	var r Run
	var created string
	var ended sql.NullString
	err := row.Scan(&r.ID, &r.Name, &r.Status, &r.Locked, &created, &ended)
	if err != nil {
		return Run{}, err
	}
	r.CreatedAt, err = parseTime(created)
	if err != nil {
		return Run{}, err
	}
	if ended.Valid {
		t, e := parseTime(ended.String)
		if e != nil {
			return Run{}, e
		}
		r.EndedAt = &t
	}
	return r, nil
}

func (s *Store) CreateRun(ctx context.Context, id, name, sourceRunID string) (Run, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Run{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	ts := formatTime(now)
	if _, err = tx.ExecContext(ctx, `UPDATE runs SET status='ended',ended_at=? WHERE status='active'`, ts); err != nil {
		return Run{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO runs(id,name,status,locked,created_at) VALUES(?,?,'active',0,?)`, id, name, ts); err != nil {
		return Run{}, err
	}
	if sourceRunID != "" {
		_, err = tx.ExecContext(ctx, `INSERT INTO students(run_id,id,name,created_at) SELECT ?,id,name,? FROM students WHERE run_id=?`, id, ts, sourceRunID)
		if err != nil {
			return Run{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return Run{}, err
	}
	return Run{ID: id, Name: name, Status: "active", CreatedAt: now}, nil
}

func (s *Store) SetLocked(ctx context.Context, runID string, locked bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE runs SET locked=? WHERE id=? AND status='active'`, locked, runID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ImportStudentsCSV(ctx context.Context, runID, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return 0, fmt.Errorf("read header: %w", err)
	}
	if len(header) != 2 || strings.TrimSpace(strings.TrimPrefix(header[0], "\ufeff")) != "id" || strings.TrimSpace(header[1]) != "name" {
		return 0, errors.New("CSV header must be id,name")
	}
	type entry struct{ id, name string }
	var entries []entry
	seen := map[string]bool{}
	line := 1
	for {
		line++
		rec, e := r.Read()
		if errors.Is(e, io.EOF) {
			break
		}
		if e != nil {
			return 0, fmt.Errorf("CSV line %d: %w", line, e)
		}
		if len(rec) != 2 {
			return 0, fmt.Errorf("CSV line %d: expected 2 fields", line)
		}
		id, name := strings.TrimSpace(rec[0]), strings.TrimSpace(rec[1])
		if id == "" || name == "" {
			return 0, fmt.Errorf("CSV line %d: id and name are required", line)
		}
		if seen[id] {
			return 0, fmt.Errorf("CSV line %d: duplicate id %q", line, id)
		}
		seen[id] = true
		entries = append(entries, entry{id, name})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	for _, e := range entries {
		_, err = tx.ExecContext(ctx, `INSERT INTO students(run_id,id,name,created_at) VALUES(?,?,?,?) ON CONFLICT(run_id,id) DO UPDATE SET name=excluded.name`, runID, e.id, e.name, now)
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(entries), nil
}

func (s *Store) Student(ctx context.Context, runID, id string) (Student, error) {
	var st Student
	var created string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,id,name,created_at FROM students WHERE run_id=? AND id=?`, runID, id).Scan(&st.RunID, &st.ID, &st.Name, &created)
	if err != nil {
		return Student{}, err
	}
	st.CreatedAt, err = parseTime(created)
	return st, err
}

func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !sess.IsTeacher {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE run_id=? AND student_id=? AND is_teacher=0 AND revoked_at IS NULL`, formatTime(now), sess.RunID, sess.StudentID); err != nil {
			return err
		}
	}
	var run, student any
	if sess.IsTeacher {
		run = nil
		student = nil
	} else {
		run = sess.RunID
		student = sess.StudentID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO sessions(token,run_id,student_id,is_teacher,expires_at,created_at,last_seen_at) VALUES(?,?,?,?,?,?,?)`, sess.TokenHash, run, student, sess.IsTeacher, formatTime(sess.ExpiresAt), formatTime(now), formatTime(now))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Session(ctx context.Context, tokenHash string) (Session, error) {
	var x Session
	var run, student, revoked sql.NullString
	var exp, created, last string
	err := s.db.QueryRowContext(ctx, `SELECT token,run_id,student_id,is_teacher,expires_at,created_at,last_seen_at,revoked_at FROM sessions WHERE token=?`, tokenHash).Scan(&x.TokenHash, &run, &student, &x.IsTeacher, &exp, &created, &last, &revoked)
	if err != nil {
		return Session{}, err
	}
	x.RunID = run.String
	x.StudentID = student.String
	x.ExpiresAt, _ = parseTime(exp)
	x.CreatedAt, _ = parseTime(created)
	x.LastSeenAt, _ = parseTime(last)
	if revoked.Valid {
		t, _ := parseTime(revoked.String)
		x.RevokedAt = &t
	}
	return x, nil
}

func (s *Store) TouchSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_seen_at=? WHERE token=?`, formatTime(time.Now().UTC()), tokenHash)
	return err
}
func (s *Store) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET revoked_at=? WHERE token=?`, formatTime(time.Now().UTC()), tokenHash)
	return err
}

func (s *Store) Design(ctx context.Context, runID, studentID string) (Design, error) {
	var d Design
	var tools, updated string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,student_id,persona,skill_md,tools,max_turns,updated_at FROM designs WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&d.RunID, &d.StudentID, &d.Persona, &d.SkillMD, &tools, &d.MaxTurns, &updated)
	if err != nil {
		return Design{}, err
	}
	d.Tools = decodeTools(tools)
	d.UpdatedAt, _ = parseTime(updated)
	return d, nil
}

func (s *Store) SaveDesign(ctx context.Context, d Design) (Design, error) {
	d.UpdatedAt = time.Now().UTC()
	tools := encodeTools(d.Tools)
	_, err := s.db.ExecContext(ctx, `INSERT INTO designs(run_id,student_id,persona,skill_md,tools,max_turns,updated_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(run_id,student_id) DO UPDATE SET persona=excluded.persona,skill_md=excluded.skill_md,tools=excluded.tools,max_turns=excluded.max_turns,updated_at=excluded.updated_at`, d.RunID, d.StudentID, d.Persona, d.SkillMD, tools, d.MaxTurns, formatTime(d.UpdatedAt))
	return d, err
}

func (s *Store) CreateConversation(ctx context.Context, c Conversation) (Conversation, error) {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = "新对话"
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO conversations(id,run_id,student_id,title,created_at,updated_at)
 SELECT ?,?,?,?, ?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)`,
		c.ID, c.RunID, c.StudentID, c.Title, formatTime(now), formatTime(now), c.RunID, c.StudentID)
	if err != nil {
		return Conversation{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Conversation{}, ErrNotFound
	}
	return c, nil
}

func (s *Store) Conversation(ctx context.Context, runID, studentID, conversationID string) (Conversation, error) {
	var c Conversation
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.run_id,c.student_id,c.title,
 (SELECT count(*) FROM messages m WHERE m.run_id=c.run_id AND m.student_id=c.student_id AND m.conversation_id=c.id),
 c.created_at,c.updated_at FROM conversations c WHERE c.run_id=? AND c.student_id=? AND c.id=?`,
		runID, studentID, conversationID).Scan(&c.ID, &c.RunID, &c.StudentID, &c.Title, &c.MessageCount, &created, &updated)
	if err != nil {
		return Conversation{}, err
	}
	c.CreatedAt, _ = parseTime(created)
	c.UpdatedAt, _ = parseTime(updated)
	return c, nil
}

func (s *Store) Conversations(ctx context.Context, runID, studentID string, limit int) ([]Conversation, error) {
	if limit < 1 || limit > 200 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.run_id,c.student_id,c.title,count(m.id),c.created_at,c.updated_at
 FROM conversations c LEFT JOIN messages m ON m.run_id=c.run_id AND m.student_id=c.student_id AND m.conversation_id=c.id
 WHERE c.run_id=? AND c.student_id=? GROUP BY c.run_id,c.student_id,c.id
 ORDER BY c.updated_at DESC,c.id DESC LIMIT ?`, runID, studentID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Conversation, 0)
	for rows.Next() {
		var c Conversation
		var created, updated string
		if err := rows.Scan(&c.ID, &c.RunID, &c.StudentID, &c.Title, &c.MessageCount, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = parseTime(created)
		c.UpdatedAt, _ = parseTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) AddMessage(ctx context.Context, m Message) (Message, error) {
	m.CreatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(run_id,student_id,conversation_id,turn_id,role,content,tool_calls,created_at)
 SELECT ?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM conversations WHERE run_id=? AND student_id=? AND id=?)`,
		m.RunID, m.StudentID, m.ConversationID, m.TurnID, m.Role, m.Content, m.ToolCalls, formatTime(m.CreatedAt), m.RunID, m.StudentID, m.ConversationID)
	if err != nil {
		return Message{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Message{}, ErrNotFound
	}
	m.ID, _ = res.LastInsertId()
	title := ""
	if m.Role == "user" {
		title = conversationTitle(m.Content)
	}
	if _, err = tx.ExecContext(ctx, `UPDATE conversations SET updated_at=?,title=CASE WHEN title='新对话' AND ?<>'' THEN ? ELSE title END
 WHERE run_id=? AND student_id=? AND id=?`, formatTime(m.CreatedAt), title, title, m.RunID, m.StudentID, m.ConversationID); err != nil {
		return Message{}, err
	}
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	return m, nil
}

func (s *Store) Messages(ctx context.Context, runID, studentID, conversationID string, afterID int64, limit int) ([]Message, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,student_id,conversation_id,turn_id,role,content,tool_calls,created_at
 FROM messages WHERE run_id=? AND student_id=? AND conversation_id=? AND id>? ORDER BY id LIMIT ?`, runID, studentID, conversationID, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

func (s *Store) StudentMessages(ctx context.Context, runID, studentID string, afterID int64, limit int) ([]Message, error) {
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,student_id,conversation_id,turn_id,role,content,tool_calls,created_at
 FROM messages WHERE run_id=? AND student_id=? AND id>? ORDER BY id LIMIT ?`, runID, studentID, afterID, limit)
	if err != nil {
		return nil, err
	}
	return scanMessages(rows)
}

func scanMessages(rows *sql.Rows) ([]Message, error) {
	defer rows.Close()
	out := make([]Message, 0)
	for rows.Next() {
		var m Message
		var created string
		if err := rows.Scan(&m.ID, &m.RunID, &m.StudentID, &m.ConversationID, &m.TurnID, &m.Role, &m.Content, &m.ToolCalls, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = parseTime(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func conversationTitle(v string) string {
	v = strings.Join(strings.Fields(v), " ")
	r := []rune(v)
	if len(r) > 24 {
		return string(r[:24]) + "…"
	}
	return v
}

func (s *Store) Usage(ctx context.Context, runID, studentID string) (Usage, error) {
	var u Usage
	err := s.db.QueryRowContext(ctx, `SELECT tokens_in,tokens_out,images,searches,estimated_cost FROM usage WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&u.TokensIn, &u.TokensOut, &u.Images, &u.Searches, &u.EstimatedCost)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, nil
	}
	return u, err
}
func (s *Store) AddUsage(ctx context.Context, runID, studentID string, in, out int64, cost float64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage(run_id,student_id,tokens_in,tokens_out,estimated_cost) VALUES(?,?,?,?,?) ON CONFLICT(run_id,student_id) DO UPDATE SET tokens_in=tokens_in+excluded.tokens_in,tokens_out=tokens_out+excluded.tokens_out,estimated_cost=estimated_cost+excluded.estimated_cost`, runID, studentID, in, out, cost)
	return err
}

func (s *Store) Wall(ctx context.Context, runID string) ([]WallStudent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT st.id,st.name,
 EXISTS(SELECT 1 FROM sessions se WHERE se.run_id=st.run_id AND se.student_id=st.id AND se.is_teacher=0 AND se.revoked_at IS NULL AND se.expires_at>?),
 COALESCE(length(trim(d.persona))>0,0),COALESCE(length(trim(d.skill_md))>0,0),
 (SELECT count(DISTINCT turn_id) FROM messages m WHERE m.run_id=st.run_id AND m.student_id=st.id AND m.role='user'),
 (SELECT max(last_seen_at) FROM sessions se WHERE se.run_id=st.run_id AND se.student_id=st.id AND se.revoked_at IS NULL)
 FROM students st LEFT JOIN designs d ON d.run_id=st.run_id AND d.student_id=st.id WHERE st.run_id=? ORDER BY st.id`, formatTime(time.Now().UTC()), runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WallStudent
	for rows.Next() {
		var w WallStudent
		var last sql.NullString
		if err := rows.Scan(&w.ID, &w.Name, &w.LoggedIn, &w.HasPersona, &w.HasSkill, &w.ChatTurns, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t, _ := parseTime(last.String)
			w.LastSeenAt = &t
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func formatTime(t time.Time) string         { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(v string) (time.Time, error) { return time.Parse(time.RFC3339Nano, v) }

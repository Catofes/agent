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

const (
	MemoryModeDisabled       = "disabled"
	MemoryModeReviewRequired = "review_required"
	MemoryModeAdaptive       = "adaptive"
	ConversationKindStudent  = "student"
	ConversationKindDemo     = "demo"
	SkillTriggerAuto         = "auto"
	SkillTriggerExplicit     = "explicit"
)

type RunPolicy struct {
	RunID                 string    `json:"-"`
	MemoryMode            string    `json:"memory_mode"`
	SkillsEnabled         bool      `json:"skills_enabled"`
	PresetSkills          []string  `json:"preset_skills"`
	AllowedTools          []string  `json:"allowed_tools"`
	ModelProvider         string    `json:"model_provider"`
	SearchProvider        string    `json:"search_provider"`
	DeepSeekSearchChannel string    `json:"deepseek_search_channel"`
	Revision              int64     `json:"revision"`
	UpdatedAt             time.Time `json:"updated_at"`
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

type Skill struct {
	ID          string    `json:"id"`
	RunID       string    `json:"-"`
	StudentID   string    `json:"-"`
	Name        string    `json:"name"`
	Summary     string    `json:"summary"`
	WhenToUse   string    `json:"when_to_use"`
	TriggerMode string    `json:"trigger_mode"`
	Content     string    `json:"content"`
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type Conversation struct {
	ID           string    `json:"id"`
	RunID        string    `json:"-"`
	StudentID    string    `json:"-"`
	Kind         string    `json:"-"`
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
	Reasoning      string    `json:"-"`
	FinishReason   string    `json:"finish_reason,omitempty"`
	ContextReceipt string    `json:"context_receipt,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type Memory struct {
	ID                   string    `json:"id"`
	RunID                string    `json:"-"`
	StudentID            string    `json:"-"`
	Content              string    `json:"content"`
	Status               string    `json:"status"`
	SourceConversationID string    `json:"source_conversation_id,omitempty"`
	SourceTurnID         string    `json:"source_turn_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type MemoryState struct {
	Enabled bool     `json:"enabled"`
	Items   []Memory `json:"items"`
}

type Artifact struct {
	ID               string    `json:"id"`
	RunnerArtifactID string    `json:"-"`
	RunID            string    `json:"-"`
	StudentID        string    `json:"-"`
	ConversationID   string    `json:"conversation_id"`
	TurnID           string    `json:"turn_id,omitempty"`
	ExecutionID      string    `json:"execution_id,omitempty"`
	Name             string    `json:"name"`
	MIMEType         string    `json:"mime_type"`
	Size             int64     `json:"size"`
	SHA256           string    `json:"sha256"`
	ExpiresAt        time.Time `json:"expires_at"`
	CreatedAt        time.Time `json:"created_at"`
}

type Usage struct {
	TokensIn      int64   `json:"tokens_in"`
	TokensOut     int64   `json:"tokens_out"`
	UnknownCalls  int64   `json:"unknown_calls"`
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
	if version > 16 {
		return fmt.Errorf("database version %d is newer than supported version 16", version)
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
	if version < 3 {
		if err := s.migrateReasoning(ctx); err != nil {
			return fmt.Errorf("migrate reasoning metadata: %w", err)
		}
	}
	if version < 4 {
		if err := s.migrateFinishReason(ctx); err != nil {
			return fmt.Errorf("migrate message finish reasons: %w", err)
		}
	}
	if version < 5 {
		if err := s.migrateUnknownUsage(ctx); err != nil {
			return fmt.Errorf("migrate unknown usage counter: %w", err)
		}
	}
	if version < 6 {
		if err := s.migrateMemory(ctx); err != nil {
			return fmt.Errorf("migrate memory: %w", err)
		}
	}
	if version < 7 {
		if err := s.migrateContextReceipt(ctx); err != nil {
			return fmt.Errorf("migrate context receipts: %w", err)
		}
	}
	if version < 8 {
		if err := s.migrateRunPolicies(ctx); err != nil {
			return fmt.Errorf("migrate run policies: %w", err)
		}
	}
	if version < 9 {
		if err := s.migrateSkills(ctx); err != nil {
			return fmt.Errorf("migrate skills: %w", err)
		}
	}
	if version < 10 {
		if err := s.migrateSkillIndex(ctx); err != nil {
			return fmt.Errorf("migrate skill index metadata: %w", err)
		}
	}
	if version < 11 {
		if err := s.migrateArtifacts(ctx); err != nil {
			return fmt.Errorf("migrate artifacts: %w", err)
		}
	}
	if version < 12 {
		if err := s.migrateSkillsPolicy(ctx); err != nil {
			return fmt.Errorf("migrate classroom Skill policy: %w", err)
		}
	}
	if version < 13 {
		if err := s.migratePresetSkillsPolicy(ctx); err != nil {
			return fmt.Errorf("migrate preset Skill policy: %w", err)
		}
	}
	if version < 14 {
		if err := s.migrateProviderPolicy(ctx); err != nil {
			return fmt.Errorf("migrate classroom provider policy: %w", err)
		}
	}
	if version < 15 {
		if err := s.migrateConversationKinds(ctx); err != nil {
			return fmt.Errorf("migrate conversation kinds: %w", err)
		}
	}
	if version < 16 {
		if err := s.migrateDeepSeekSearchChannel(ctx); err != nil {
			return fmt.Errorf("migrate DeepSeek search channel: %w", err)
		}
	}
	return nil
}

func (s *Store) migrateDeepSeekSearchChannel(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "run_policies", "deepseek_search_channel")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !has {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE run_policies ADD COLUMN deepseek_search_channel TEXT NOT NULL DEFAULT 'anthropic'`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE run_policies SET deepseek_search_channel='anthropic' WHERE deepseek_search_channel=''`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 16`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateConversationKinds(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "conversations", "kind")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !has {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE conversations ADD COLUMN kind TEXT NOT NULL DEFAULT 'student' CHECK(kind IN ('student','demo'))`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 15`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateProviderPolicy(ctx context.Context) error {
	columns := []struct {
		name string
		sql  string
	}{
		{"model_provider", `ALTER TABLE run_policies ADD COLUMN model_provider TEXT NOT NULL DEFAULT ''`},
		{"search_provider", `ALTER TABLE run_policies ADD COLUMN search_provider TEXT NOT NULL DEFAULT ''`},
	}
	missing := make([]string, 0, len(columns))
	for _, column := range columns {
		has, checkErr := s.tableHasColumn(ctx, "run_policies", column.name)
		if checkErr != nil {
			return checkErr
		}
		if !has {
			missing = append(missing, column.sql)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range missing {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 14`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migratePresetSkillsPolicy(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "run_policies", "preset_skills")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !has {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE run_policies ADD COLUMN preset_skills TEXT NOT NULL DEFAULT '[]'`); err != nil {
			return err
		}
	}
	for _, table := range []string{"run_policies", "designs"} {
		column := "allowed_tools"
		keys := "run_id"
		if table == "designs" {
			column = "tools"
			keys = "run_id,student_id"
		}
		rows, queryErr := tx.QueryContext(ctx, `SELECT `+keys+`,`+column+` FROM `+table)
		if queryErr != nil {
			return queryErr
		}
		type update struct{ runID, studentID, value string }
		updates := []update{}
		for rows.Next() {
			var item update
			if table == "designs" {
				queryErr = rows.Scan(&item.runID, &item.studentID, &item.value)
			} else {
				queryErr = rows.Scan(&item.runID, &item.value)
			}
			if queryErr != nil {
				rows.Close()
				return queryErr
			}
			items := decodeTools(item.value)
			clean := make([]string, 0, len(items))
			for _, name := range items {
				if name != "calculator" {
					clean = append(clean, name)
				}
			}
			item.value = encodeTools(clean)
			updates = append(updates, item)
		}
		if queryErr = rows.Close(); queryErr != nil {
			return queryErr
		}
		for _, item := range updates {
			if table == "designs" {
				_, queryErr = tx.ExecContext(ctx, `UPDATE designs SET tools=? WHERE run_id=? AND student_id=?`, item.value, item.runID, item.studentID)
			} else {
				_, queryErr = tx.ExecContext(ctx, `UPDATE run_policies SET allowed_tools=? WHERE run_id=?`, item.value, item.runID)
			}
			if queryErr != nil {
				return queryErr
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 13`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateSkillsPolicy(ctx context.Context) error {
	has, err := s.tableHasColumn(ctx, "run_policies", "skills_enabled")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !has {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE run_policies ADD COLUMN skills_enabled INTEGER NOT NULL DEFAULT 1 CHECK(skills_enabled IN (0,1))`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 12`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateArtifacts(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS artifacts(
 id TEXT NOT NULL, runner_artifact_id TEXT NOT NULL, run_id TEXT NOT NULL, student_id TEXT NOT NULL,
 conversation_id TEXT NOT NULL DEFAULT '', turn_id TEXT NOT NULL DEFAULT '', execution_id TEXT NOT NULL DEFAULT '',
 name TEXT NOT NULL, mime_type TEXT NOT NULL, size INTEGER NOT NULL CHECK(size>=0), sha256 TEXT NOT NULL,
 expires_at TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id,id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS artifacts_runner_id ON artifacts(runner_artifact_id)`,
		`CREATE INDEX IF NOT EXISTS artifacts_student_conversation ON artifacts(run_id,student_id,conversation_id,created_at,id)`,
		`PRAGMA user_version = 11`,
	} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migrateSkillIndex(ctx context.Context) error {
	columns := []struct {
		name string
		sql  string
	}{
		{"summary", `ALTER TABLE skills ADD COLUMN summary TEXT NOT NULL DEFAULT ''`},
		{"when_to_use", `ALTER TABLE skills ADD COLUMN when_to_use TEXT NOT NULL DEFAULT ''`},
		{"trigger_mode", `ALTER TABLE skills ADD COLUMN trigger_mode TEXT NOT NULL DEFAULT 'auto' CHECK(trigger_mode IN ('auto','explicit'))`},
	}
	missing := make([]string, 0, len(columns))
	for _, column := range columns {
		has, err := s.tableHasColumn(ctx, "skills", column.name)
		if err != nil {
			return err
		}
		if !has {
			missing = append(missing, column.sql)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, query := range append(missing,
		`UPDATE skills SET summary=name,when_to_use=description WHERE summary='' AND when_to_use=''`,
		`PRAGMA user_version = 10`,
	) {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migrateSkills(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS skills(
 id TEXT NOT NULL, run_id TEXT NOT NULL, student_id TEXT NOT NULL, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
 content TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1 CHECK(enabled IN (0,1)), created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id,id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS skills_student_updated ON skills(run_id,student_id,updated_at DESC,id)`); err != nil {
		return err
	}
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO skills(id,run_id,student_id,name,description,content,enabled,created_at,updated_at)
 SELECT 'skill_default',run_id,student_id,'我的 Skill','从原有 Skill 迁移；模型在任务匹配时按需加载。',skill_md,1,?,?
 FROM designs WHERE trim(skill_md)<>''`, now, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 9`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateRunPolicies(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS run_policies(
 run_id TEXT PRIMARY KEY REFERENCES runs(id), memory_mode TEXT NOT NULL CHECK(memory_mode IN ('disabled','review_required','adaptive')),
 allowed_tools TEXT NOT NULL DEFAULT '["calculator"]', revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0), updated_at TEXT NOT NULL
)`,
	} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO run_policies(run_id,memory_mode,allowed_tools,revision,updated_at)
 SELECT id,'review_required','["calculator"]',1,? FROM runs`, formatTime(time.Now().UTC())); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 8`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateContextReceipt(ctx context.Context) error {
	hasReceipt, err := s.tableHasColumn(ctx, "messages", "context_receipt")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasReceipt {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN context_receipt TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 7`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateMemory(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS memory_settings(
 run_id TEXT NOT NULL, student_id TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 0 CHECK(enabled IN (0,1)), updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`,
		`CREATE TABLE IF NOT EXISTS memories(
 id TEXT NOT NULL, run_id TEXT NOT NULL, student_id TEXT NOT NULL, content TEXT NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('candidate','confirmed')), source_conversation_id TEXT NOT NULL DEFAULT '',
 source_turn_id TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
 PRIMARY KEY(run_id,student_id,id), FOREIGN KEY(run_id,student_id) REFERENCES students(run_id,id)
)`,
		`CREATE INDEX IF NOT EXISTS memories_student_status ON memories(run_id,student_id,status,updated_at DESC,id)`,
		`PRAGMA user_version = 6`,
	} {
		if _, err = tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) migrateConversations(ctx context.Context) error {
	hasConversationID, err := s.tableHasColumn(ctx, "messages", "conversation_id")
	if err != nil {
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

func (s *Store) migrateReasoning(ctx context.Context) error {
	hasReasoning, err := s.tableHasColumn(ctx, "messages", "reasoning_content")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasReasoning {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN reasoning_content TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateFinishReason(ctx context.Context) error {
	hasFinishReason, err := s.tableHasColumn(ctx, "messages", "finish_reason")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasFinishReason {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE messages ADD COLUMN finish_reason TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) migrateUnknownUsage(ctx context.Context) error {
	hasUnknownCalls, err := s.tableHasColumn(ctx, "usage", "unknown_calls")
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if !hasUnknownCalls {
		if _, err = tx.ExecContext(ctx, `ALTER TABLE usage ADD COLUMN unknown_calls INTEGER NOT NULL DEFAULT 0 CHECK(unknown_calls>=0)`); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `PRAGMA user_version = 5`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) tableHasColumn(ctx context.Context, table, column string) (bool, error) {
	// Callers pass only migration-owned table names, never request data.
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notNull, pk int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return false, err
		}
		if name == column {
			return true, nil
		}
	}
	return false, rows.Err()
}

func (s *Store) EnsureActiveRun(ctx context.Context, id, name string) (Run, error) {
	r, err := s.ActiveRun(ctx)
	if err == nil {
		if err = s.ensureRunPolicy(ctx, r.ID); err != nil {
			return Run{}, err
		}
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
	if err = s.ensureRunPolicy(ctx, id); err != nil {
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
	return s.CreateRunWithPolicy(ctx, id, name, sourceRunID, RunPolicy{MemoryMode: MemoryModeReviewRequired, SkillsEnabled: true})
}

func (s *Store) CreateRunWithPolicy(ctx context.Context, id, name, sourceRunID string, policy RunPolicy) (Run, error) {
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
	policy = normalizeRunPolicy(policy)
	if _, err = tx.ExecContext(ctx, `INSERT INTO run_policies(run_id,memory_mode,skills_enabled,preset_skills,allowed_tools,model_provider,search_provider,deepseek_search_channel,revision,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, policy.MemoryMode, policy.SkillsEnabled, encodeTools(policy.PresetSkills), encodeTools(policy.AllowedTools), policy.ModelProvider, policy.SearchProvider, policy.DeepSeekSearchChannel, 1, ts); err != nil {
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

func normalizeRunPolicy(policy RunPolicy) RunPolicy {
	if policy.MemoryMode != MemoryModeDisabled && policy.MemoryMode != MemoryModeReviewRequired && policy.MemoryMode != MemoryModeAdaptive {
		policy.MemoryMode = MemoryModeReviewRequired
	}
	seen := map[string]bool{}
	tools := make([]string, 0, len(policy.AllowedTools))
	for _, name := range policy.AllowedTools {
		name = strings.TrimSpace(name)
		if name != "" && !seen[name] {
			seen[name] = true
			tools = append(tools, name)
		}
	}
	policy.AllowedTools = tools
	seen = map[string]bool{}
	presets := make([]string, 0, len(policy.PresetSkills))
	for _, id := range policy.PresetSkills {
		id = strings.TrimSpace(id)
		if id != "" && !seen[id] {
			seen[id] = true
			presets = append(presets, id)
		}
	}
	policy.PresetSkills = presets
	policy.ModelProvider = strings.ToLower(strings.TrimSpace(policy.ModelProvider))
	policy.SearchProvider = strings.ToLower(strings.TrimSpace(policy.SearchProvider))
	policy.DeepSeekSearchChannel = strings.ToLower(strings.TrimSpace(policy.DeepSeekSearchChannel))
	if policy.DeepSeekSearchChannel == "" {
		policy.DeepSeekSearchChannel = "anthropic"
	}
	return policy
}

func (s *Store) ensureRunPolicy(ctx context.Context, runID string) error {
	now := formatTime(time.Now().UTC())
	_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO run_policies(run_id,memory_mode,skills_enabled,preset_skills,allowed_tools,model_provider,search_provider,deepseek_search_channel,revision,updated_at)
	 VALUES(?,'review_required',1,'[]','[]','','','anthropic',1,?)`, runID, now)
	return err
}

// InitializeRunProviders fills provider selections introduced after a run was
// created. Existing teacher selections are never overwritten on restart.
func (s *Store) InitializeRunProviders(ctx context.Context, runID, modelProvider, searchProvider, deepSeekSearchChannel string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE run_policies
	 SET model_provider=CASE WHEN model_provider='' THEN ? ELSE model_provider END,
	     search_provider=CASE WHEN search_provider='' THEN ? ELSE search_provider END,
	     deepseek_search_channel=CASE WHEN deepseek_search_channel='' THEN ? ELSE deepseek_search_channel END
	 WHERE run_id=?`, strings.ToLower(strings.TrimSpace(modelProvider)), strings.ToLower(strings.TrimSpace(searchProvider)), strings.ToLower(strings.TrimSpace(deepSeekSearchChannel)), runID)
	return err
}

func (s *Store) RunPolicy(ctx context.Context, runID string) (RunPolicy, error) {
	var policy RunPolicy
	var presets, allowed, updated string
	err := s.db.QueryRowContext(ctx, `SELECT run_id,memory_mode,skills_enabled,preset_skills,allowed_tools,model_provider,search_provider,deepseek_search_channel,revision,updated_at FROM run_policies WHERE run_id=?`, runID).Scan(&policy.RunID, &policy.MemoryMode, &policy.SkillsEnabled, &presets, &allowed, &policy.ModelProvider, &policy.SearchProvider, &policy.DeepSeekSearchChannel, &policy.Revision, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		if err = s.ensureRunPolicy(ctx, runID); err != nil {
			return RunPolicy{}, err
		}
		err = s.db.QueryRowContext(ctx, `SELECT run_id,memory_mode,skills_enabled,preset_skills,allowed_tools,model_provider,search_provider,deepseek_search_channel,revision,updated_at FROM run_policies WHERE run_id=?`, runID).Scan(&policy.RunID, &policy.MemoryMode, &policy.SkillsEnabled, &presets, &allowed, &policy.ModelProvider, &policy.SearchProvider, &policy.DeepSeekSearchChannel, &policy.Revision, &updated)
	}
	if err != nil {
		return RunPolicy{}, err
	}
	policy.AllowedTools = decodeTools(allowed)
	policy.PresetSkills = decodeTools(presets)
	policy = normalizeRunPolicy(policy)
	policy.UpdatedAt, _ = parseTime(updated)
	return policy, nil
}

func (s *Store) SetRunPolicy(ctx context.Context, runID string, policy RunPolicy) (RunPolicy, error) {
	policy = normalizeRunPolicy(policy)
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE run_policies SET memory_mode=?,skills_enabled=?,preset_skills=?,allowed_tools=?,model_provider=?,search_provider=?,deepseek_search_channel=?,revision=revision+1,updated_at=?
	 WHERE run_id=? AND EXISTS(SELECT 1 FROM runs WHERE id=? AND status='active')`, policy.MemoryMode, policy.SkillsEnabled, encodeTools(policy.PresetSkills), encodeTools(policy.AllowedTools), policy.ModelProvider, policy.SearchProvider, policy.DeepSeekSearchChannel, formatTime(now), runID, runID)
	if err != nil {
		return RunPolicy{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return RunPolicy{}, ErrNotFound
	}
	return s.RunPolicy(ctx, runID)
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

func (s *Store) Skills(ctx context.Context, runID, studentID string) ([]Skill, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,student_id,name,summary,when_to_use,trigger_mode,content,enabled,created_at,updated_at
 FROM skills WHERE run_id=? AND student_id=? ORDER BY updated_at DESC,id`, runID, studentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Skill, 0)
	for rows.Next() {
		skill, scanErr := scanSkill(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, skill)
	}
	return out, rows.Err()
}

func (s *Store) Skill(ctx context.Context, runID, studentID, id string) (Skill, error) {
	return scanSkill(s.db.QueryRowContext(ctx, `SELECT id,run_id,student_id,name,summary,when_to_use,trigger_mode,content,enabled,created_at,updated_at
 FROM skills WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id))
}

func scanSkill(row rowScanner) (Skill, error) {
	var skill Skill
	var created, updated string
	if err := row.Scan(&skill.ID, &skill.RunID, &skill.StudentID, &skill.Name, &skill.Summary, &skill.WhenToUse, &skill.TriggerMode, &skill.Content, &skill.Enabled, &created, &updated); err != nil {
		return Skill{}, err
	}
	skill.CreatedAt, _ = parseTime(created)
	skill.UpdatedAt, _ = parseTime(updated)
	return skill, nil
}

func (s *Store) CreateSkill(ctx context.Context, skill Skill) (Skill, error) {
	if skill.TriggerMode == "" {
		skill.TriggerMode = SkillTriggerAuto
	}
	now := time.Now().UTC()
	skill.CreatedAt, skill.UpdatedAt = now, now
	res, err := s.db.ExecContext(ctx, `INSERT INTO skills(id,run_id,student_id,name,summary,when_to_use,trigger_mode,description,content,enabled,created_at,updated_at)
	 SELECT ?,?,?,?,?,?,?,?, ?,?,?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)`, skill.ID, skill.RunID, skill.StudentID, skill.Name, skill.Summary, skill.WhenToUse, skill.TriggerMode, skill.WhenToUse, skill.Content, skill.Enabled, formatTime(now), formatTime(now), skill.RunID, skill.StudentID)
	if err != nil {
		return Skill{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Skill{}, ErrNotFound
	}
	return skill, nil
}

func (s *Store) UpdateSkill(ctx context.Context, skill Skill) (Skill, error) {
	if skill.TriggerMode == "" {
		skill.TriggerMode = SkillTriggerAuto
	}
	skill.UpdatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE skills SET name=?,summary=?,when_to_use=?,trigger_mode=?,description=?,content=?,enabled=?,updated_at=?
	 WHERE run_id=? AND student_id=? AND id=?`, skill.Name, skill.Summary, skill.WhenToUse, skill.TriggerMode, skill.WhenToUse, skill.Content, skill.Enabled, formatTime(skill.UpdatedAt), skill.RunID, skill.StudentID, skill.ID)
	if err != nil {
		return Skill{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Skill{}, ErrNotFound
	}
	return s.Skill(ctx, skill.RunID, skill.StudentID, skill.ID)
}

func (s *Store) DeleteSkill(ctx context.Context, runID, studentID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM skills WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) CreateConversation(ctx context.Context, c Conversation) (Conversation, error) {
	now := time.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	c.Title = strings.TrimSpace(c.Title)
	if c.Title == "" {
		c.Title = "新对话"
	}
	if c.Kind == "" {
		c.Kind = ConversationKindStudent
	}
	if c.Kind != ConversationKindStudent && c.Kind != ConversationKindDemo {
		return Conversation{}, errors.New("invalid conversation kind")
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO conversations(id,run_id,student_id,kind,title,created_at,updated_at)
	 SELECT ?,?,?,?,?, ?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)`,
		c.ID, c.RunID, c.StudentID, c.Kind, c.Title, formatTime(now), formatTime(now), c.RunID, c.StudentID)
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
	return s.conversationByKind(ctx, runID, studentID, conversationID, ConversationKindStudent)
}

func (s *Store) DemoConversation(ctx context.Context, runID, studentID, conversationID string) (Conversation, error) {
	return s.conversationByKind(ctx, runID, studentID, conversationID, ConversationKindDemo)
}

func (s *Store) conversationByKind(ctx context.Context, runID, studentID, conversationID, kind string) (Conversation, error) {
	var c Conversation
	var created, updated string
	err := s.db.QueryRowContext(ctx, `SELECT c.id,c.run_id,c.student_id,c.kind,c.title,
 (SELECT count(*) FROM messages m WHERE m.run_id=c.run_id AND m.student_id=c.student_id AND m.conversation_id=c.id),
	 c.created_at,c.updated_at FROM conversations c WHERE c.run_id=? AND c.student_id=? AND c.id=? AND c.kind=?`,
		runID, studentID, conversationID, kind).Scan(&c.ID, &c.RunID, &c.StudentID, &c.Kind, &c.Title, &c.MessageCount, &created, &updated)
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
	rows, err := s.db.QueryContext(ctx, `SELECT c.id,c.run_id,c.student_id,c.kind,c.title,count(m.id),c.created_at,c.updated_at
 FROM conversations c LEFT JOIN messages m ON m.run_id=c.run_id AND m.student_id=c.student_id AND m.conversation_id=c.id
	 WHERE c.run_id=? AND c.student_id=? AND c.kind=? GROUP BY c.run_id,c.student_id,c.id
	 ORDER BY c.updated_at DESC,c.id DESC LIMIT ?`, runID, studentID, ConversationKindStudent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Conversation, 0)
	for rows.Next() {
		var c Conversation
		var created, updated string
		if err := rows.Scan(&c.ID, &c.RunID, &c.StudentID, &c.Kind, &c.Title, &c.MessageCount, &created, &updated); err != nil {
			return nil, err
		}
		c.CreatedAt, _ = parseTime(created)
		c.UpdatedAt, _ = parseTime(updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteConversation(ctx context.Context, runID, studentID, conversationID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM conversations WHERE run_id=? AND student_id=? AND id=? AND kind=?`, runID, studentID, conversationID, ConversationKindStudent)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM messages WHERE run_id=? AND student_id=? AND conversation_id=?`, runID, studentID, conversationID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM artifacts WHERE run_id=? AND student_id=? AND conversation_id=?`, runID, studentID, conversationID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) DeleteDemoConversations(ctx context.Context, runID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"messages", "artifacts"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE run_id=? AND conversation_id IN (SELECT id FROM conversations WHERE run_id=? AND kind=?)`, runID, runID, ConversationKindDemo); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM conversations WHERE run_id=? AND kind=?`, runID, ConversationKindDemo); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CreateArtifact(ctx context.Context, artifact Artifact) (Artifact, error) {
	artifact.CreatedAt = time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO artifacts(
 id,runner_artifact_id,run_id,student_id,conversation_id,turn_id,execution_id,name,mime_type,size,sha256,expires_at,created_at)
 SELECT ?,?,?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)`,
		artifact.ID, artifact.RunnerArtifactID, artifact.RunID, artifact.StudentID, artifact.ConversationID,
		artifact.TurnID, artifact.ExecutionID, artifact.Name, artifact.MIMEType, artifact.Size,
		artifact.SHA256, formatTime(artifact.ExpiresAt), formatTime(artifact.CreatedAt), artifact.RunID, artifact.StudentID)
	if err != nil {
		return Artifact{}, err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return Artifact{}, ErrNotFound
	}
	return artifact, nil
}

func (s *Store) Artifact(ctx context.Context, runID, studentID, id string) (Artifact, error) {
	return scanArtifact(s.db.QueryRowContext(ctx, `SELECT id,runner_artifact_id,run_id,student_id,conversation_id,turn_id,execution_id,name,mime_type,size,sha256,expires_at,created_at
 FROM artifacts WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id))
}

func (s *Store) Artifacts(ctx context.Context, runID, studentID, conversationID string) ([]Artifact, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,runner_artifact_id,run_id,student_id,conversation_id,turn_id,execution_id,name,mime_type,size,sha256,expires_at,created_at
 FROM artifacts WHERE run_id=? AND student_id=? AND (?='' OR conversation_id=?) ORDER BY created_at,id`, runID, studentID, conversationID, conversationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Artifact, 0)
	for rows.Next() {
		artifact, scanErr := scanArtifact(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, artifact)
	}
	return items, rows.Err()
}

func (s *Store) DeleteArtifact(ctx context.Context, runID, studentID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM artifacts WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count == 0 {
		return ErrNotFound
	}
	return nil
}

func scanArtifact(row rowScanner) (Artifact, error) {
	var artifact Artifact
	var expires, created string
	if err := row.Scan(&artifact.ID, &artifact.RunnerArtifactID, &artifact.RunID, &artifact.StudentID,
		&artifact.ConversationID, &artifact.TurnID, &artifact.ExecutionID, &artifact.Name, &artifact.MIMEType,
		&artifact.Size, &artifact.SHA256, &expires, &created); err != nil {
		return Artifact{}, err
	}
	artifact.ExpiresAt, _ = parseTime(expires)
	artifact.CreatedAt, _ = parseTime(created)
	return artifact, nil
}

func (s *Store) AddMessage(ctx context.Context, m Message) (Message, error) {
	m.CreatedAt = time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `INSERT INTO messages(run_id,student_id,conversation_id,turn_id,role,content,tool_calls,reasoning_content,finish_reason,context_receipt,created_at)
	 SELECT ?,?,?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM conversations WHERE run_id=? AND student_id=? AND id=?)`,
		m.RunID, m.StudentID, m.ConversationID, m.TurnID, m.Role, m.Content, m.ToolCalls, m.Reasoning, m.FinishReason, m.ContextReceipt, formatTime(m.CreatedAt), m.RunID, m.StudentID, m.ConversationID)
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

func (s *Store) UpdateMessageContextReceipt(ctx context.Context, runID, studentID, conversationID, turnID, receipt string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE messages SET context_receipt=? WHERE run_id=? AND student_id=? AND conversation_id=? AND turn_id=? AND role='user'`, receipt, runID, studentID, conversationID, turnID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Messages(ctx context.Context, runID, studentID, conversationID string, afterID int64, limit int) ([]Message, error) {
	if limit < 1 || limit > 500 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,run_id,student_id,conversation_id,turn_id,role,content,tool_calls,reasoning_content,finish_reason,context_receipt,created_at
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
	rows, err := s.db.QueryContext(ctx, `SELECT m.id,m.run_id,m.student_id,m.conversation_id,m.turn_id,m.role,m.content,m.tool_calls,m.reasoning_content,m.finish_reason,m.context_receipt,m.created_at
	 FROM messages m JOIN conversations c ON c.id=m.conversation_id AND c.run_id=m.run_id AND c.student_id=m.student_id
	 WHERE m.run_id=? AND m.student_id=? AND m.id>? AND c.kind=? ORDER BY m.id LIMIT ?`, runID, studentID, afterID, ConversationKindStudent, limit)
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
		if err := rows.Scan(&m.ID, &m.RunID, &m.StudentID, &m.ConversationID, &m.TurnID, &m.Role, &m.Content, &m.ToolCalls, &m.Reasoning, &m.FinishReason, &m.ContextReceipt, &created); err != nil {
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
	err := s.db.QueryRowContext(ctx, `SELECT tokens_in,tokens_out,unknown_calls,images,searches,estimated_cost FROM usage WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&u.TokensIn, &u.TokensOut, &u.UnknownCalls, &u.Images, &u.Searches, &u.EstimatedCost)
	if errors.Is(err, sql.ErrNoRows) {
		return Usage{}, nil
	}
	return u, err
}
func (s *Store) AddUsage(ctx context.Context, runID, studentID string, in, out, unknown int64, cost float64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO usage(run_id,student_id,tokens_in,tokens_out,unknown_calls,estimated_cost) VALUES(?,?,?,?,?,?) ON CONFLICT(run_id,student_id) DO UPDATE SET tokens_in=tokens_in+excluded.tokens_in,tokens_out=tokens_out+excluded.tokens_out,unknown_calls=unknown_calls+excluded.unknown_calls,estimated_cost=estimated_cost+excluded.estimated_cost`, runID, studentID, in, out, unknown, cost)
	return err
}

func (s *Store) MemoryState(ctx context.Context, runID, studentID string) (MemoryState, error) {
	var state MemoryState
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM memory_settings WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&state.Enabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return MemoryState{}, err
	}
	state.Items, err = s.Memories(ctx, runID, studentID, "")
	return state, err
}

func (s *Store) SetMemoryEnabled(ctx context.Context, runID, studentID string, enabled bool) error {
	now := formatTime(time.Now().UTC())
	res, err := s.db.ExecContext(ctx, `INSERT INTO memory_settings(run_id,student_id,enabled,updated_at)
 SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)
 ON CONFLICT(run_id,student_id) DO UPDATE SET enabled=excluded.enabled,updated_at=excluded.updated_at`,
		runID, studentID, enabled, now, runID, studentID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) MemoryEnabled(ctx context.Context, runID, studentID string) (bool, error) {
	enabled, _, err := s.MemorySetting(ctx, runID, studentID)
	return enabled, err
}

func (s *Store) MemorySetting(ctx context.Context, runID, studentID string) (bool, string, error) {
	var enabled bool
	var updated string
	err := s.db.QueryRowContext(ctx, `SELECT enabled,updated_at FROM memory_settings WHERE run_id=? AND student_id=?`, runID, studentID).Scan(&enabled, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	return enabled, updated, err
}

func (s *Store) Memories(ctx context.Context, runID, studentID, status string) ([]Memory, error) {
	query := `SELECT id,run_id,student_id,content,status,source_conversation_id,source_turn_id,created_at,updated_at
 FROM memories WHERE run_id=? AND student_id=?`
	args := []any{runID, studentID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY updated_at DESC,id DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Memory, 0)
	for rows.Next() {
		var m Memory
		var created, updated string
		if err := rows.Scan(&m.ID, &m.RunID, &m.StudentID, &m.Content, &m.Status, &m.SourceConversationID, &m.SourceTurnID, &created, &updated); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = parseTime(created)
		m.UpdatedAt, _ = parseTime(updated)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) AddMemory(ctx context.Context, m Memory) (Memory, error) {
	return s.addMemory(ctx, m, "", 0, "")
}

// AddMemoryIfSetting stores a candidate only while the Memory switch still has
// the same revision observed before asynchronous extraction began.
func (s *Store) AddMemoryIfSetting(ctx context.Context, m Memory, settingRevision string) (Memory, error) {
	return s.addMemory(ctx, m, settingRevision, 0, "")
}

// AddMemoryIfSettings also guards the classroom Memory mode. This prevents an
// extraction started under an old teacher policy from writing after a switch.
func (s *Store) AddMemoryIfSettings(ctx context.Context, m Memory, settingRevision string, policyRevision int64, memoryMode string) (Memory, error) {
	return s.addMemory(ctx, m, settingRevision, policyRevision, memoryMode)
}

func (s *Store) addMemory(ctx context.Context, m Memory, settingRevision string, policyRevision int64, memoryMode string) (Memory, error) {
	now := time.Now().UTC()
	m.CreatedAt, m.UpdatedAt = now, now
	query := `INSERT INTO memories(id,run_id,student_id,content,status,source_conversation_id,source_turn_id,created_at,updated_at)
 SELECT ?,?,?,?,?,?,?,?,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)`
	args := []any{m.ID, m.RunID, m.StudentID, m.Content, m.Status, m.SourceConversationID, m.SourceTurnID, formatTime(now), formatTime(now), m.RunID, m.StudentID}
	if settingRevision != "" {
		query += ` AND EXISTS(SELECT 1 FROM memory_settings WHERE run_id=? AND student_id=? AND enabled=1 AND updated_at=?)`
		args = append(args, m.RunID, m.StudentID, settingRevision)
	}
	if policyRevision > 0 {
		query += ` AND EXISTS(SELECT 1 FROM run_policies WHERE run_id=? AND revision=? AND memory_mode=? AND memory_mode<>'disabled')`
		args = append(args, m.RunID, policyRevision, memoryMode)
	}
	res, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return Memory{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Memory{}, ErrNotFound
	}
	return m, nil
}

func (s *Store) UpdateMemory(ctx context.Context, runID, studentID, id, content, status string) (Memory, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `UPDATE memories SET content=?,status=?,updated_at=? WHERE run_id=? AND student_id=? AND id=?`, content, status, formatTime(now), runID, studentID, id)
	if err != nil {
		return Memory{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Memory{}, ErrNotFound
	}
	var m Memory
	var created, updated string
	err = s.db.QueryRowContext(ctx, `SELECT id,run_id,student_id,content,status,source_conversation_id,source_turn_id,created_at,updated_at
 FROM memories WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id).Scan(&m.ID, &m.RunID, &m.StudentID, &m.Content, &m.Status, &m.SourceConversationID, &m.SourceTurnID, &created, &updated)
	if err != nil {
		return Memory{}, err
	}
	m.CreatedAt, _ = parseTime(created)
	m.UpdatedAt, _ = parseTime(updated)
	return m, nil
}

func (s *Store) DeleteMemory(ctx context.Context, runID, studentID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM memories WHERE run_id=? AND student_id=? AND id=?`, runID, studentID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err = tx.ExecContext(ctx, `UPDATE memory_settings SET updated_at=? WHERE run_id=? AND student_id=?`, formatTime(time.Now().UTC()), runID, studentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ClearMemories(ctx context.Context, runID, studentID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM memories WHERE run_id=? AND student_id=?`, runID, studentID); err != nil {
		return err
	}
	now := formatTime(time.Now().UTC())
	if _, err = tx.ExecContext(ctx, `INSERT INTO memory_settings(run_id,student_id,enabled,updated_at)
 SELECT ?,?,0,? WHERE EXISTS(SELECT 1 FROM students WHERE run_id=? AND id=?)
 ON CONFLICT(run_id,student_id) DO UPDATE SET updated_at=excluded.updated_at`, runID, studentID, now, runID, studentID); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Wall(ctx context.Context, runID string) ([]WallStudent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT st.id,st.name,
 EXISTS(SELECT 1 FROM sessions se WHERE se.run_id=st.run_id AND se.student_id=st.id AND se.is_teacher=0 AND se.revoked_at IS NULL AND se.expires_at>?),
	 COALESCE(length(trim(d.persona))>0,0),
	 (COALESCE(length(trim(d.skill_md))>0,0) OR EXISTS(SELECT 1 FROM skills sk WHERE sk.run_id=st.run_id AND sk.student_id=st.id AND sk.enabled=1 AND trim(sk.content)<>'')),
 (SELECT count(DISTINCT m.turn_id) FROM messages m
  JOIN conversations c ON c.run_id=m.run_id AND c.student_id=m.student_id AND c.id=m.conversation_id
  WHERE m.run_id=st.run_id AND m.student_id=st.id AND m.role='user' AND c.kind='student'),
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

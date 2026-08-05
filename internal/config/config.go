package config

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	DatabasePath       string
	StudentsCSV        string
	DeepSeekBaseURL    string
	DeepSeekModel      string
	DeepSeekAPIKey     string
	AdminPassword      string
	AnonymousHMACKey   string
	CookieSecure       bool
	RequireNameInitial bool
	SessionTTL         time.Duration
	LLMTimeout         time.Duration
	LLMConcurrency     int
	StudentTokenBudget int64
	DefaultMaxTurns    int
	MinMaxTurns        int
	MaxMaxTurns        int
	MaxToolCalls       int
	MaxPersonaChars    int
	MaxSkillChars      int
	MaxInputChars      int
	MaxOutputChars     int
	MaxReasoningChars  int
	InputPricePerM     float64
	OutputPricePerM    float64
	InitialRunName     string
	Version            string
}

func Load(version string) (Config, error) {
	c := Config{Version: version}
	flag.StringVar(&c.ListenAddr, "listen", env("LISTEN_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&c.DatabasePath, "db", env("DATABASE_PATH", "data/app.db"), "SQLite database path")
	flag.StringVar(&c.StudentsCSV, "students", env("STUDENTS_CSV", "data/students.csv"), "students CSV path")
	flag.StringVar(&c.DeepSeekBaseURL, "deepseek-base-url", env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "DeepSeek API base URL")
	flag.StringVar(&c.DeepSeekModel, "deepseek-model", env("DEEPSEEK_MODEL", "deepseek-v4-flash"), "DeepSeek model name")
	flag.StringVar(&c.DeepSeekAPIKey, "deepseek-api-key", os.Getenv("DEEPSEEK_API_KEY"), "DeepSeek API key")
	flag.StringVar(&c.AdminPassword, "admin-password", os.Getenv("ADMIN_PASSWORD"), "teacher password")
	flag.StringVar(&c.AnonymousHMACKey, "anonymous-hmac-key", os.Getenv("ANONYMOUS_HMAC_KEY"), "HMAC key for anonymous provider IDs")
	flag.BoolVar(&c.CookieSecure, "cookie-secure", envBool("COOKIE_SECURE", false), "mark cookies Secure")
	flag.BoolVar(&c.RequireNameInitial, "require-name-initial", envBool("REQUIRE_NAME_INITIAL", false), "require the first character of the student name at login")
	flag.DurationVar(&c.SessionTTL, "session-ttl", envDuration("SESSION_TTL", 12*time.Hour), "session lifetime")
	flag.DurationVar(&c.LLMTimeout, "llm-timeout", envDuration("LLM_TIMEOUT", 90*time.Second), "timeout for one model call")
	flag.IntVar(&c.LLMConcurrency, "llm-concurrency", envInt("LLM_CONCURRENCY", 40), "maximum concurrent model calls")
	flag.Int64Var(&c.StudentTokenBudget, "student-token-budget", envInt64("STUDENT_TOKEN_BUDGET", 200000), "token budget per student and run")
	flag.IntVar(&c.DefaultMaxTurns, "default-max-turns", envInt("DEFAULT_MAX_TURNS", 5), "default model iterations per chat")
	flag.IntVar(&c.MinMaxTurns, "min-max-turns", envInt("MIN_MAX_TURNS", 1), "minimum selectable model iterations")
	flag.IntVar(&c.MaxMaxTurns, "max-max-turns", envInt("MAX_MAX_TURNS", 8), "maximum selectable model iterations")
	flag.IntVar(&c.MaxToolCalls, "max-tool-calls", envInt("MAX_TOOL_CALLS", 8), "tool call cap per chat")
	flag.IntVar(&c.MaxPersonaChars, "max-persona-chars", envInt("MAX_PERSONA_CHARS", 4000), "persona character limit")
	flag.IntVar(&c.MaxSkillChars, "max-skill-chars", envInt("MAX_SKILL_CHARS", 12000), "skill character limit")
	flag.IntVar(&c.MaxInputChars, "max-input-chars", envInt("MAX_INPUT_CHARS", 4000), "chat input character limit")
	flag.IntVar(&c.MaxOutputChars, "max-output-chars", envInt("MAX_OUTPUT_CHARS", 16000), "model output character limit")
	flag.IntVar(&c.MaxReasoningChars, "max-reasoning-chars", envInt("MAX_REASONING_CHARS", 12000), "reasoning draft character limit per chat")
	flag.Float64Var(&c.InputPricePerM, "input-price-per-million", envFloat("INPUT_PRICE_PER_MILLION", 0), "input token price per million")
	flag.Float64Var(&c.OutputPricePerM, "output-price-per-million", envFloat("OUTPUT_PRICE_PER_MILLION", 0), "output token price per million")
	flag.StringVar(&c.InitialRunName, "initial-run-name", env("INITIAL_RUN_NAME", "课堂 1"), "name used when creating the first run")
	flag.Parse()

	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func (c Config) Validate() error {
	var missing []string
	if strings.TrimSpace(c.AdminPassword) == "" {
		missing = append(missing, "ADMIN_PASSWORD or -admin-password")
	}
	if strings.TrimSpace(c.DeepSeekAPIKey) == "" {
		missing = append(missing, "DEEPSEEK_API_KEY or -deepseek-api-key")
	}
	if strings.TrimSpace(c.AnonymousHMACKey) == "" {
		missing = append(missing, "ANONYMOUS_HMAC_KEY or -anonymous-hmac-key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required configuration: %s", strings.Join(missing, ", "))
	}
	if c.LLMConcurrency < 1 || c.StudentTokenBudget < 1 || c.MaxToolCalls < 1 {
		return errors.New("concurrency, token budget, and max tool calls must be positive")
	}
	if c.MinMaxTurns < 1 || c.DefaultMaxTurns < c.MinMaxTurns || c.DefaultMaxTurns > c.MaxMaxTurns {
		return errors.New("max-turn settings must satisfy 1 <= min <= default <= max")
	}
	if c.MaxPersonaChars < 1 || c.MaxSkillChars < 1 || c.MaxInputChars < 1 || c.MaxOutputChars < 1 || c.MaxReasoningChars < 1 {
		return errors.New("text limits must be positive")
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

func envInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envFloat(key string, fallback float64) float64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fallback
	}
	return n
}

func envDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

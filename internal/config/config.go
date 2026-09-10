package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddr                     string
	DatabasePath                   string
	StudentsCSV                    string
	DeepSeekBaseURL                string
	DeepSeekModel                  string
	DeepSeekAPIKey                 string
	QwenBaseURL                    string
	QwenModel                      string
	QwenAPIKey                     string
	QwenReasoningEffort            string
	QwenInputPricePerM             float64
	QwenOutputPricePerM            float64
	BailianDeepSeekBaseURL         string
	BailianDeepSeekModel           string
	BailianDeepSeekAPIKey          string
	BailianDeepSeekInputPricePerM  float64
	BailianDeepSeekOutputPricePerM float64
	WebSearchProvider              string
	DeepSeekSearchChannel          string
	ZhipuSearchAPIKey              string
	ZhipuSearchEngine              string
	RunnerURL                      string
	RunnerToken                    string
	RunnerTimeout                  time.Duration
	MaxPythonCodeChars             int
	MaxArtifactBytes               int64
	AdminPassword                  string
	AnonymousHMACKey               string
	CookieSecure                   bool
	RequireNameInitial             bool
	SessionTTL                     time.Duration
	LLMTimeout                     time.Duration
	LLMConcurrency                 int
	StudentTokenBudget             int64
	DefaultMaxTurns                int
	MinMaxTurns                    int
	MaxMaxTurns                    int
	MaxToolCalls                   int
	MaxPersonaChars                int
	MaxSkillChars                  int
	MaxInputChars                  int
	MaxOutputChars                 int
	MaxReasoningChars              int
	MaxMemoryItems                 int
	MaxMemoryChars                 int
	MaxMemoryTokens                int
	MemoryExtractTimeout           time.Duration
	InputPricePerM                 float64
	OutputPricePerM                float64
	InitialRunName                 string
	Version                        string
}

func Load(version string) (Config, error) {
	c := Config{Version: version}
	flag.StringVar(&c.ListenAddr, "listen", env("LISTEN_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&c.DatabasePath, "db", env("DATABASE_PATH", "data/app.db"), "SQLite database path")
	flag.StringVar(&c.StudentsCSV, "students", env("STUDENTS_CSV", "data/students.csv"), "students CSV path")
	flag.StringVar(&c.DeepSeekBaseURL, "deepseek-base-url", env("DEEPSEEK_BASE_URL", "https://api.deepseek.com"), "DeepSeek API base URL")
	flag.StringVar(&c.DeepSeekModel, "deepseek-model", env("DEEPSEEK_MODEL", "deepseek-v4-flash"), "DeepSeek model name")
	flag.StringVar(&c.DeepSeekAPIKey, "deepseek-api-key", os.Getenv("DEEPSEEK_API_KEY"), "DeepSeek API key")
	flag.StringVar(&c.QwenBaseURL, "qwen-base-url", env("QWEN_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"), "Qwen OpenAI-compatible API base URL")
	flag.StringVar(&c.QwenModel, "qwen-model", env("QWEN_MODEL", "qwen3.8-flash"), "Qwen model name")
	flag.StringVar(&c.QwenAPIKey, "qwen-api-key", os.Getenv("QWEN_API_KEY"), "Qwen API key; optional backup provider")
	flag.StringVar(&c.QwenReasoningEffort, "qwen-reasoning-effort", env("QWEN_REASONING_EFFORT", "low"), "Qwen reasoning effort: none, low, medium, or xhigh")
	flag.Float64Var(&c.QwenInputPricePerM, "qwen-input-price-per-million", envFloat("QWEN_INPUT_PRICE_PER_MILLION", 0), "Qwen input price per million tokens")
	flag.Float64Var(&c.QwenOutputPricePerM, "qwen-output-price-per-million", envFloat("QWEN_OUTPUT_PRICE_PER_MILLION", 0), "Qwen output price per million tokens")
	flag.StringVar(&c.BailianDeepSeekBaseURL, "bailian-deepseek-base-url", os.Getenv("BAILIAN_DEEPSEEK_BASE_URL"), "Alibaba Cloud Model Studio OpenAI-compatible API base URL")
	flag.StringVar(&c.BailianDeepSeekModel, "bailian-deepseek-model", env("BAILIAN_DEEPSEEK_MODEL", "deepseek-v4-flash-0731"), "DeepSeek model served by Alibaba Cloud Model Studio")
	flag.StringVar(&c.BailianDeepSeekAPIKey, "bailian-deepseek-api-key", os.Getenv("BAILIAN_DEEPSEEK_API_KEY"), "Alibaba Cloud Model Studio API key; optional backup provider")
	flag.Float64Var(&c.BailianDeepSeekInputPricePerM, "bailian-deepseek-input-price-per-million", envFloat("BAILIAN_DEEPSEEK_INPUT_PRICE_PER_MILLION", 0), "Alibaba-hosted DeepSeek input price per million tokens")
	flag.Float64Var(&c.BailianDeepSeekOutputPricePerM, "bailian-deepseek-output-price-per-million", envFloat("BAILIAN_DEEPSEEK_OUTPUT_PRICE_PER_MILLION", 0), "Alibaba-hosted DeepSeek output price per million tokens")
	flag.StringVar(&c.WebSearchProvider, "web-search-provider", env("WEB_SEARCH_PROVIDER", "disabled"), "web search provider: disabled, zhipu, deepseek, qwen, or bailian-deepseek")
	flag.StringVar(&c.DeepSeekSearchChannel, "deepseek-search-channel", env("DEEPSEEK_SEARCH_CHANNEL", "anthropic"), "DeepSeek web search channel: anthropic or responses")
	flag.StringVar(&c.ZhipuSearchAPIKey, "zhipu-search-api-key", os.Getenv("ZHIPU_SEARCH_API_KEY"), "Zhipu Web Search API key")
	flag.StringVar(&c.ZhipuSearchEngine, "zhipu-search-engine", env("ZHIPU_SEARCH_ENGINE", "search_std"), "Zhipu search engine: search_std, search_pro, search_pro_sogou, or search_pro_quark")
	flag.StringVar(&c.RunnerURL, "runner-url", os.Getenv("RUNNER_URL"), "internal Python runner base URL")
	flag.StringVar(&c.RunnerToken, "runner-token", os.Getenv("RUNNER_TOKEN"), "internal Python runner bearer token")
	flag.DurationVar(&c.RunnerTimeout, "runner-timeout", envDuration("RUNNER_TIMEOUT", 20*time.Second), "timeout for one Python runner request, including queue wait")
	flag.IntVar(&c.MaxPythonCodeChars, "max-python-code-chars", envInt("MAX_PYTHON_CODE_CHARS", 12000), "Python source character limit")
	flag.Int64Var(&c.MaxArtifactBytes, "max-artifact-bytes", envInt64("MAX_ARTIFACT_BYTES", 10<<20), "maximum uploaded artifact size")
	flag.StringVar(&c.AdminPassword, "admin-password", os.Getenv("ADMIN_PASSWORD"), "teacher password")
	flag.StringVar(&c.AnonymousHMACKey, "anonymous-hmac-key", os.Getenv("ANONYMOUS_HMAC_KEY"), "HMAC key for anonymous provider IDs")
	flag.BoolVar(&c.CookieSecure, "cookie-secure", envBool("COOKIE_SECURE", false), "mark cookies Secure")
	flag.BoolVar(&c.RequireNameInitial, "require-name-initial", envBool("REQUIRE_NAME_INITIAL", false), "require the first character of the student name at login")
	flag.DurationVar(&c.SessionTTL, "session-ttl", envDuration("SESSION_TTL", 12*time.Hour), "session lifetime")
	flag.DurationVar(&c.LLMTimeout, "llm-timeout", envDuration("LLM_TIMEOUT", 180*time.Second), "timeout for one model call")
	flag.IntVar(&c.LLMConcurrency, "llm-concurrency", envInt("LLM_CONCURRENCY", 40), "maximum concurrent model calls")
	flag.Int64Var(&c.StudentTokenBudget, "student-token-budget", envInt64("STUDENT_TOKEN_BUDGET", 50_000_000), "token budget per student and run")
	flag.IntVar(&c.DefaultMaxTurns, "default-max-turns", envInt("DEFAULT_MAX_TURNS", 45), "default model iterations per chat")
	flag.IntVar(&c.MinMaxTurns, "min-max-turns", envInt("MIN_MAX_TURNS", 1), "minimum selectable model iterations")
	flag.IntVar(&c.MaxMaxTurns, "max-max-turns", envInt("MAX_MAX_TURNS", 60), "maximum selectable model iterations")
	flag.IntVar(&c.MaxToolCalls, "max-tool-calls", envInt("MAX_TOOL_CALLS", 30), "tool call cap per chat")
	flag.IntVar(&c.MaxPersonaChars, "max-persona-chars", envInt("MAX_PERSONA_CHARS", 4000), "persona character limit")
	flag.IntVar(&c.MaxSkillChars, "max-skill-chars", envInt("MAX_SKILL_CHARS", 12000), "skill character limit")
	flag.IntVar(&c.MaxInputChars, "max-input-chars", envInt("MAX_INPUT_CHARS", 12000), "chat input character limit")
	flag.IntVar(&c.MaxOutputChars, "max-output-chars", envInt("MAX_OUTPUT_CHARS", 48000), "model output character limit")
	flag.IntVar(&c.MaxReasoningChars, "max-reasoning-chars", envInt("MAX_REASONING_CHARS", 12000), "reasoning draft character limit per chat")
	flag.IntVar(&c.MaxMemoryItems, "max-memory-items", envInt("MAX_MEMORY_ITEMS", 30), "maximum memory candidates and confirmed items per student and run")
	flag.IntVar(&c.MaxMemoryChars, "max-memory-chars", envInt("MAX_MEMORY_CHARS", 400), "character limit per memory item")
	flag.IntVar(&c.MaxMemoryTokens, "max-memory-tokens", envInt("MAX_MEMORY_TOKENS", 1200), "estimated token limit for all memory items per student and run")
	flag.DurationVar(&c.MemoryExtractTimeout, "memory-extract-timeout", envDuration("MEMORY_EXTRACT_TIMEOUT", 20*time.Second), "timeout for asynchronous memory candidate extraction")
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
	provider := strings.ToLower(strings.TrimSpace(c.WebSearchProvider))
	if provider != "" && provider != "disabled" && provider != "zhipu" && provider != "deepseek" && provider != "qwen" && provider != "bailian-deepseek" {
		return errors.New("WEB_SEARCH_PROVIDER must be disabled, zhipu, deepseek, qwen, or bailian-deepseek")
	}
	if provider == "zhipu" && strings.TrimSpace(c.ZhipuSearchAPIKey) == "" {
		return errors.New("ZHIPU_SEARCH_API_KEY is required when WEB_SEARCH_PROVIDER=zhipu")
	}
	if provider == "qwen" && strings.TrimSpace(c.QwenAPIKey) == "" {
		return errors.New("QWEN_API_KEY is required when WEB_SEARCH_PROVIDER=qwen")
	}
	if provider == "bailian-deepseek" && (strings.TrimSpace(c.BailianDeepSeekAPIKey) == "" || strings.TrimSpace(c.BailianDeepSeekBaseURL) == "") {
		return errors.New("BAILIAN_DEEPSEEK_API_KEY and BAILIAN_DEEPSEEK_BASE_URL are required when WEB_SEARCH_PROVIDER=bailian-deepseek")
	}
	channel := strings.ToLower(strings.TrimSpace(c.DeepSeekSearchChannel))
	if channel == "" {
		channel = "anthropic"
	}
	if channel != "anthropic" && channel != "responses" {
		return errors.New("DEEPSEEK_SEARCH_CHANNEL must be anthropic or responses")
	}
	effort := strings.ToLower(strings.TrimSpace(c.QwenReasoningEffort))
	if effort == "" {
		effort = "low"
	}
	if effort != "none" && effort != "low" && effort != "medium" && effort != "xhigh" {
		return errors.New("QWEN_REASONING_EFFORT must be none, low, medium, or xhigh")
	}
	if (strings.TrimSpace(c.BailianDeepSeekBaseURL) == "") != (strings.TrimSpace(c.BailianDeepSeekAPIKey) == "") {
		return errors.New("BAILIAN_DEEPSEEK_BASE_URL and BAILIAN_DEEPSEEK_API_KEY must be configured together")
	}
	if strings.TrimSpace(c.BailianDeepSeekBaseURL) != "" {
		parsed, err := url.Parse(c.BailianDeepSeekBaseURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("BAILIAN_DEEPSEEK_BASE_URL must be an absolute http or https URL")
		}
		if strings.TrimSpace(c.BailianDeepSeekModel) == "" {
			return errors.New("BAILIAN_DEEPSEEK_MODEL must not be empty")
		}
	}
	engines := map[string]bool{"search_std": true, "search_pro": true, "search_pro_sogou": true, "search_pro_quark": true}
	if engine := strings.TrimSpace(c.ZhipuSearchEngine); engine != "" && !engines[engine] {
		return errors.New("ZHIPU_SEARCH_ENGINE must be search_std, search_pro, search_pro_sogou, or search_pro_quark")
	}
	if (strings.TrimSpace(c.RunnerURL) == "") != (strings.TrimSpace(c.RunnerToken) == "") {
		return errors.New("RUNNER_URL and RUNNER_TOKEN must be configured together")
	}
	if strings.TrimSpace(c.RunnerURL) != "" {
		parsed, err := url.Parse(c.RunnerURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("RUNNER_URL must be an absolute http or https URL")
		}
	}
	if c.RunnerTimeout <= 0 || c.MaxPythonCodeChars < 1 || c.MaxArtifactBytes < 1 {
		return errors.New("runner timeout and limits must be positive")
	}
	if c.LLMConcurrency < 1 || c.StudentTokenBudget < 1 || c.MaxToolCalls < 1 {
		return errors.New("concurrency, token budget, and max tool calls must be positive")
	}
	if c.MinMaxTurns < 1 || c.DefaultMaxTurns < c.MinMaxTurns || c.DefaultMaxTurns > c.MaxMaxTurns {
		return errors.New("max-turn settings must satisfy 1 <= min <= default <= max")
	}
	if c.MaxPersonaChars < 1 || c.MaxSkillChars < 1 || c.MaxInputChars < 1 || c.MaxOutputChars < 1 || c.MaxReasoningChars < 1 || c.MaxMemoryItems < 1 || c.MaxMemoryChars < 1 || c.MaxMemoryTokens < 1 || c.MemoryExtractTimeout <= 0 {
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

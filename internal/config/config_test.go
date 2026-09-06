package config

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAcceptsCompleteConfiguration(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReportsAllMissingSecrets(t *testing.T) {
	cfg := validConfig()
	cfg.AdminPassword = " "
	cfg.DeepSeekAPIKey = ""
	cfg.AnonymousHMACKey = "\t"
	err := cfg.Validate()
	if err == nil {
		t.Fatal("missing secrets were accepted")
	}
	for _, want := range []string{"ADMIN_PASSWORD", "DEEPSEEK_API_KEY", "ANONYMOUS_HMAC_KEY"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %s", err, want)
		}
	}
}

func TestValidateRejectsUnsafeLimits(t *testing.T) {
	tests := map[string]func(*Config){
		"zero concurrency":     func(c *Config) { c.LLMConcurrency = 0 },
		"zero token budget":    func(c *Config) { c.StudentTokenBudget = 0 },
		"zero tool calls":      func(c *Config) { c.MaxToolCalls = 0 },
		"bad turn ordering":    func(c *Config) { c.DefaultMaxTurns = c.MaxMaxTurns + 1 },
		"zero persona limit":   func(c *Config) { c.MaxPersonaChars = 0 },
		"zero skill limit":     func(c *Config) { c.MaxSkillChars = 0 },
		"zero input limit":     func(c *Config) { c.MaxInputChars = 0 },
		"zero output limit":    func(c *Config) { c.MaxOutputChars = 0 },
		"zero reasoning":       func(c *Config) { c.MaxReasoningChars = 0 },
		"zero memory items":    func(c *Config) { c.MaxMemoryItems = 0 },
		"zero memory chars":    func(c *Config) { c.MaxMemoryChars = 0 },
		"zero memory tokens":   func(c *Config) { c.MaxMemoryTokens = 0 },
		"zero extract timeout": func(c *Config) { c.MemoryExtractTimeout = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("invalid configuration was accepted")
			}
		})
	}
}

func TestValidateWebSearchProvider(t *testing.T) {
	t.Run("deepseek uses model key", func(t *testing.T) {
		cfg := validConfig()
		cfg.WebSearchProvider = "deepseek"
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("zhipu requires search key", func(t *testing.T) {
		cfg := validConfig()
		cfg.WebSearchProvider = "zhipu"
		cfg.ZhipuSearchEngine = "search_std"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "ZHIPU_SEARCH_API_KEY") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("rejects unknown provider", func(t *testing.T) {
		cfg := validConfig()
		cfg.WebSearchProvider = "brave"
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "WEB_SEARCH_PROVIDER") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestValidateRunnerConfiguration(t *testing.T) {
	cfg := validConfig()
	cfg.RunnerURL = "http://10.16.100.20:8090"
	cfg.RunnerToken = "runner-secret"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	cfg.RunnerURL = "runner.internal"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "RUNNER_URL") {
		t.Fatalf("invalid URL error=%v", err)
	}
	cfg = validConfig()
	cfg.RunnerToken = "runner-secret"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("mismatched runner config error=%v", err)
	}
}

func validConfig() Config {
	return Config{
		AdminPassword:        "teacher-secret",
		DeepSeekAPIKey:       "provider-secret",
		AnonymousHMACKey:     "anonymous-secret",
		LLMConcurrency:       8,
		StudentTokenBudget:   10_000,
		DefaultMaxTurns:      30,
		MinMaxTurns:          1,
		MaxMaxTurns:          60,
		MaxToolCalls:         4,
		MaxPersonaChars:      4_000,
		MaxSkillChars:        12_000,
		MaxInputChars:        4_000,
		MaxOutputChars:       16_000,
		MaxReasoningChars:    12_000,
		MaxMemoryItems:       30,
		MaxMemoryChars:       400,
		MaxMemoryTokens:      1_200,
		MemoryExtractTimeout: 20 * time.Second,
		RunnerTimeout:        10 * time.Second,
		MaxPythonCodeChars:   12_000,
		MaxArtifactBytes:     10 << 20,
	}
}

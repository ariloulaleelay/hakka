package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ariloulaleelay/hakka/agent/adapters"
)

const sample = `{
  "default": "fast",
  "models": {
    "fast": {
      "dialect": "openai",
      "base_url": "${env: HAKKA_ENDPOINT}/v1",
      "model": "deepseek-latest",
      "headers": {
        "Authorization": "OAuth ${env: HAKKA_TOKEN}",
        "Need-Raw-Answer": "true"
      }
    },
    "smart": {
      "dialect": "anthropic",
      "base_url": "https://example/anthropic/v1",
      "model": "claude-opus-4-7",
      "headers": {"Authorization": "OAuth ${env: HAKKA_TOKEN}"},
      "extra": {"anthropic_version": "2023-06-01", "max_tokens": 2048}
    },
    "google": {
      "dialect": "gemini",
      "base_url": "https://example/google/v1beta",
      "model": "gemini-3.1-pro-preview",
      "headers": {"Authorization": "OAuth ${env: HAKKA_TOKEN}"}
    }
  }
}`

func writeSample(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoad(t *testing.T) {
	os.Setenv("HAKKA_ENDPOINT", "https://api.example.com/internal/deepseek-v3-1-terminus")
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_ENDPOINT")
	defer os.Unsetenv("HAKKA_TOKEN")

	f, err := Load(writeSample(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if f.Default != "fast" {
		t.Fatalf("default: %q", f.Default)
	}
	if len(f.Models) != 3 {
		t.Fatalf("models: %+v", f.Models)
	}
	// Verify env vars were expanded in the model config
	fast := f.Models["fast"]
	if fast.BaseURL != "https://api.example.com/internal/deepseek-v3-1-terminus/v1" {
		t.Fatalf("fast.base_url expanded: %q", fast.BaseURL)
	}
	if fast.Headers["Authorization"] != "OAuth test-token" {
		t.Fatalf("fast.headers.Authorization expanded: %q", fast.Headers["Authorization"])
	}
}

func TestBuildRegistry(t *testing.T) {
	os.Setenv("HAKKA_ENDPOINT", "https://api.example.com/internal/deepseek-v3-1-terminus")
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_ENDPOINT")
	defer os.Unsetenv("HAKKA_TOKEN")

	f, err := Load(writeSample(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	reg, err := BuildRegistry(f, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if reg.Default() != "fast" {
		t.Fatalf("default: %q", reg.Default())
	}
	for _, name := range []string{"fast", "smart", "google"} {
		if _, ok := reg.Get(name); !ok {
			t.Fatalf("missing adapter %q", name)
		}
	}
}

func TestLoadMissingDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(p, []byte(`{"default":"nope","models":{"a":{"dialect":"openai","base_url":"x","model":"y"}}}`), 0o600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected error for missing default")
	}
}

func TestUnknownDialect(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.json")
	_ = os.WriteFile(p, []byte(`{"default":"a","models":{"a":{"dialect":"alien","base_url":"x","model":"y"}}}`), 0o600)
	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if _, err := BuildRegistry(f, ""); err == nil {
		t.Fatal("expected error for unknown dialect")
	}
}

func TestExpandEnvSet(t *testing.T) {
	os.Setenv("MY_VAR", "hello")
	defer os.Unsetenv("MY_VAR")

	result, err := expandEnv("prefix-${env: MY_VAR}-suffix")
	if err != nil {
		t.Fatalf("expandEnv: %v", err)
	}
	if result != "prefix-hello-suffix" {
		t.Fatalf("got %q, want %q", result, "prefix-hello-suffix")
	}
}

func TestExpandEnvUnset(t *testing.T) {
	os.Unsetenv("NONEXISTENT_VAR")

	_, err := expandEnv("${env: NONEXISTENT_VAR}")
	if err == nil {
		t.Fatal("expected error for unset env var")
	}
}

func TestExpandEnvMultiple(t *testing.T) {
	os.Setenv("A", "foo")
	os.Setenv("B", "bar")
	defer os.Unsetenv("A")
	defer os.Unsetenv("B")

	result, err := expandEnv("${env: A}-${env: B}")
	if err != nil {
		t.Fatalf("expandEnv: %v", err)
	}
	if result != "foo-bar" {
		t.Fatalf("got %q, want %q", result, "foo-bar")
	}
}

func TestExpandEnvNoPlaceholders(t *testing.T) {
	result, err := expandEnv("plain string without env vars")
	if err != nil {
		t.Fatalf("expandEnv: %v", err)
	}
	if result != "plain string without env vars" {
		t.Fatalf("got %q, want %q", result, "plain string without env vars")
	}
}

func TestExpandEnvEmptyVarName(t *testing.T) {
	result, err := expandEnv("${env: }")
	if err != nil {
		t.Fatalf("expandEnv: %v", err)
	}
	// The regex requires at least one word char for the var name,
	// so the placeholder is left as-is.
	if result != "${env: }" {
		t.Fatalf("got %q, want %q", result, "${env: }")
	}
}

func TestLoadFailsOnMissingEnvVar(t *testing.T) {
	os.Unsetenv("HAKKA_ENDPOINT")
	os.Unsetenv("HAKKA_TOKEN")

	_, err := Load(writeSample(t))
	if err == nil {
		t.Fatal("expected error when env var is missing")
	}
}

func TestExpandEnvInMCPServerConfig(t *testing.T) {
	os.Setenv("MCP_SCRIPT", "/path/to/script.py")
	os.Setenv("MCP_API_KEY", "sk-abc123")
	defer os.Unsetenv("MCP_SCRIPT")
	defer os.Unsetenv("MCP_API_KEY")

	const mcpSample = `{
	  "default": "m",
	  "models": {
		"m": {
		  "dialect": "openai",
		  "base_url": "http://localhost",
		  "model": "test"
		}
	  },
	  "mcp_servers": {
		"my_server": {
		  "command": "${env: MCP_SCRIPT}",
		  "args": ["--mode", "stdio", "--key", "${env: MCP_API_KEY}"],
		  "env": {
			"MY_SECRET": "${env: MCP_API_KEY}"
		  },
		  "url": "http://${env: MCP_API_KEY}.example.com",
		  "headers": {
			"X-API-Key": "${env: MCP_API_KEY}"
		  }
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "mcp_config.json")
	if err := os.WriteFile(p, []byte(mcpSample), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	mcpCfg, ok := f.MCPServers["my_server"]
	if !ok {
		t.Fatal("mcp_server 'my_server' not found")
	}

	if mcpCfg.Command != "/path/to/script.py" {
		t.Errorf("command: got %q, want %q", mcpCfg.Command, "/path/to/script.py")
	}
	if len(mcpCfg.Args) != 4 {
		t.Fatalf("args: got %v (len=%d)", mcpCfg.Args, len(mcpCfg.Args))
	}
	if mcpCfg.Args[3] != "sk-abc123" {
		t.Errorf("args[3]: got %q, want %q", mcpCfg.Args[3], "sk-abc123")
	}
	if mcpCfg.Env["MY_SECRET"] != "sk-abc123" {
		t.Errorf("env[MY_SECRET]: got %q, want %q", mcpCfg.Env["MY_SECRET"], "sk-abc123")
	}
	if mcpCfg.URL != "http://sk-abc123.example.com" {
		t.Errorf("url: got %q, want %q", mcpCfg.URL, "http://sk-abc123.example.com")
	}
	if mcpCfg.Headers["X-API-Key"] != "sk-abc123" {
		t.Errorf("headers[X-API-Key]: got %q, want %q", mcpCfg.Headers["X-API-Key"], "sk-abc123")
	}
}

func TestBuildRegistryOpenAIExtra(t *testing.T) {
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_TOKEN")

	const cfgWithExtra = `{
	  "default": "openrouter",
	  "models": {
		"openrouter": {
		  "dialect": "openai",
		  "base_url": "https://openrouter.ai/api/v1",
		  "model": "anthropic/claude-3.5-sonnet",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"},
		  "extra": {"session_id": "$session_id", "provider": "openrouter"}
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "config_extra.json")
	if err := os.WriteFile(p, []byte(cfgWithExtra), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reg, err := BuildRegistry(f, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	ad, ok := reg.Get("openrouter")
	if !ok {
		t.Fatal("adapter 'openrouter' not found")
	}

	oa, ok := ad.(*adapters.OpenAIAdapter)
	if !ok {
		t.Fatalf("expected *adapters.OpenAIAdapter, got %T", ad)
	}

	if oa.Config.Extra == nil {
		t.Fatal("expected Extra to be set")
	}
	if oa.Config.Extra["session_id"] != "$session_id" {
		t.Errorf("extra.session_id: got %v, want $session_id", oa.Config.Extra["session_id"])
	}
	if oa.Config.Extra["provider"] != "openrouter" {
		t.Errorf("extra.provider: got %v, want openrouter", oa.Config.Extra["provider"])
	}
}

func TestExpandEnvInMCPServerConfigFailsOnMissing(t *testing.T) {
	os.Unsetenv("MCP_SCRIPT")

	const mcpSample = `{
	  "default": "m",
	  "models": {
		"m": {
		  "dialect": "openai",
		  "base_url": "http://localhost",
		  "model": "test"
		}
	  },
	  "mcp_servers": {
		"my_server": {
		  "command": "${env: MCP_SCRIPT}"
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "mcp_config.json")
	if err := os.WriteFile(p, []byte(mcpSample), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error when MCP env var is missing")
	}
}

func TestBuildRegistry_RetryConfig(t *testing.T) {
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_TOKEN")

	const cfgWithRetry = `{
	  "default": "unstable",
	  "models": {
		"unstable": {
		  "dialect": "openai",
		  "base_url": "https://unstable.example.com/v1",
		  "model": "unstable-model",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"},
		  "retry_config": {
			"max_attempts": 60,
			"base_delay": "10s",
			"max_delay": "60s",
			"backoff_factor": 1.5
		  }
		},
		"default-retry": {
		  "dialect": "anthropic",
		  "base_url": "https://example.com/v1",
		  "model": "default-model",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"}
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "config_retry.json")
	if err := os.WriteFile(p, []byte(cfgWithRetry), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reg, err := BuildRegistry(f, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Model with retry_config should have custom values on the adapter
	ad, ok := reg.Get("unstable")
	if !ok {
		t.Fatal("adapter 'unstable' not found")
	}
	oa, ok := ad.(*adapters.OpenAIAdapter)
	if !ok {
		t.Fatalf("expected *adapters.OpenAIAdapter, got %T", ad)
	}
	rc := oa.Config.RetryPolicy()
	if rc.MaxAttempts != 60 {
		t.Errorf("MaxAttempts: got %d, want 60", rc.MaxAttempts)
	}
	if rc.BaseDelay != 10_000_000_000 {
		t.Errorf("BaseDelay: got %v, want 10s", rc.BaseDelay)
	}
	if rc.MaxDelay != 60_000_000_000 {
		t.Errorf("MaxDelay: got %v, want 60s", rc.MaxDelay)
	}
	if rc.BackoffFactor != 1.5 {
		t.Errorf("BackoffFactor: got %f, want 1.5", rc.BackoffFactor)
	}

	// Model without retry_config should get defaults (zero values → defaults on use)
	ad2, ok := reg.Get("default-retry")
	if !ok {
		t.Fatal("adapter 'default-retry' not found")
	}
	aa, ok := ad2.(*adapters.AnthropicAdapter)
	if !ok {
		t.Fatalf("expected *adapters.AnthropicAdapter, got %T", ad2)
	}
	arc := aa.Config.RetryPolicy()
	if arc.MaxAttempts != 0 || arc.BaseDelay != 0 || arc.BackoffFactor != 0 {
		t.Errorf("expected zero-value RetryConfig for model without retry_config, got %+v", arc)
	}
}

func TestBuildRegistryDeepSeekDialect(t *testing.T) {
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_TOKEN")

	const cfg = `{
	  "default": "ds",
	  "models": {
		"ds": {
		  "dialect": "deepseek",
		  "base_url": "https://deepseek.example.com",
		  "model": "deepseek-v4-flash",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"},
		  "extra": {"thinking": {"type": "enabled"}}
		},
		"ds-default": {
		  "dialect": "deepseek",
		  "model": "deepseek-v4-pro",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"}
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "config_deepseek.json")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reg, err := BuildRegistry(f, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	ad, ok := reg.Get("ds")
	if !ok {
		t.Fatal("adapter 'ds' not found")
	}
	da, ok := ad.(*adapters.DeepSeekAdapter)
	if !ok {
		t.Fatalf("expected *adapters.DeepSeekAdapter, got %T", ad)
	}
	if da.BaseURL != "https://deepseek.example.com" {
		t.Errorf("BaseURL: got %q, want https://deepseek.example.com", da.BaseURL)
	}
	if da.Model != "deepseek-v4-flash" {
		t.Errorf("Model: got %q, want deepseek-v4-flash", da.Model)
	}

	// Model without base_url should fall back to the DeepSeek default.
	ad2, ok := reg.Get("ds-default")
	if !ok {
		t.Fatal("adapter 'ds-default' not found")
	}
	da2, ok := ad2.(*adapters.DeepSeekAdapter)
	if !ok {
		t.Fatalf("expected *adapters.DeepSeekAdapter, got %T", ad2)
	}
	if da2.BaseURL != "https://api.deepseek.com" {
		t.Errorf("default BaseURL: got %q, want https://api.deepseek.com", da2.BaseURL)
	}
}

func TestBuildRegistry_ModelCompactSoftLimit(t *testing.T) {
	os.Setenv("HAKKA_TOKEN", "test-token")
	defer os.Unsetenv("HAKKA_TOKEN")

	const cfgWithLimit = `{
	  "default": "high-limit",
	  "models": {
		"high-limit": {
		  "dialect": "openai",
		  "base_url": "https://example.com/v1",
		  "model": "test-model",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"},
		  "compact_soft_limit": 50000
		},
		"no-limit": {
		  "dialect": "openai",
		  "base_url": "https://example.com/v1",
		  "model": "test-model-2",
		  "headers": {"Authorization": "Bearer ${env: HAKKA_TOKEN}"}
		}
	  }
	}`

	p := filepath.Join(t.TempDir(), "config_limit.json")
	if err := os.WriteFile(p, []byte(cfgWithLimit), 0o600); err != nil {
		t.Fatal(err)
	}

	f, err := Load(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	reg, err := BuildRegistry(f, "")
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	// Model with compact_soft_limit should have it in the profile
	profile, ok := reg.GetProfile("high-limit")
	if !ok {
		t.Fatal("expected profile for 'high-limit'")
	}
	if profile.CompactSoftLimit != 50000 {
		t.Fatalf("expected CompactSoftLimit=50000, got %d", profile.CompactSoftLimit)
	}

	// Model without compact_soft_limit should have 0
	profile2, ok := reg.GetProfile("no-limit")
	if !ok {
		t.Fatal("expected profile for 'no-limit'")
	}
	if profile2.CompactSoftLimit != 0 {
		t.Fatalf("expected CompactSoftLimit=0 for model without limit, got %d", profile2.CompactSoftLimit)
	}
}

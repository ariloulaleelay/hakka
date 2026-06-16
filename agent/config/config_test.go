package config

import (
	"os"
	"path/filepath"
	"testing"
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

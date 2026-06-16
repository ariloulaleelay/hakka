package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/you/hakka/agent"
	"github.com/you/hakka/agent/adapters"
)

var envVarRe = regexp.MustCompile(`\$\{env:\s*(\w+)\s*\}`)

// ModelConfig describes a single named LLM endpoint.
type ModelConfig struct {
	Dialect string            `json:"dialect"`           // openai | anthropic | gemini
	BaseURL string            `json:"base_url"`          // provider base URL
	Model   string            `json:"model"`             // provider model id
	Headers map[string]string `json:"headers,omitempty"` // extra HTTP headers
	Extra   map[string]any    `json:"extra,omitempty"`   // provider-specific knobs (anthropic_version, max_tokens, ...)
}

// File is the on-disk shape of the configuration.
type File struct {
	Default    string                          `json:"default"`
	Models     map[string]ModelConfig          `json:"models"`
	MCPServers map[string]MCPServerConfig      `json:"mcp_servers,omitempty"`
}

// MCPServerConfig describes a single MCP server endpoint.
type MCPServerConfig struct {
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// Load reads and parses a JSON config file. All ${env: VAR_NAME} placeholders
// in model config fields are expanded from the environment. Returns an error
// if any referenced environment variable is unset.
func Load(path string) (*File, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	var cfgFile File
	if err := json.Unmarshal(raw, &cfgFile); err != nil {
		return nil, fmt.Errorf("config parse: %w", err)
	}
	if len(cfgFile.Models) == 0 {
		return nil, fmt.Errorf("config: no models defined")
	}
	if cfgFile.Default == "" {
		for name := range cfgFile.Models {
			cfgFile.Default = name
			break
		}
	}
	if _, ok := cfgFile.Models[cfgFile.Default]; !ok {
		return nil, fmt.Errorf("config: default %q not present in models", cfgFile.Default)
	}

	// Expand environment variables in all model configs
	for name, modelCfg := range cfgFile.Models {
		expanded, err := expandModelConfig(modelCfg)
		if err != nil {
			return nil, fmt.Errorf("config: model %q: %w", name, err)
		}
		cfgFile.Models[name] = expanded
	}

	// Expand environment variables in all MCP server configs
	for name, mcpCfg := range cfgFile.MCPServers {
		expanded, err := expandMCPServerConfig(mcpCfg)
		if err != nil {
			return nil, fmt.Errorf("config: mcp_server %q: %w", name, err)
		}
		cfgFile.MCPServers[name] = expanded
	}

	return &cfgFile, nil
}

// expandModelConfig expands ${env: VAR_NAME} placeholders in all string fields
// of a ModelConfig.
func expandModelConfig(cfg ModelConfig) (ModelConfig, error) {
	var err error

	cfg.BaseURL, err = expandEnv(cfg.BaseURL)
	if err != nil {
		return cfg, fmt.Errorf("base_url: %w", err)
	}

	cfg.Model, err = expandEnv(cfg.Model)
	if err != nil {
		return cfg, fmt.Errorf("model: %w", err)
	}

	expandedHeaders := make(map[string]string, len(cfg.Headers))
	for key, value := range cfg.Headers {
		// Expand both key and value (allows dynamic header names if needed)
		expKey, err := expandEnv(key)
		if err != nil {
			return cfg, fmt.Errorf("header key %q: %w", key, err)
		}
		expValue, err := expandEnv(value)
		if err != nil {
			return cfg, fmt.Errorf("header %q: %w", key, err)
		}
		expandedHeaders[expKey] = expValue
	}
	cfg.Headers = expandedHeaders

	return cfg, nil
}

// expandMCPServerConfig expands ${env: VAR_NAME} placeholders in all string fields
// of an MCPServerConfig.
func expandMCPServerConfig(cfg MCPServerConfig) (MCPServerConfig, error) {
	var err error

	cfg.Command, err = expandEnv(cfg.Command)
	if err != nil {
		return cfg, fmt.Errorf("command: %w", err)
	}

	cfg.URL, err = expandEnv(cfg.URL)
	if err != nil {
		return cfg, fmt.Errorf("url: %w", err)
	}

	expandedArgs := make([]string, len(cfg.Args))
	for i, arg := range cfg.Args {
		expandedArgs[i], err = expandEnv(arg)
		if err != nil {
			return cfg, fmt.Errorf("args[%d]: %w", i, err)
		}
	}
	cfg.Args = expandedArgs

	expandedEnv := make(map[string]string, len(cfg.Env))
	for key, value := range cfg.Env {
		expKey, err := expandEnv(key)
		if err != nil {
			return cfg, fmt.Errorf("env key %q: %w", key, err)
		}
		expValue, err := expandEnv(value)
		if err != nil {
			return cfg, fmt.Errorf("env %q: %w", key, err)
		}
		expandedEnv[expKey] = expValue
	}
	cfg.Env = expandedEnv

	expandedHeaders := make(map[string]string, len(cfg.Headers))
	for key, value := range cfg.Headers {
		expKey, err := expandEnv(key)
		if err != nil {
			return cfg, fmt.Errorf("header key %q: %w", key, err)
		}
		expValue, err := expandEnv(value)
		if err != nil {
			return cfg, fmt.Errorf("header %q: %w", key, err)
		}
		expandedHeaders[expKey] = expValue
	}
	cfg.Headers = expandedHeaders

	return cfg, nil
}

// expandEnv replaces all occurrences of ${env: VAR_NAME} with the value of
// the environment variable VAR_NAME. Returns an error if any referenced
// variable is empty or unset.
func expandEnv(value string) (string, error) {
	matches := envVarRe.FindAllStringSubmatch(value, -1)
	if len(matches) == 0 {
		return value, nil
	}

	result := value
	for _, m := range matches {
		varName := m[1]
		if varName == "" {
			return "", fmt.Errorf("empty variable name in %q", m[0])
		}
		envVal := os.Getenv(varName)
		if envVal == "" {
			return "", fmt.Errorf("environment variable %q is unset or empty", varName)
		}
		result = strings.ReplaceAll(result, m[0], envVal)
	}
	return result, nil
}

func newHTTPClient(headers map[string]string) *http.Client {
	return &http.Client{Transport: &headerTransport{
		base:    http.DefaultTransport,
		headers: headers,
	}}
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t *headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	for key, value := range t.headers {
		request.Header.Set(key, value)
	}
	return t.base.RoundTrip(request)
}

// MCPServerConfigs returns a map of MCP server name to server config.
// Returns nil if no MCP servers are configured.
func (f *File) MCPServerConfigs() map[string]MCPServerConfig {
	return f.MCPServers
}

// BuildRegistry constructs an agent.Registry from a parsed config file.
// Each model entry is registered under its config key. The default selection
// from the file is applied.
func BuildRegistry(cfgFile *File, llmDebugDir string) (*agent.Registry, error) {
	reg := agent.NewRegistry()
	for name, modelCfg := range cfgFile.Models {
		client := newHTTPClient(modelCfg.Headers)
		adapter, err := buildAdapter(name, modelCfg, client, llmDebugDir)
		if err != nil {
			return nil, err
		}
		reg.Register(name, adapter)
	}
	if err := reg.SetDefault(cfgFile.Default); err != nil {
		return nil, err
	}
	return reg, nil
}

func buildAdapter(name string, modelCfg ModelConfig, client *http.Client, llmDebugDir string) (agent.LLMAdapter, error) {
	switch strings.ToLower(modelCfg.Dialect) {
	case "openai":
		cfg := openai.DefaultConfig("dummy-key")
		cfg.BaseURL = modelCfg.BaseURL
		cfg.HTTPClient = client
		adapter := adapters.NewOpenAIAdapter(openai.NewClientWithConfig(cfg), modelCfg.Model)
		adapter.LLMDebugDir = llmDebugDir
		return adapter, nil
	case "anthropic":
		adapter := adapters.NewAnthropicAdapter(client, modelCfg.BaseURL, modelCfg.Model)
		if version, ok := modelCfg.Extra["anthropic_version"].(string); ok && version != "" {
			adapter.Version = version
		}
		if maxTokens, ok := modelCfg.Extra["max_tokens"].(float64); ok {
			adapter.MaxTokens = int(maxTokens)
		}
		return adapter, nil
	case "gemini", "google":
		return adapters.NewGeminiAdapter(client, modelCfg.BaseURL, modelCfg.Model), nil
	default:
		return nil, fmt.Errorf("config: model %q: unknown dialect %q", name, modelCfg.Dialect)
	}
}

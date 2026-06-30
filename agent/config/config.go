package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/adapters"
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
	Default     string                          `json:"default"`
	Models      map[string]ModelConfig          `json:"models"`
	MCPServers  map[string]MCPServerConfig      `json:"mcp_servers,omitempty"`
	FeedbackURL string                          `json:"feedback_url,omitempty"`
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

	for name, modelCfg := range cfgFile.Models {
		var e envExpander
		e.String(&modelCfg.BaseURL, "base_url")
		e.String(&modelCfg.Model, "model")
		e.Map(modelCfg.Headers, "header")
		if e.err != nil {
			return nil, fmt.Errorf("config: model %q: %w", name, e.err)
		}
		cfgFile.Models[name] = modelCfg
	}

	for name, mcpCfg := range cfgFile.MCPServers {
		var e envExpander
		e.String(&mcpCfg.Command, "command")
		e.String(&mcpCfg.URL, "url")
		e.Slice(mcpCfg.Args, "args")
		e.Map(mcpCfg.Env, "env")
		e.Map(mcpCfg.Headers, "header")
		if e.err != nil {
			return nil, fmt.Errorf("config: mcp_server %q: %w", name, e.err)
		}
		cfgFile.MCPServers[name] = mcpCfg
	}

	// Expand env vars in feedback_url if set.
	if cfgFile.FeedbackURL != "" {
		var e envExpander
		e.String(&cfgFile.FeedbackURL, "feedback_url")
		if e.err != nil {
			return nil, fmt.Errorf("config: %w", e.err)
		}
	}

	return &cfgFile, nil
}

// envExpander batches multiple environment-variable expansions so that the
// first error stops all subsequent expansions. Callers check e.err once
// after all expansions are queued.
type envExpander struct {
	err error
}

// String expands a pointer-to-string field. label is used in error messages
// (e.g. "base_url").
func (e *envExpander) String(dst *string, label string) {
	if e.err != nil {
		return
	}
	*dst, e.err = expandEnv(*dst)
	if e.err != nil {
		e.err = fmt.Errorf("%s: %w", label, e.err)
	}
}

// Slice expands every element of a string slice. label is used in error
// messages (e.g. "args").
func (e *envExpander) Slice(s []string, label string) {
	if e.err != nil {
		return
	}
	for i, v := range s {
		s[i], e.err = expandEnv(v)
		if e.err != nil {
			e.err = fmt.Errorf("%s[%d]: %w", label, i, e.err)
			return
		}
	}
}

// Map expands every key and value of a string map in-place. label is used
// in error messages (e.g. "header", "env").
func (e *envExpander) Map(m map[string]string, label string) {
	if e.err != nil {
		return
	}
	for k, v := range m {
		ek, err := expandEnv(k)
		if err != nil {
			e.err = fmt.Errorf("%s key %q: %w", label, k, err)
			return
		}
		ev, err := expandEnv(v)
		if err != nil {
			e.err = fmt.Errorf("%s %q: %w", label, k, err)
			return
		}
		delete(m, k)
		m[ek] = ev
	}
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

// FeedbackEndpoint returns the configured feedback URL, or empty string if not set.
func (f *File) FeedbackEndpoint() string {
	return f.FeedbackURL
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
		// Wrap transport to inject extra body fields (e.g. session_id for OpenRouter).
		client.Transport = adapters.WrapTransport(client.Transport, modelCfg.Extra, llmDebugDir)
		cfg.HTTPClient = client
		adapter := adapters.NewOpenAIAdapter(openai.NewClientWithConfig(cfg), modelCfg.Model)
		adapter.LLMDebugDir = llmDebugDir
		adapter.Extra = modelCfg.Extra
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

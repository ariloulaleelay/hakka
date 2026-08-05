package config

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	openai "github.com/sashabaranov/go-openai"

	"github.com/ariloulaleelay/hakka/agent"
	"github.com/ariloulaleelay/hakka/agent/adapters"
)

var envVarRe = regexp.MustCompile(`\$\{env:\s*(\w+)\s*\}`)

// RetryConfigRaw is the JSON-serializable form of agent.RetryConfig.
// Duration fields are specified as Go duration strings (e.g. "10s", "250ms").
type RetryConfigRaw struct {
	MaxAttempts   int     `json:"max_attempts,omitempty"`
	BaseDelay     string  `json:"base_delay,omitempty"`
	MaxDelay      string  `json:"max_delay,omitempty"`
	BackoffFactor float64 `json:"backoff_factor,omitempty"`
}

// ModelConfig describes a single named LLM endpoint.
type ModelConfig struct {
	Dialect          string            `json:"dialect"`                      // openai | anthropic | gemini | deepseek | openai-responses
	BaseURL          string            `json:"base_url"`                     // provider base URL
	Model            string            `json:"model"`                        // provider model id
	Headers          map[string]string `json:"headers,omitempty"`            // extra HTTP headers
	Extra            map[string]any    `json:"extra,omitempty"`              // provider-specific knobs (anthropic_version, max_tokens, ...)
	Pricing          *agent.Pricing    `json:"pricing,omitempty"`            // per-token pricing for cost calculation when provider doesn't return cost
	CompactSoftLimit int               `json:"compact_soft_limit,omitempty"` // per-provider compact soft limit (0 = use engine default)
	Hacks            agent.Hacks       `json:"hacks,omitempty"`              // per-provider workarounds
	RetryConfig      *RetryConfigRaw   `json:"retry_config,omitempty"`       // per-provider retry policy
	Quota            *agent.QuotaConfig `json:"quota,omitempty"`              // per-provider quota/balance API
}

type File struct {
	Default     string                     `json:"default"`
	Models      map[string]ModelConfig     `json:"models"`
	MCPServers  map[string]MCPServerConfig `json:"mcp_servers,omitempty"`
	FeedbackURL string                     `json:"feedback_url,omitempty"`
}

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

// toAgent converts the raw config to an agent.RetryConfig, parsing duration
// strings into time.Duration values. Returns the zero value if raw is nil.
func (r *RetryConfigRaw) toAgent() agent.RetryConfig {
	if r == nil {
		return agent.RetryConfig{}
	}
	rc := agent.RetryConfig{
		MaxAttempts:   r.MaxAttempts,
		BackoffFactor: r.BackoffFactor,
	}
	if r.BaseDelay != "" {
		if d, err := time.ParseDuration(r.BaseDelay); err == nil {
			rc.BaseDelay = d
		}
	}
	if r.MaxDelay != "" {
		if d, err := time.ParseDuration(r.MaxDelay); err == nil {
			rc.MaxDelay = d
		}
	}
	return rc
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
		// Propagate compact_soft_limit to the model profile
		if modelCfg.CompactSoftLimit > 0 {
			reg.SetProfileCompactSoftLimit(name, modelCfg.CompactSoftLimit)
		}
		// Propagate hacks to the model profile
		if modelCfg.Hacks.IgnoreStopIfNoContent != nil {
			reg.SetProfileHacks(name, modelCfg.Hacks)
		}
	}
	if err := reg.SetDefault(cfgFile.Default); err != nil {
		return nil, err
	}
	return reg, nil
}

func buildAdapter(name string, modelCfg ModelConfig, client *http.Client, llmDebugDir string) (agent.LLMAdapter, error) {
	retryCfg := modelCfg.RetryConfig.toAgent()
	var pricing agent.Pricing
	if modelCfg.Pricing != nil {
		pricing = *modelCfg.Pricing
	}

	switch strings.ToLower(modelCfg.Dialect) {
	case "openai":
		cfg := openai.DefaultConfig("dummy-key")
		cfg.BaseURL = modelCfg.BaseURL
		client.Transport = adapters.WrapTransport(client.Transport, modelCfg.Extra, llmDebugDir)
		cfg.HTTPClient = client
		adCfg := adapters.NewOpenAIConfig(llmDebugDir, retryCfg, pricing, modelCfg.Extra)
		adCfg.HTTPClient = client
		adCfg.Quota = modelCfg.Quota
		adapter := adapters.NewOpenAIAdapter(openai.NewClientWithConfig(cfg), modelCfg.Model, adCfg)
		return adapter, nil
	case "anthropic":
		version := ""
		if v, ok := modelCfg.Extra["anthropic_version"].(string); ok {
			version = v
		}
		maxTokens := 0
		if v, ok := modelCfg.Extra["max_tokens"].(float64); ok {
			maxTokens = int(v)
		}
		adCfg := adapters.NewAnthropicConfig(llmDebugDir, retryCfg, pricing, version, maxTokens)
		adCfg.Quota = modelCfg.Quota
		adapter := adapters.NewAnthropicAdapter(client, modelCfg.BaseURL, modelCfg.Model, adCfg)
		return adapter, nil
	case "gemini", "google":
		adCfg := adapters.NewGeminiConfig(llmDebugDir, retryCfg, pricing)
		adCfg.Quota = modelCfg.Quota
		adapter := adapters.NewGeminiAdapter(client, modelCfg.BaseURL, modelCfg.Model, adCfg)
		return adapter, nil
	case "deepseek":
		client.Transport = adapters.WrapTransport(client.Transport, modelCfg.Extra, llmDebugDir)
		adCfg := adapters.NewDeepSeekConfig(llmDebugDir, retryCfg, pricing, modelCfg.Extra)
		adCfg.Quota = modelCfg.Quota
		adapter := adapters.NewDeepSeekAdapter(client, modelCfg.BaseURL, modelCfg.Model, adCfg)
		return adapter, nil
	case "openai-responses":
		adCfg := adapters.NewOpenAIResponsesConfig(llmDebugDir, retryCfg, pricing)
		adCfg.Quota = modelCfg.Quota
		adapter := adapters.NewOpenAIResponsesAdapter(client, modelCfg.BaseURL, modelCfg.Model, adCfg)
		return adapter, nil
	default:
		return nil, fmt.Errorf("config: model %q: unknown dialect %q", name, modelCfg.Dialect)
	}
}

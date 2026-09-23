// Package settings resolves the app configuration from one global
// ~/.zua/settings.json. Every field follows the same precedence: CLI flag /
// request value > OPENCODE_* environment variable > settings file > built-in
// default. The model and base-URL defaults are provider-aware: they apply
// only when the resolved provider is opencode-go, leaving other providers
// with their own client defaults.
package settings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	DefaultProvider = "opencode-go"
	OpenCodeBaseURL = "https://opencode.ai/zen/go/v1"
	DefaultModel    = "glm-5.3-flash"
	DefaultThinking = "high"
)

// Settings is the resolved configuration for one run.
type Settings struct {
	Provider      string `json:"provider"`
	APIKey        string `json:"api_key"`
	BaseURL       string `json:"base_url"`
	Model         string `json:"model"`
	ThinkingLevel string `json:"thinking_level"`
}

// Path is the global settings file path under the given HOME.
func Path(home string) string {
	return filepath.Join(home, ".zua", "settings.json")
}

// apiKeyEnv maps a resolved provider to the environment variable that
// carries its API key (the env tier of the precedence chain).
func apiKeyEnv(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_API_KEY"
	case "commandcode":
		return "COMMANDCODE_API_KEY"
	case "openrouter":
		return "OPENROUTER_API_KEY"
	case "fireworks":
		return "FIREWORKS_AI_API_KEY"
	case "opencode-go":
		return "OPENCODE_API_KEY"
	}
	return ""
}

// baseURLEnv maps a resolved provider to the environment variable that
// overrides its base URL, if one exists.
func baseURLEnv(provider string) string {
	switch provider {
	case "openai":
		return "OPENAI_BASE_URL"
	case "opencode-go":
		return "OPENCODE_BASE_URL"
	}
	return ""
}

// Load resolves settings with flag > env > file > default precedence.
// overrides carries CLI-flag (or request-provided) values — empty fields are
// ignored; getenv and readFile are injected so the resolution is hermetic in
// tests. A missing settings file is not an error: defaults apply.
func Load(overrides Settings, getenv func(string) string, readFile func(string) ([]byte, error)) (Settings, error) {
	home := getenv("HOME")
	path := Path(home)

	var file Settings
	if contents, err := readFile(path); err == nil {
		if err := json.Unmarshal(contents, &file); err != nil {
			return Settings{}, fmt.Errorf("%s: %w", path, err)
		}
	} else if !isNotExist(err) {
		return Settings{}, fmt.Errorf("%s: %w", path, err)
	}

	resolved := Settings{
		Provider:      FirstNonEmpty(overrides.Provider, getenv("OPENCODE_PROVIDER"), file.Provider, DefaultProvider),
		ThinkingLevel: FirstNonEmpty(overrides.ThinkingLevel, getenv("OPENCODE_THINKING"), file.ThinkingLevel, DefaultThinking),
	}
	resolved.APIKey = FirstNonEmpty(overrides.APIKey, getenv(apiKeyEnv(resolved.Provider)), file.APIKey)
	resolved.BaseURL = FirstNonEmpty(overrides.BaseURL, getenv(baseURLEnv(resolved.Provider)), file.BaseURL, providerDefaultBaseURL(resolved.Provider))
	resolved.Model = FirstNonEmpty(overrides.Model, getenv("OPENCODE_MODEL"), file.Model, providerDefaultModel(resolved.Provider))
	return resolved, nil
}

// providerDefaultBaseURL is the built-in base URL for the default provider;
// other providers keep their client's built-in URL (empty here).
func providerDefaultBaseURL(provider string) string {
	if provider == DefaultProvider {
		return OpenCodeBaseURL
	}
	return ""
}

// providerDefaultModel is the built-in model for the default provider;
// other providers fall back to their client-side default model (empty here).
func providerDefaultModel(provider string) string {
	if provider == DefaultProvider {
		return DefaultModel
	}
	return ""
}

// isNotExist reports whether err means "no settings file".
func isNotExist(err error) bool {
	return err == nil || os.IsNotExist(err)
}

// FirstNonEmpty returns the first non-empty string, or "". Exported for
// consumers that merge optional values with the same convention.
func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

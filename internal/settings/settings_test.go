package settings

import (
	"os"
	"strings"
	"testing"
)

// fixture builds a getenv fake with HOME set to a temp dir and a readFile
// fake serving contents at the canonical settings path inside that home.
// Extra env entries are merged over the HOME entry. Empty contents means
// "no settings file".
func fixture(t *testing.T, contents string, extraEnv map[string]string) (string, func(string) string, func(string) ([]byte, error)) {
	t.Helper()
	home := t.TempDir()
	if extraEnv == nil {
		extraEnv = map[string]string{}
	}
	extraEnv["HOME"] = home
	return home, env(extraEnv), readFileFor(home, contents)
}

// env builds a getenv fake from a map; unset keys return "".
func env(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

// readFileFor builds a readFile fake serving one file at the canonical
// settings path under the given HOME.
func readFileFor(home, contents string) func(string) ([]byte, error) {
	path := Path(home)
	return func(p string) ([]byte, error) {
		if p == path {
			if contents == "" {
				return nil, os.ErrNotExist
			}
			return []byte(contents), nil
		}
		return nil, os.ErrNotExist
	}
}

func TestLoadDefaultsWithoutFileOrEnv(t *testing.T) {
	_, getenv, readFile := fixture(t, "", nil)
	got, err := Load(Settings{}, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Settings{
		Provider:      "opencode-go",
		BaseURL:       "https://opencode.ai/zen/go/v1",
		Model:         "glm-5.3-flash",
		ThinkingLevel: "high",
	}
	if got != want {
		t.Fatalf("defaults mismatch:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadFileValues(t *testing.T) {
	file := `{"provider":"openai","api_key":"sk-file","base_url":"https://file.example/v1","model":"file-model","thinking_level":"low"}`
	_, getenv, readFile := fixture(t, file, nil)
	got, err := Load(Settings{}, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Provider != "openai" || got.APIKey != "sk-file" || got.BaseURL != "https://file.example/v1" ||
		got.Model != "file-model" || got.ThinkingLevel != "low" {
		t.Fatalf("file values not applied: %+v", got)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	file := `{"provider":"openai","api_key":"sk-file","base_url":"https://file.example/v1","model":"file-model","thinking_level":"low"}`
	_, getenv, readFile := fixture(t, file, map[string]string{
		"OPENAI_API_KEY":     "sk-env",
		"OPENCODE_PROVIDER":  "openrouter",
		"OPENROUTER_API_KEY": "sk-router",
		"OPENCODE_MODEL":     "env-model",
	})
	got, err := Load(Settings{}, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.APIKey != "sk-router" || got.Provider != "openrouter" || got.Model != "env-model" {
		t.Fatalf("env did not override file: %+v", got)
	}
	// The provider env for base URLs only exists for openai/opencode-go:
	// openrouter has none, so the file base URL survives.
	if got.BaseURL != "https://file.example/v1" {
		t.Fatalf("file base URL lost without an env override: %+v", got)
	}
	// No thinking-level env exists: the file value survives.
	if got.ThinkingLevel != "low" {
		t.Fatalf("thinking level from file lost: %+v", got)
	}
}

func TestLoadThinkingEnvOverridesFile(t *testing.T) {
	file := `{"thinking_level":"low"}`
	_, getenv, readFile := fixture(t, file, map[string]string{"OPENCODE_THINKING": "max"})
	got, err := Load(Settings{}, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ThinkingLevel != "max" {
		t.Fatalf("OPENCODE_THINKING did not override file: %+v", got)
	}
}

func TestLoadOverridesBeatEnvAndFile(t *testing.T) {
	file := `{"provider":"openai","api_key":"sk-file","base_url":"https://file.example/v1","model":"file-model","thinking_level":"low"}`
	flags := Settings{
		Provider:      "ollama",
		APIKey:        "sk-flag",
		BaseURL:       "https://flag.example/v1",
		Model:         "flag-model",
		ThinkingLevel: "max",
	}
	_, getenv, readFile := fixture(t, file, map[string]string{
		"OPENCODE_API_KEY":  "sk-env",
		"OPENCODE_PROVIDER": "openrouter",
		"OPENCODE_BASE_URL": "https://env.example/v1",
		"OPENCODE_MODEL":    "env-model",
	})
	got, err := Load(flags, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != flags {
		t.Fatalf("overrides did not win:\n got %+v\nwant %+v", got, flags)
	}
}

func TestLoadMalformedFileErrorsNamingPath(t *testing.T) {
	home, getenv, readFile := fixture(t, "{not json", nil)
	_, err := Load(Settings{}, getenv, readFile)
	if err == nil {
		t.Fatal("expected an error for malformed settings")
	}
	if !strings.Contains(err.Error(), Path(home)) {
		t.Fatalf("error must name the settings path:\n%v", err)
	}
}

func TestLoadMalformedErrorNeverContainsFileContent(t *testing.T) {
	_, getenv, readFile := fixture(t, `{"api_key":"sk-super-secret","broken":`, nil)
	_, err := Load(Settings{}, getenv, readFile)
	if err == nil {
		t.Fatal("expected an error for malformed settings")
	}
	if strings.Contains(err.Error(), "sk-super-secret") {
		t.Fatalf("error leaked file content (api key): %v", err)
	}
}

func TestLoadThinkingDefaultEvenWithFile(t *testing.T) {
	file := `{"provider":"openai"}`
	_, getenv, readFile := fixture(t, file, nil)
	got, err := Load(Settings{}, getenv, readFile)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.ThinkingLevel != "high" {
		t.Fatalf("thinking level default not applied: %+v", got)
	}
}

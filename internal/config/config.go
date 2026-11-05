package config

import (
	"bufio"
	"os"
	"strings"
)

type Config struct {
	Address        string
	BifrostBaseURL string
	BifrostAPIKey  string
	BifrostModel   string
	TavilyAPIKey   string
}

func Load() Config {
	loadDotEnv(".env")
	return Config{
		Address:        valueOr("ADDR", ":8000"),
		BifrostBaseURL: valueOr("BIFROST_BASE_URL", "http://localhost:8080/v1"),
		BifrostAPIKey:  valueOr("BIFROST_API_KEY", "local-development"),
		BifrostModel:   valueOr("BIFROST_MODEL", "openrouter/free"),
		TavilyAPIKey:   strings.TrimSpace(os.Getenv("TAVILY_API_KEY")),
	}
}

func valueOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

// loadDotEnv is deliberately small: the service has no third-party runtime dependencies.
// Values already present in the process environment always take precedence.
func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" {
			continue
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
}

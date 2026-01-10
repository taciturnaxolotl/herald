package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type AppConfig struct {
	Host            string     `yaml:"host"`
	SSHPort         int        `yaml:"ssh_port"`
	ExternalSSHPort int        `yaml:"external_ssh_port"`
	HTTPPort        int        `yaml:"http_port"`
	HostKeyPath     string     `yaml:"host_key_path"`
	DBPath          string     `yaml:"db_path"`
	Origin          string     `yaml:"origin"`
	SMTP            SMTPConfig `yaml:"smtp"`
	AllowAllKeys    bool       `yaml:"allow_all_keys"`
	AllowedKeys     []string   `yaml:"allowed_keys"`
}

type SMTPConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	Pass string `yaml:"pass"`
	From string `yaml:"from"`
}

func DefaultAppConfig() *AppConfig {
	return &AppConfig{
		Host:        "0.0.0.0",
		SSHPort:     2222,
		HTTPPort:    8080,
		HostKeyPath: "./host_key",
		DBPath:      "./herald.db",
		Origin:      "http://localhost:8080",
		SMTP: SMTPConfig{
			Host: "localhost",
			Port: 587,
			From: "herald@localhost",
		},
		AllowAllKeys: true,
	}
}

func LoadAppConfig(path string) (*AppConfig, error) {
	cfg := DefaultAppConfig()

	// Load .env file if it exists (silently ignore if not found)
	if envPath := findEnvFile(path); envPath != "" {
		_ = godotenv.Load(envPath)
	}

	if path != "" {
		data, err := os.ReadFile(path) //nolint:gosec // Config file path from CLI flag
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}

		expanded := os.Expand(string(data), func(key string) string {
			return os.Getenv(key)
		})

		if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	applyEnvOverrides(cfg)

	// Default external_ssh_port to ssh_port if not set
	if cfg.ExternalSSHPort == 0 {
		cfg.ExternalSSHPort = cfg.SSHPort
	}

	return cfg, nil
}

// findEnvFile looks for .env file in the config file's directory or current directory
func findEnvFile(configPath string) string {
	// If config path provided, look in its directory
	if configPath != "" {
		dir := filepath.Dir(configPath)
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			return envPath
		}
	}

	// Otherwise check current directory
	if _, err := os.Stat(".env"); err == nil {
		return ".env"
	}

	return ""
}

func applyEnvOverrides(cfg *AppConfig) {
	if v := os.Getenv("HERALD_HOST"); v != "" {
		cfg.Host = v
	}
	if v := os.Getenv("HERALD_SSH_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.SSHPort = port
		}
	}
	if v := os.Getenv("HERALD_EXTERNAL_SSH_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.ExternalSSHPort = port
		}
	}
	if v := os.Getenv("HERALD_HTTP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.HTTPPort = port
		}
	}
	if v := os.Getenv("HERALD_HOST_KEY_PATH"); v != "" {
		cfg.HostKeyPath = v
	}
	if v := os.Getenv("HERALD_DB_PATH"); v != "" {
		cfg.DBPath = v
	}
	if v := os.Getenv("HERALD_SMTP_HOST"); v != "" {
		cfg.SMTP.Host = v
	}
	if v := os.Getenv("HERALD_SMTP_PORT"); v != "" {
		if port, err := strconv.Atoi(v); err == nil {
			cfg.SMTP.Port = port
		}
	}
	if v := os.Getenv("HERALD_SMTP_USER"); v != "" {
		cfg.SMTP.User = v
	}
	if v := os.Getenv("HERALD_SMTP_PASS"); v != "" {
		cfg.SMTP.Pass = v
	}
	if v := os.Getenv("HERALD_SMTP_FROM"); v != "" {
		cfg.SMTP.From = v
	}
	if v := os.Getenv("HERALD_ALLOW_ALL_KEYS"); v != "" {
		cfg.AllowAllKeys = strings.ToLower(v) == "true"
	}
	if v := os.Getenv("HERALD_ORIGIN"); v != "" {
		cfg.Origin = v
	}
}

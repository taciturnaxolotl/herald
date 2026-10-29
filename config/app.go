package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
	LogLevel        string     `yaml:"log_level"`
	SMTP            SMTPConfig `yaml:"smtp"`
	AllowAllKeys    bool       `yaml:"allow_all_keys"`
	AllowedKeys     []string   `yaml:"allowed_keys"`
}

type SMTPConfig struct {
	Host               string `yaml:"host"`
	Port               int    `yaml:"port"`
	User               string `yaml:"user"`
	Pass               string `yaml:"pass"`
	From               string `yaml:"from"`
	DKIMPrivateKey     string `yaml:"dkim_private_key"`
	DKIMPrivateKeyFile string `yaml:"dkim_private_key_file"`
	DKIMSelector       string `yaml:"dkim_selector"`
	DKIMDomain         string `yaml:"dkim_domain"`
}

func DefaultAppConfig() *AppConfig {
	return &AppConfig{
		Host:        "0.0.0.0",
		SSHPort:     2222,
		HTTPPort:    8080,
		HostKeyPath: "./host_key",
		DBPath:      "./herald.db",
		Origin:      "http://localhost:8080",
		LogLevel:    "info",
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

		if err := yaml.Unmarshal([]byte(expandEnv(string(data))), cfg); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
	}

	if err := applyEnvOverrides(cfg); err != nil {
		return nil, err
	}

	// Default external_ssh_port to ssh_port if not set
	if cfg.ExternalSSHPort == 0 {
		cfg.ExternalSSHPort = cfg.SSHPort
	}

	return cfg, nil
}

// envRef matches only the documented ${VAR} form, so a bare $ in a password
// or key survives untouched.
var envRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnv(s string) string {
	return envRef.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
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

// applyEnvOverrides lets HERALD_* variables override the file; a malformed
// number is an error rather than being silently ignored.
func applyEnvOverrides(cfg *AppConfig) error {
	strs := map[string]*string{
		"HERALD_HOST":                       &cfg.Host,
		"HERALD_HOST_KEY_PATH":              &cfg.HostKeyPath,
		"HERALD_DB_PATH":                    &cfg.DBPath,
		"HERALD_ORIGIN":                     &cfg.Origin,
		"HERALD_LOG_LEVEL":                  &cfg.LogLevel,
		"HERALD_SMTP_HOST":                  &cfg.SMTP.Host,
		"HERALD_SMTP_USER":                  &cfg.SMTP.User,
		"HERALD_SMTP_PASS":                  &cfg.SMTP.Pass,
		"HERALD_SMTP_FROM":                  &cfg.SMTP.From,
		"HERALD_SMTP_DKIM_PRIVATE_KEY":      &cfg.SMTP.DKIMPrivateKey,
		"HERALD_SMTP_DKIM_PRIVATE_KEY_FILE": &cfg.SMTP.DKIMPrivateKeyFile,
		"HERALD_SMTP_DKIM_SELECTOR":         &cfg.SMTP.DKIMSelector,
		"HERALD_SMTP_DKIM_DOMAIN":           &cfg.SMTP.DKIMDomain,
	}
	for key, dst := range strs {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}

	ints := map[string]*int{
		"HERALD_SSH_PORT":          &cfg.SSHPort,
		"HERALD_EXTERNAL_SSH_PORT": &cfg.ExternalSSHPort,
		"HERALD_HTTP_PORT":         &cfg.HTTPPort,
		"HERALD_SMTP_PORT":         &cfg.SMTP.Port,
	}
	for key, dst := range ints {
		if v := os.Getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				return fmt.Errorf("%s must be a number, got %q", key, v)
			}
			*dst = n
		}
	}

	if v := os.Getenv("HERALD_ALLOW_ALL_KEYS"); v != "" {
		cfg.AllowAllKeys = strings.EqualFold(v, "true")
	}
	return nil
}

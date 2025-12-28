package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/viper"
)

// Config holds all configuration for the application
type Config struct {
	// GitHub settings
	GitHubToken    string `mapstructure:"github_token"`
	GitHubUsername string `mapstructure:"github_username"`
	GitHubEmail    string `mapstructure:"github_email"`

	// Claude settings
	ClaudeTimeout    time.Duration `mapstructure:"claude_timeout"`
	ClaudeMaxRetries int           `mapstructure:"claude_max_retries"`

	// Worker settings
	WorkerCount       int           `mapstructure:"worker_count"`
	WorkerQueueSize   int           `mapstructure:"worker_queue_size"`
	IssueCheckInterval time.Duration `mapstructure:"issue_check_interval"`

	// Paths
	WorkspaceDir string `mapstructure:"workspace_dir"`
	DatabasePath string `mapstructure:"database_path"`

	// Filters
	Languages      []string `mapstructure:"languages"`
	IncludeLabels  []string `mapstructure:"include_labels"`
	ExcludeLabels  []string `mapstructure:"exclude_labels"`
	ExcludeRepos   []string `mapstructure:"exclude_repos"`
	MinRepoStars   int      `mapstructure:"min_repo_stars"`
	MaxIssueAgeDays int     `mapstructure:"max_issue_age_days"`

	// Web UI
	WebEnabled bool   `mapstructure:"web_enabled"`
	WebPort    int    `mapstructure:"web_port"`

	// Logging
	LogLevel string `mapstructure:"log_level"`
	LogFile  string `mapstructure:"log_file"`
}

// Default returns the default configuration
func Default() *Config {
	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".auto-contributor")

	return &Config{
		GitHubEmail:       "1835304752@qq.com",
		ClaudeTimeout:     24 * time.Hour, // No practical timeout - let Claude work
		ClaudeMaxRetries:  3,
		WorkerCount:       2,
		WorkerQueueSize:   100,
		IssueCheckInterval: 10 * time.Minute,
		WorkspaceDir:      filepath.Join(dataDir, "workspace"),
		DatabasePath:      filepath.Join(dataDir, "data.db"),
		Languages:         []string{"go", "python", "typescript", "javascript", "rust"},
		IncludeLabels:     []string{"good first issue", "help wanted", "bug"},
		ExcludeLabels:     []string{"wontfix", "duplicate", "invalid"},
		ExcludeRepos:      []string{},
		MinRepoStars:      10,
		MaxIssueAgeDays:   30,
		WebEnabled:        true,
		WebPort:           8080,
		LogLevel:          "info",
		LogFile:           filepath.Join(dataDir, "auto-contributor.log"),
	}
}

// Load loads configuration from file and environment
func Load() (*Config, error) {
	cfg := Default()

	// Set up viper
	viper.SetConfigName("config")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("$HOME/.auto-contributor")

	// Environment variables
	viper.SetEnvPrefix("AC")
	viper.AutomaticEnv()

	// Bind specific env vars
	viper.BindEnv("github_token", "GITHUB_TOKEN")
	viper.BindEnv("github_username", "GITHUB_USERNAME")

	// Read config file (optional)
	if err := viper.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return nil, err
		}
	}

	// Unmarshal into struct
	if err := viper.Unmarshal(cfg); err != nil {
		return nil, err
	}

	// Ensure directories exist
	os.MkdirAll(cfg.WorkspaceDir, 0755)
	os.MkdirAll(filepath.Dir(cfg.DatabasePath), 0755)

	return cfg, nil
}

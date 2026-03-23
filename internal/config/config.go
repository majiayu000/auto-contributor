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

	// Paths
	WorkspaceDir string `mapstructure:"workspace_dir"`
	DatabasePath string `mapstructure:"database_path"`
	DatabaseURL  string `mapstructure:"database_url"` // PostgreSQL connection string (optional)

	// Filters
	Languages      []string `mapstructure:"languages"`
	IncludeLabels  []string `mapstructure:"include_labels"`
	ExcludeLabels  []string `mapstructure:"exclude_labels"`
	ExcludeRepos   []string `mapstructure:"exclude_repos"`
	PriorityRepos  []string `mapstructure:"priority_repos"` // repos to check first before searching
	MinRepoStars   int      `mapstructure:"min_repo_stars"`
	MaxIssueAgeDays int     `mapstructure:"max_issue_age_days"`

	// Pipeline V2 settings
	MaxReviewRounds        int    `mapstructure:"max_review_rounds"`
	MaxPRsPerRepo          int    `mapstructure:"max_prs_per_repo"`          // max open PRs per repo before throttling
	MaxConcurrentPipelines int    `mapstructure:"max_concurrent_pipelines"` // number of parallel issue workers
	PromptsDir             string `mapstructure:"prompts_dir"`

	// Loop settings
	Mode              string `mapstructure:"mode"`               // full or followup
	DiscoveryInterval int    `mapstructure:"discovery_interval"` // minutes between discovery cycles
	FeedbackInterval  int    `mapstructure:"feedback_interval"`  // minutes between feedback scans
	Topic             string `mapstructure:"topic"`              // discovery topic (e.g., "ai", "golang")
	AnalysisDepth     string `mapstructure:"analysis_depth"`     // quick, deep, ultrathink

	// Self-learning
	RulesDir          string `mapstructure:"rules_dir"`
	SynthesisInterval int    `mapstructure:"synthesis_interval"` // hours between synthesis cycles

	// Logging
	LogLevel string `mapstructure:"log_level"`
	LogFile  string `mapstructure:"log_file"`
}

// Default returns the default configuration
func Default() *Config {
	homeDir, _ := os.UserHomeDir()
	dataDir := filepath.Join(homeDir, ".auto-contributor")

	return &Config{
		GitHubEmail:      "1835304752@qq.com",
		ClaudeTimeout:    24 * time.Hour,
		ClaudeMaxRetries: 3,
		WorkspaceDir:     filepath.Join(homeDir, "Desktop", "code", "opensourece", "auto-workspace"),
		DatabasePath:     filepath.Join(dataDir, "data.db"),
		Languages:        []string{"go", "python", "typescript", "javascript", "rust"},
		IncludeLabels:    []string{"good first issue", "help wanted", "bug"},
		ExcludeLabels:    []string{"wontfix", "duplicate", "invalid"},
		ExcludeRepos:     []string{},
		PriorityRepos:    []string{},
		MinRepoStars:     1000,
		MaxIssueAgeDays:  30,
		MaxReviewRounds:        3,
		MaxPRsPerRepo:          1,
		MaxConcurrentPipelines: 3,
		PromptsDir:        filepath.Join(dataDir, "prompts"),
		Mode:              "full",
		DiscoveryInterval: 60,
		FeedbackInterval:  30,
		Topic:             "ai",
		AnalysisDepth:     "deep",
		RulesDir:          "",
		SynthesisInterval: 24,
		LogLevel:         "info",
		LogFile:          filepath.Join(dataDir, "auto-contributor.log"),
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
	viper.BindEnv("database_url", "DATABASE_URL")

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

	// Auto-detect prompts directory: prefer ./prompts (project-local) over default
	homeDir, _ := os.UserHomeDir()
	defaultPromptsDir := filepath.Join(homeDir, ".auto-contributor", "prompts")
	if cfg.PromptsDir == defaultPromptsDir {
		if info, err := os.Stat("prompts"); err == nil && info.IsDir() {
			cfg.PromptsDir = "prompts"
		}
	}

	return cfg, nil
}

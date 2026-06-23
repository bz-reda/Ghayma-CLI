package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	APIHost  string    `json:"api_host"`
	Token    string    `json:"token"`
	APIToken string    `json:"api_token"`
	UserID   string    `json:"user_id"`
	Email    string    `json:"email"`
	CLI      CLIState  `json:"cli,omitempty"`
}

// CLIState carries local client-side metadata that doesn't round-trip with
// the server — currently only deprecation-notice rate limiting. Nested
// under a `cli` key so future additions don't clutter the top-level config
// shape.
type CLIState struct {
	// DeprecationNotices maps a stable notice-id (e.g. "site.add") to the
	// RFC3339 timestamp at which that notice was last shown to the user.
	// Used to throttle repeat warnings to once per week per notice.
	DeprecationNotices map[string]string `json:"deprecation_notices,omitempty"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".paas-cli.json")
}

func Load() *Config {
	data, err := os.ReadFile(configPath())
	if err != nil {
		return &Config{APIHost: "https://api.ghayma.tech"}
	}

	var cfg Config
	json.Unmarshal(data, &cfg)
	if cfg.APIHost == "" {
		cfg.APIHost = "https://api.ghayma.tech"
	}
	return &cfg
}

func (c *Config) Save() error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(configPath(), data, 0600)
}
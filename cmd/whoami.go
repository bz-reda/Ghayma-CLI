package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var whoamiJSON bool

// authDescription explains which credential the CLI is riding and, for a
// session, how long it has left — the thing that was invisible when weekly
// expiry started surfacing as unrelated errors (2026-08-16).
func authDescription(cfg *config.Config) string {
	if cfg.UsesAPIToken() {
		return "long-lived CLI token"
	}
	exp, ok := cfg.SessionExpiry()
	if !ok {
		return "7-day session"
	}
	if time.Now().After(exp) {
		return "7-day session (EXPIRED — run: ghayma login)"
	}
	return fmt.Sprintf("7-day session (expires %s)", exp.Local().Format("2006-01-02 15:04"))
}

// whoamiAuthFields is the JSON half of the same information. Tokens never
// appear in either.
func whoamiAuthFields(cfg *config.Config) map[string]interface{} {
	if cfg.UsesAPIToken() {
		return map[string]interface{}{"auth_method": "api_token"}
	}
	out := map[string]interface{}{"auth_method": "session"}
	if exp, ok := cfg.SessionExpiry(); ok {
		out["session_expires_at"] = exp.Format(time.RFC3339)
	}
	return out
}

var whoamiCmd = &cobra.Command{
	Use:     "whoami",
	Aliases: []string{"account"},
	Short:   "Show the current CLI identity",
	Long:    "Prints the email, user ID, API endpoint and CLI version for the currently logged-in session. Tokens are never printed.",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		loggedIn := cfg.LoggedIn()

		if whoamiJSON {
			out := map[string]interface{}{
				"logged_in":   loggedIn,
				"email":       cfg.Email,
				"user_id":     cfg.UserID,
				"api_host":    cfg.APIHost,
				"cli_version": version,
			}
			if loggedIn {
				for k, v := range whoamiAuthFields(cfg) {
					out[k] = v
				}
			}
			b, _ := json.MarshalIndent(out, "", "  ")
			fmt.Println(string(b))
			if !loggedIn {
				os.Exit(1)
			}
			return
		}

		if !loggedIn {
			fmt.Println("✗ Not signed in. Run: ghayma login")
			os.Exit(1)
		}

		fmt.Printf("✓ Signed in as: %s\n", cfg.Email)
		if cfg.UserID != "" {
			fmt.Printf("  User ID:      %s\n", cfg.UserID)
		}
		fmt.Printf("  API endpoint: %s\n", cfg.APIHost)
		fmt.Printf("  Auth:         %s\n", authDescription(cfg))
		fmt.Printf("  CLI version:  %s\n", version)
	},
}

func init() {
	whoamiCmd.Flags().BoolVar(&whoamiJSON, "json", false, "output as JSON (tokens are never included)")
	rootCmd.AddCommand(whoamiCmd)
}

package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

var logoutCmd = &cobra.Command{
	Use:   "logout",
	Short: "Log out and clear saved credentials",
	Run: func(cmd *cobra.Command, args []string) {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Printf("❌ Could not locate config: %v\n", err)
			return
		}

		configFile := filepath.Join(home, ".paas-cli.json")

		if _, err := os.Stat(configFile); os.IsNotExist(err) {
			fmt.Println("ℹ️  Not logged in.")
			return
		}

		// Revoke the token login minted for this machine before the config
		// that holds its id is deleted — otherwise it lingers on the account
		// until it expires, eating one of the ten token slots.
		if cfg := config.Load(); cfg.APITokenID != "" {
			err := api.NewClient(cfg).DeleteAPIToken(cfg.APITokenID)
			var apiErr *api.APIError
			switch {
			case err == nil:
				fmt.Println("🔒 Revoked the CLI token.")
			case errors.Is(err, api.ErrUnauthorized),
				errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound:
				// Already gone (revoked in the Dashboard, or the credential
				// itself is dead) — nothing left to clean up server-side.
				fmt.Println("ℹ️  CLI token already revoked.")
			default:
				fmt.Printf("⚠️  Could not revoke the CLI token (%v) — revoke it in the Dashboard → Settings → API Tokens.\n", err)
			}
		}

		if err := os.Remove(configFile); err != nil {
			fmt.Printf("❌ Failed to remove credentials: %v\n", err)
			return
		}

		fmt.Println("👋 Logged out successfully.")
	},
}

func init() {
	rootCmd.AddCommand(logoutCmd)
}

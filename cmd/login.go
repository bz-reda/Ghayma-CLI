package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

// Browser login hands the CLI the Dashboard's 7-day session JWT, and there is
// no refresh — sessions died once a week and the failure surfaced as nonsense
// like "you don't have any projects yet" (2026-08-16). A personal access token
// is the credential the API is actually designed to be driven by, so login
// trades the fresh JWT for one straight away.
const (
	cliTokenTTLDays = 365
	cliTokenScope   = "full"
	// cliTokenNameMax mirrors the backend's 100-char limit on token names.
	cliTokenNameMax = 100
)

// cliTokenName identifies this machine's token, so re-logging in replaces it
// rather than eating another slot of the account's 10-token ceiling.
func cliTokenName() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	name := "ghayma-cli@" + host
	if len(name) > cliTokenNameMax {
		name = name[:cliTokenNameMax]
	}
	return name
}

// provisionCLIToken swaps a freshly-issued session JWT for a long-lived token.
// The JWT is stored first and any token left over from an earlier login on
// this machine is cleared, because the mint call is itself authenticated: a
// stale PAT would be preferred by Bearer() and 401 the request that is
// supposed to replace it.
func provisionCLIToken(client *api.Client, cfg *config.Config, jwt string) {
	cfg.Token = jwt
	cfg.APIToken = ""
	cfg.APITokenID = ""
	cfg.Save()

	name := cliTokenName()

	// One token per machine: drop the previous one before minting its
	// replacement. Best-effort — an older CLI may not have left one.
	if existing, err := client.ListAPITokens(); err == nil {
		for _, tok := range existing {
			if tok.Name == name {
				client.DeleteAPIToken(tok.ID)
			}
		}
	}

	created, err := client.CreateAPIToken(name, cliTokenScope, cliTokenTTLDays)
	if err != nil {
		fmt.Printf("⚠️  Could not create a long-lived CLI token (%v) — using a 7-day session instead.\n", err)
		return
	}

	cfg.APIToken = created.Token
	cfg.APITokenID = created.ID
	cfg.Save()

	expires := time.Now().AddDate(0, 0, cliTokenTTLDays)
	if created.ExpiresAt != nil {
		expires = *created.ExpiresAt
	}
	fmt.Printf("🔑 Created a long-lived CLI token %q (expires %s). Revoke it with 'ghayma logout' or in the Dashboard → Settings → API Tokens.\n",
		name, expires.Format("2006-01-02"))
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login to Ghayma",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		client := api.NewClient(cfg)

		useEmail, _ := cmd.Flags().GetBool("email")

		if useEmail {
			emailPrompt := promptui.Prompt{Label: "Email"}
			email, _ := emailPrompt.Run()

			passPrompt := promptui.Prompt{Label: "Password", Mask: '*'}
			password, _ := passPrompt.Run()

			resp, err := client.Login(email, password)
			if err != nil {
				fmt.Printf("❌ Login failed: %v\n", err)
				return
			}

			cfg.UserID = resp.User.ID
			cfg.Email = resp.User.Email

			fmt.Printf("✅ Logged in as %s (%s)\n", resp.User.Name, resp.User.Email)
			provisionCLIToken(client, cfg, resp.Token)
			return
		}

		// Default: browser login
		browserLogin(cfg, client)
	},
}

func browserLogin(cfg *config.Config, client *api.Client) {
	fmt.Println("🔐 Opening browser for login...")

	resp, err := http.Post(cfg.APIHost+"/api/v1/auth/cli/session", "application/json", nil)
	if err != nil {
		fmt.Printf("❌ Failed to create login session: %v\n", err)
		return
	}
	defer resp.Body.Close()

	var session struct {
		Code     string `json:"code"`
		LoginURL string `json:"login_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		fmt.Printf("❌ Failed to parse response: %v\n", err)
		return
	}

	openBrowser(session.LoginURL)
	fmt.Printf("\n   If browser didn't open, visit:\n   %s\n\n", session.LoginURL)
	fmt.Println("⏳ Waiting for approval in browser... (Ctrl+C to cancel)")

	for i := 0; i < 60; i++ {
		time.Sleep(5 * time.Second)

		pollResp, err := http.Get(cfg.APIHost + "/api/v1/auth/cli/session/" + session.Code)
		if err != nil {
			continue
		}

		var result struct {
			Confirmed bool   `json:"confirmed"`
			Token     string `json:"token"`
			Email     string `json:"email"`
			Error     string `json:"error"`
		}
		json.NewDecoder(pollResp.Body).Decode(&result)
		pollResp.Body.Close()

		if pollResp.StatusCode == http.StatusGone || pollResp.StatusCode == http.StatusNotFound {
			fmt.Println("❌ Login session expired. Please try again.")
			return
		}

		if result.Confirmed && result.Token != "" {
			cfg.Email = result.Email

			me, err := client.GetMe(result.Token)
			if err == nil {
				cfg.UserID = me.ID
				cfg.Email = me.Email
			}

			fmt.Printf("✅ Logged in as %s\n", cfg.Email)
			provisionCLIToken(client, cfg, result.Token)
			return
		}
	}

	fmt.Println("❌ Login timed out. Please try again.")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	}
	if cmd != nil {
		cmd.Start()
	}
}

func init() {
	loginCmd.Flags().Bool("email", false, "Login with email/password instead of browser")
	rootCmd.AddCommand(loginCmd)
}

package cmd

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

const (
	privacyPolicyVersion = "1.0"
	privacyPolicyURL     = "https://dash.ghayma.cloud/privacy"
)

var (
	registerInvite        string
	registerAcceptPrivacy bool
)

func privacyConsentText() string {
	return "Ghayma is in beta and your data is hosted on servers in Paris, France,\n" +
		"and in the other countries listed in the policy, outside Algeria.\n" +
		"Privacy Policy: " + privacyPolicyURL
}

// confirmPrivacy defaults to No: only an explicit y/yes accepts.
func confirmPrivacy(flagAccepted bool, ask func() (string, error)) bool {
	if flagAccepted {
		return true
	}
	answer, err := ask()
	if err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	}
	return false
}

// askPrivacyConsent is the consent question register and login share.
func askPrivacyConsent() (string, error) {
	p := promptui.Prompt{Label: "I have read and accept the Privacy Policy [y/N]"}
	return p.Run()
}

func registerHint(code string) string {
	switch code {
	case "invite_required":
		return "💡 Have a code? Run: ghayma register --invite <code>"
	case "privacy_required":
		return "💡 Your Ghayma CLI may be outdated: update it, or sign up at https://dash.ghayma.cloud/register"
	}
	return ""
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a new Ghayma account",
	Long: `Create a new Ghayma account.

While Ghayma is in private beta, registration requires an invitation code:

  ghayma register --invite GYB-XXXXXXXX

Registration asks you to accept the Privacy Policy (` + privacyPolicyURL + `).
--accept-privacy answers that question in advance; name, email and password
are still asked.`,
	Run: func(cmd *cobra.Command, args []string) {
		// The API host comes from config (default api.ghayma.tech), same as
		// every other command — register deliberately has no --host flag, since
		// it is not the place to choose a backend.
		cfg := config.Load()
		// It is, however, the place to record one: register auto-logs in and
		// mints a CLI token, and that token is only valid against the backend
		// that issued it. So when GHAYMA_API_HOST is steering this run, the
		// host is persisted with the credential instead of being dropped on
		// save the way a per-command override normally is.
		if os.Getenv("GHAYMA_API_HOST") != "" {
			cfg.SetHost(cfg.APIHost)
		}

		// Name
		namePrompt := promptui.Prompt{Label: "Full Name"}
		name, _ := namePrompt.Run()

		// Email
		emailPrompt := promptui.Prompt{Label: "Email"}
		email, _ := emailPrompt.Run()

		// Password
		passPrompt := promptui.Prompt{
			Label: "Password",
			Mask:  '*',
			Validate: func(s string) error {
				if len(s) < 8 {
					return fmt.Errorf("password must be at least 8 characters")
				}
				return nil
			},
		}
		password, _ := passPrompt.Run()

		// Confirm Password
		confirmPrompt := promptui.Prompt{Label: "Confirm Password", Mask: '*'}
		confirm, _ := confirmPrompt.Run()

		if password != confirm {
			fmt.Println("❌ Passwords do not match")
			return
		}

		fmt.Println()
		fmt.Println(privacyConsentText())
		accepted := confirmPrivacy(registerAcceptPrivacy, askPrivacyConsent)
		if !accepted {
			fmt.Println("❌ Registration cancelled: the Privacy Policy must be accepted.")
			return
		}

		client := api.NewClient(cfg)
		resp, err := client.Register(email, password, name, registerInvite, privacyPolicyVersion)
		if err != nil {
			fmt.Printf("❌ Registration failed: %v\n", err)
			var apiErr *api.APIError
			if errors.As(err, &apiErr) {
				if hint := registerHint(apiErr.Code); hint != "" {
					fmt.Println(hint)
				}
			}
			return
		}

		fmt.Printf("✅ Account created for %s (%s)\n", resp.User.Name, resp.User.Email)

		if resp.Message != "" {
			fmt.Printf("📧 %s\n", resp.Message)
		}

		// Auto-login if token is returned
		if resp.Token != "" {
			cfg.UserID = resp.User.ID
			cfg.Email = resp.User.Email
			fmt.Println("🔑 Automatically logged in")
			provisionCLIToken(client, cfg, resp.Token)
		} else {
			fmt.Println("📋 Please verify your email, then run: ghayma login")
		}
	},
}

func init() {
	registerCmd.Flags().StringVar(&registerInvite, "invite", "", "invitation code (required while Ghayma is in private beta)")
	registerCmd.Flags().BoolVar(&registerAcceptPrivacy, "accept-privacy", false, "accept the Privacy Policy ("+privacyPolicyURL+") without being asked")
	rootCmd.AddCommand(registerCmd)
}

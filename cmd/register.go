package cmd

import (
	"errors"
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var registerInvite string

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a new Ghayma account",
	Long: `Create a new Ghayma account.

While Ghayma is in private beta, registration requires an invitation code:

  ghayma register --invite GYB-XXXXXXXX`,
	Run: func(cmd *cobra.Command, args []string) {
		// The API host comes from config (default api.ghayma.tech), same as
		// every other command — registration is not the place to change it.
		cfg := config.Load()

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

		client := api.NewClient(cfg)
		resp, err := client.Register(email, password, name, registerInvite)
		if err != nil {
			fmt.Printf("❌ Registration failed: %v\n", err)
			var apiErr *api.APIError
			if errors.As(err, &apiErr) && apiErr.Code == "invite_required" {
				fmt.Println("💡 Have a code? Run: ghayma register --invite <code>")
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
	rootCmd.AddCommand(registerCmd)
}

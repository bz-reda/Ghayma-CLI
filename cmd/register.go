package cmd

import (
	"fmt"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Create a new Ghayma account",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()

		// API Host
		hostPrompt := promptui.Prompt{
			Label:   "API Host",
			Default: cfg.APIHost,
		}
		host, _ := hostPrompt.Run()
		cfg.APIHost = host

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
		resp, err := client.Register(email, password, name)
		if err != nil {
			fmt.Printf("❌ Registration failed: %v\n", err)
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
	rootCmd.AddCommand(registerCmd)
}
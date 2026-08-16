package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// projectCmd is the top-level grouping for project-scoped commands that
// don't fit cleanly under a single-verb top-level command. Today it hosts
// the transfer flow; future additions (rename, show, etc.) can land here.
var projectCmd = &cobra.Command{
	Use:   "project",
	Short: "Manage the current project",
}

// projectTransferCmd handles the initiate action directly when given an
// email. It also hosts the status / cancel / accept subcommands.
var projectTransferCmd = &cobra.Command{
	Use:   "transfer <email>",
	Short: "Transfer ownership of the current project to another user",
	Long: `Transfer ownership of the current project to another ghayma user.

The recipient will receive an email with an accept link (valid for 24h).
Once accepted, the project and all its resources (sites, deployments, env
vars, domains, databases, storage, auth apps) move to the recipient's
account. Audit history stays attributed to the original owner.

The transfer can be cancelled before it's accepted via:
  ghayma project transfer cancel`,
	Args: requireOneArg("email", ""),
	Run:  runTransferInitiate,
}

func runTransferInitiate(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		fmt.Println("❌ Please login first: ghayma login")
		return
	}

	projectID, projectName, err := readProjectIDAndName()
	if err != nil {
		fmt.Printf("❌ %v\n", err)
		return
	}

	toEmail := strings.TrimSpace(args[0])

	// Type-to-confirm — mirrors `project delete`. Stops the common "oops
	// wrong window" mistake where a user tab-completes a command without
	// realising which project they're in.
	fmt.Printf("⚠️  Transfer project '%s' to %s?\n", projectName, toEmail)
	fmt.Printf("   This will move all deployments, env vars, domains, and other resources.\n")
	fmt.Printf("   Type the project name to confirm: ")
	reader := bufio.NewReader(os.Stdin)
	input, _ := reader.ReadString('\n')
	input = strings.TrimSpace(input)
	if input != projectName {
		fmt.Println("❌ Name doesn't match. Transfer cancelled.")
		return
	}

	client := api.NewClient(cfg)
	resp, err := client.InitiateProjectTransfer(projectID, toEmail)
	if err != nil {
		fmt.Printf("❌ Failed to initiate transfer: %v\n", err)
		return
	}

	fmt.Println("✅ Transfer initiated.")
	fmt.Printf("   Recipient:  %s\n", resp.ToEmail)
	fmt.Printf("   Expires:    %s\n", resp.ExpiresAt)
	fmt.Printf("   Transfer ID: %s\n", resp.TransferID)
	fmt.Println("\nThe recipient has been emailed an accept link. They must accept within 24h.")
	fmt.Println("Cancel before acceptance with: ghayma project transfer cancel")
}

var projectTransferStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show the pending transfer for the current project",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, _, err := readProjectIDAndName()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		st, err := client.GetProjectTransferStatus(projectID)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}
		if !st.Pending {
			fmt.Println("No pending transfer.")
			return
		}
		fmt.Println("🔄 Pending transfer:")
		fmt.Printf("   To:           %s\n", st.ToEmail)
		fmt.Printf("   Initiated at: %s\n", st.InitiatedAt)
		fmt.Printf("   Expires at:   %s\n", st.ExpiresAt)
		fmt.Printf("   Transfer ID:  %s\n", st.TransferID)
		fmt.Println("\nCancel with: ghayma project transfer cancel")
	},
}

var projectTransferCancelCmd = &cobra.Command{
	Use:   "cancel",
	Short: "Cancel a pending project transfer",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		projectID, _, err := readProjectIDAndName()
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		client := api.NewClient(cfg)
		if err := client.CancelProjectTransfer(projectID); err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}
		fmt.Println("✅ Pending transfer cancelled.")
	},
}

var projectTransferAcceptCmd = &cobra.Command{
	Use:   "accept <token>",
	Short: "Accept a transfer invite sent to your email",
	Long: `Accept a project transfer using the raw token from the invite email.

Paste the token value exactly as received (a URL-safe base64 string). The
server handles hashing and constant-time verification internally. Your
signed-in CLI account's email must match the one the invite was addressed
to.`,
	Args: requireOneArg("token", ""),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if !cfg.LoggedIn() {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		// Token here is the raw base64url string from the email — never
		// the sha256 hash. The server hashes it before comparison.
		rawToken := strings.TrimSpace(args[0])

		client := api.NewClient(cfg)
		resp, err := client.AcceptProjectTransfer(rawToken)
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}
		fmt.Println("✅ Transfer accepted. You are now the owner.")
		fmt.Printf("   Project ID: %s\n", resp.ProjectID)
		fmt.Println("\nRun 'ghayma link' from your repo to wire a local directory to this project,")
		fmt.Println("or 'ghayma status' to see it in your project list.")
	},
}

// readProjectIDAndName reads the project config from CWD and returns the
// project's UUID and display name. Kept separate from deploy's projectConfig
// so it can be used from any directory regardless of site-level linking.
func readProjectIDAndName() (string, string, error) {
	data, err := readProjectConfig(".")
	if err != nil {
		return "", "", fmt.Errorf("no project config found — run 'ghayma init' or 'ghayma link' first")
	}
	var cfg struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", "", fmt.Errorf("invalid project config: %w", err)
	}
	if cfg.ProjectID == "" {
		return "", "", fmt.Errorf("project_id missing from project config")
	}
	return cfg.ProjectID, cfg.Name, nil
}

func init() {
	projectTransferCmd.AddCommand(projectTransferStatusCmd)
	projectTransferCmd.AddCommand(projectTransferCancelCmd)
	projectTransferCmd.AddCommand(projectTransferAcceptCmd)
	projectCmd.AddCommand(projectTransferCmd)
	rootCmd.AddCommand(projectCmd)
}

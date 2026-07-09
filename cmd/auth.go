package cmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// secretLikeKeyRegex flags map keys whose values should not be echoed to the
// terminal after an update. Mirrors the paas-api build-time secret heuristic
// but tightened for the narrower "are we about to print this to a user's
// screen" decision — we accept a few false positives to avoid leaking OAuth
// secrets in the common `auth config` confirmation.
var secretLikeKeyRegex = regexp.MustCompile(`(?i)(secret|password|token|private_key)`)

// looksLikeSecret reports whether a map key name hints that its value is
// sensitive enough to redact from echoed terminal output.
func looksLikeSecret(key string) bool {
	return secretLikeKeyRegex.MatchString(key)
}

// formatUpdatesList returns one "key = value" string per entry, redacting
// the value when the key name looks like a secret. Keys are sorted so the
// output is deterministic (helps tests and user scanning).
func formatUpdatesList(updates map[string]interface{}) []string {
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		if looksLikeSecret(k) {
			lines = append(lines, fmt.Sprintf("%s = <redacted>", k))
			continue
		}
		lines = append(lines, fmt.Sprintf("%s = %v", k, updates[k]))
	}
	return lines
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication services",
}

var authCreateCmd = &cobra.Command{
	Use:   "create [name]",
	Short: "Create a managed auth service for a project",
	Args:  requireOneArg("name", ""),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)

		// Find project
		projectID := ""
		if authProject != "" {
			projects, err := client.ListProjects()
			if err != nil {
				fmt.Printf("❌ Failed to list projects: %v\n", err)
				return
			}
			for _, p := range projects {
				if p.Name == authProject || p.Slug == authProject {
					projectID = p.ID
					break
				}
			}
			if projectID == "" {
				fmt.Printf("❌ Project '%s' not found\n", authProject)
				return
			}
		} else {
			data, err := readProjectConfig(".")
			if err != nil {
				fmt.Println("❌ No --project flag and no project config found")
				return
			}
			var projectCfg struct {
				ProjectID string `json:"project_id"`
			}
			json.Unmarshal(data, &projectCfg)
			projectID = projectCfg.ProjectID
		}

		bracketSlug := authCreateUsers
		twofa := authCreate2FA
		sms := authCreateSMS

		// Fail-soft pricing. An older backend (pre-catalog) returns
		// ErrCatalogUnavailable — skip all pricing UI and fall back to bare
		// flags / server defaults; never block the create.
		if cat, catErr := client.GetMarketplaceCatalog(); catErr == nil && cat != nil {
			// Interactive only when the user set NONE of the pricing flags, so
			// a scripted run (any flag present) stays fully non-interactive.
			if !cmd.Flags().Changed("users") && !cmd.Flags().Changed("2fa") && !cmd.Flags().Changed("sms") {
				var selErr error
				bracketSlug, twofa, sms, selErr = promptAuthSelections(cat)
				if selErr != nil {
					// A promptui cancel (Ctrl-C) must abort — never fall
					// through to a create at a default bracket.
					fmt.Println("❌ Cancelled.")
					return
				}
			} else if err := validateAuthBracket(cat, bracketSlug); err != nil {
				fmt.Printf("❌ %v\n", err)
				return
			}
			// Reserve preview on the FULL total (bracket + 2FA + SMS). A blank
			// bracket defaults to the catalog's smallest for the estimate.
			printAuthReservePreview(client, cat, projectID, bracketSlug, twofa, sms)
		}

		app, err := client.CreateAuthApp(args[0], authAppSlug, projectID, bracketSlug)
		if err != nil {
			fmt.Printf("❌ Failed to create auth app: %s\n", formatMarketplaceError(err))
			return
		}

		fmt.Printf("✅ Created auth app '%s'\n", app.Name)
		fmt.Printf("   App ID:    %s\n", app.AppID)
		fmt.Printf("   Status:    %s\n", app.Status)

		// 2FA/SMS aren't accepted by the create endpoint — enable them via a
		// follow-up PATCH. A points rejection here is NOT swallowed: the app
		// exists at its bracket, so report the partial success and how to
		// recover rather than pretend the toggle took.
		if twofa || sms {
			updates := map[string]interface{}{}
			if twofa {
				updates["two_fa_enabled"] = true
			}
			if sms {
				updates["sms_enabled"] = true
			}
			if err := client.UpdateAuthApp(app.ID, updates); err != nil {
				fmt.Printf("\n⚠️  Auth app created at bracket '%s', but enabling %s failed:\n%s\n",
					bracketOrDefault(bracketSlug), enabledFeatureList(twofa, sms), formatMarketplaceError(err))
				fmt.Printf("   Free up points, then enable them from the dashboard (or delete and recreate).\n")
			} else {
				fmt.Printf("   Enabled:   %s\n", enabledFeatureList(twofa, sms))
			}
		}

		fmt.Printf("\n📋 Endpoints:\n")
		fmt.Printf("   Register:  POST https://auth.ghayma.tech/v1/%s/register\n", app.AppID)
		fmt.Printf("   Login:     POST https://auth.ghayma.tech/v1/%s/login\n", app.AppID)
		fmt.Printf("   JWKS:      GET  https://auth.ghayma.tech/v1/%s/.well-known/jwks.json\n", app.AppID)
		fmt.Println("\n📋 Next steps:")
		fmt.Printf("   ghayma auth config %s --google-client-id <id> --google-client-secret <secret>\n", args[0])
	},
}

// printAuthReservePreview prints "This auth app will reserve N pts" (plus the
// remaining-after tail when the points summary is fetchable) before submit.
// Every step is fail-soft: a blank bracket defaults to the catalog's smallest,
// and a failed summary fetch just prints less — it never blocks the create.
func printAuthReservePreview(client *api.Client, cat *api.MarketplaceCatalog, projectID, bracketSlug string, twofa, sms bool) {
	previewBracket := bracketSlug
	if previewBracket == "" {
		if dt, ok := defaultAuthTier(cat); ok {
			previewBracket = dt.Slug
		}
	}
	cost, err := authCostPreview(cat, previewBracket, twofa, sms)
	if err != nil {
		return
	}
	summary, _ := client.GetProjectPoints(projectID)
	fmt.Println(formatReserveLineFor("auth app", cost, summary))
}

var authListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your auth apps",
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		apps, err := client.ListAuthApps()
		if err != nil {
			fmt.Printf("❌ Failed to list auth apps: %v\n", err)
			return
		}

		if len(apps) == 0 {
			fmt.Println("No auth apps found. Create one with: ghayma auth create <name> --project <project>")
			return
		}

		fmt.Printf("🔐 Your auth apps (%d):\n\n", len(apps))
		for _, app := range apps {
			providers := []string{"email"}
			if app.GoogleClientID != "" {
				providers = append(providers, "google")
			}
			if app.GitHubClientID != "" {
				providers = append(providers, "github")
			}
			fmt.Printf("   %-20s  %-15s  %-8s  providers: %s\n", app.Name, app.AppID, app.Status, strings.Join(providers, ", "))
		}
	},
}

var authInfoCmd = &cobra.Command{
	Use:   "info [name]",
	Short: "Show auth app details and endpoints",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("🔐 Auth App: %s\n\n", app.Name)
		fmt.Printf("   App ID:         %s\n", app.AppID)
		fmt.Printf("   Status:         %s\n", app.Status)
		fmt.Printf("   JWT Expiry:     %ds\n", app.JWTExpirySeconds)
		fmt.Printf("   Refresh Expiry: %ds\n", app.RefreshExpirySeconds)
		fmt.Printf("   Email Verify:   %v\n", app.EmailVerificationRequired)

		fmt.Println("\n   Providers:")
		fmt.Println("   ✅ Email/Password (always enabled)")
		if app.GoogleClientID != "" {
			fmt.Printf("   ✅ Google (client: %s...)\n", app.GoogleClientID[:20])
		} else {
			fmt.Println("   ❌ Google (not configured)")
		}
		if app.GitHubClientID != "" {
			fmt.Printf("   ✅ GitHub (client: %s...)\n", app.GitHubClientID[:20])
		} else {
			fmt.Println("   ❌ GitHub (not configured)")
		}

		if len(app.AllowedOrigins) > 0 {
			fmt.Printf("\n   Allowed Origins: %s\n", strings.Join(app.AllowedOrigins, ", "))
		}

		fmt.Printf("\n📋 Endpoints:\n")
		fmt.Printf("   Register:       POST https://auth.ghayma.tech/v1/%s/register\n", app.AppID)
		fmt.Printf("   Login:          POST https://auth.ghayma.tech/v1/%s/login\n", app.AppID)
		fmt.Printf("   Refresh:        POST https://auth.ghayma.tech/v1/%s/refresh\n", app.AppID)
		fmt.Printf("   Logout:         POST https://auth.ghayma.tech/v1/%s/logout\n", app.AppID)
		fmt.Printf("   Forgot Password:POST https://auth.ghayma.tech/v1/%s/forgot-password\n", app.AppID)
		fmt.Printf("   JWKS:           GET  https://auth.ghayma.tech/v1/%s/.well-known/jwks.json\n", app.AppID)
		if app.GoogleClientID != "" {
			fmt.Printf("   Google OAuth:   GET  https://auth.ghayma.tech/v1/%s/auth/google?redirect_uri=<url>\n", app.AppID)
		}
		if app.GitHubClientID != "" {
			fmt.Printf("   GitHub OAuth:   GET  https://auth.ghayma.tech/v1/%s/auth/github?redirect_uri=<url>\n", app.AppID)
		}
	},
}

// Config flags
var (
	authProject               string
	authAppSlug               string
	authCreateUsers           string
	authCreate2FA             bool
	authCreateSMS             bool
	authConfigGoogleID        string
	authConfigGoogleSecret    string
	authConfigGitHubID        string
	authConfigGitHubSecret    string
	authConfigEmailVerify     string
	authConfigAllowedOrigins  string
	authConfigJWTExpiry       int
	authConfigRefreshExpiry   int
)

var authConfigCmd = &cobra.Command{
	Use:   "config [name]",
	Short: "Configure an auth app (OAuth providers, settings)",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		updates := map[string]interface{}{}

		if authConfigGoogleID != "" {
			updates["google_client_id"] = authConfigGoogleID
		}
		if authConfigGoogleSecret != "" {
			updates["google_client_secret"] = authConfigGoogleSecret
		}
		if authConfigGitHubID != "" {
			updates["github_client_id"] = authConfigGitHubID
		}
		if authConfigGitHubSecret != "" {
			updates["github_client_secret"] = authConfigGitHubSecret
		}
		if authConfigEmailVerify != "" {
			updates["email_verification_required"] = authConfigEmailVerify == "true"
		}
		if authConfigAllowedOrigins != "" {
			origins := strings.Split(authConfigAllowedOrigins, ",")
			for i := range origins {
				origins[i] = strings.TrimSpace(origins[i])
			}
			updates["allowed_origins"] = origins
		}
		if authConfigJWTExpiry > 0 {
			updates["jwt_expiry_seconds"] = authConfigJWTExpiry
		}
		if authConfigRefreshExpiry > 0 {
			updates["refresh_expiry_seconds"] = authConfigRefreshExpiry
		}

		if len(updates) == 0 {
			fmt.Println("❌ No configuration changes specified. Available flags:")
			fmt.Println("   --google-client-id, --google-client-secret")
			fmt.Println("   --github-client-id, --github-client-secret")
			fmt.Println("   --email-verify (true/false)")
			fmt.Println("   --allowed-origins (comma-separated)")
			fmt.Println("   --jwt-expiry (seconds)")
			fmt.Println("   --refresh-expiry (seconds)")
			return
		}

		err = client.UpdateAuthApp(app.ID, updates)
		if err != nil {
			fmt.Printf("❌ Failed to update: %v\n", err)
			return
		}

		fmt.Printf("✅ Auth app '%s' updated\n", args[0])
		for _, line := range formatUpdatesList(updates) {
			fmt.Printf("   %s\n", line)
		}
	},
}

var authUsersCmd = &cobra.Command{
	Use:   "users [name]",
	Short: "List users for an auth app",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		users, total, err := client.ListAuthAppUsers(app.ID)
		if err != nil {
			fmt.Printf("❌ Failed to list users: %v\n", err)
			return
		}

		if total == 0 {
			fmt.Printf("No users registered for '%s'\n", args[0])
			return
		}

		fmt.Printf("👥 Users for '%s' (%d total):\n\n", args[0], total)
		for _, u := range users {
			verified := "✅"
			if !u.EmailVerified {
				verified = "❌"
			}
			disabled := ""
			if u.Disabled {
				disabled = " [DISABLED]"
			}
			fmt.Printf("   %-30s  %-8s  verified:%s%s\n", u.Email, u.Provider, verified, disabled)
		}
	},
}

var authStatsCmd = &cobra.Command{
	Use:   "stats [name]",
	Short: "Show auth app statistics",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		stats, err := client.GetAuthAppStats(app.ID)
		if err != nil {
			fmt.Printf("❌ Failed to get stats: %v\n", err)
			return
		}

		fmt.Printf("📊 Stats for '%s':\n\n", args[0])
		for k, v := range stats {
			fmt.Printf("   %-20s %v\n", k+":", v)
		}
	},
}

var authDeleteCmd = &cobra.Command{
	Use:   "delete [name]",
	Short: "Delete an auth app and all its users",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  This will permanently delete auth app '%s' and ALL its users. Type the name to confirm: ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != args[0] {
			fmt.Println("❌ Cancelled.")
			return
		}

		err = client.DeleteAuthApp(app.ID)
		if err != nil {
			fmt.Printf("❌ Failed to delete: %v\n", err)
			return
		}

		fmt.Printf("✅ Auth app '%s' deleted.\n", args[0])
	},
}

var authRotateKeysCmd = &cobra.Command{
	Use:   "rotate-keys [name]",
	Short: "Rotate JWT signing keys for an auth app",
	Args:  requireOneArg("name", "auth list"),
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		if cfg.Token == "" {
			fmt.Println("❌ Please login first: ghayma login")
			return
		}

		client := api.NewClient(cfg)
		app, err := findAuthAppByName(client, args[0])
		if err != nil {
			fmt.Printf("❌ %v\n", err)
			return
		}

		fmt.Printf("⚠️  Rotating keys will invalidate all existing JWT tokens for '%s'. Continue? (y/n): ", args[0])
		var confirm string
		fmt.Scanln(&confirm)
		if confirm != "y" && confirm != "Y" {
			fmt.Println("❌ Cancelled.")
			return
		}

		err = client.RotateAuthAppKeys(app.ID)
		if err != nil {
			fmt.Printf("❌ Failed to rotate keys: %v\n", err)
			return
		}

		fmt.Printf("✅ JWT signing keys rotated for '%s'. All existing tokens are now invalid.\n", args[0])
	},
}

// Helper to find auth app by name
func findAuthAppByName(client *api.Client, name string) (*api.AuthAppInfo, error) {
	apps, err := client.ListAuthApps()
	if err != nil {
		return nil, fmt.Errorf("failed to list auth apps: %v", err)
	}
	for _, app := range apps {
		if app.Name == name || app.AppID == name {
			return &app, nil
		}
	}
	return nil, fmt.Errorf("auth app '%s' not found", name)
}

func init() {
	// --project has no short form: -p is reserved for --prod on `deploy`,
	// and silently aliasing --project to -p here (or vice-versa) is a
	// muscle-memory footgun across commands.
	authCreateCmd.Flags().StringVar(&authProject, "project", "", "Project name or slug")
	authCreateCmd.Flags().StringVar(&authAppSlug, "app-id", "", "App slug/identifier (required)")
	authCreateCmd.MarkFlagRequired("app-id")
	authCreateCmd.Flags().StringVar(&authCreateUsers, "users", "", "User-capacity bracket: 1k|10k|100k|1m (auth_tiers.slug), priced in points. Interactive picker when omitted; server default if no catalog.")
	authCreateCmd.Flags().BoolVar(&authCreate2FA, "2fa", false, "Enable two-factor auth (adds the flat 2FA points add-on).")
	authCreateCmd.Flags().BoolVar(&authCreateSMS, "sms", false, "Enable SMS (adds the selected bracket's SMS points add-on).")

	authConfigCmd.Flags().StringVar(&authConfigGoogleID, "google-client-id", "", "Google OAuth client ID")
	authConfigCmd.Flags().StringVar(&authConfigGoogleSecret, "google-client-secret", "", "Google OAuth client secret")
	authConfigCmd.Flags().StringVar(&authConfigGitHubID, "github-client-id", "", "GitHub OAuth client ID")
	authConfigCmd.Flags().StringVar(&authConfigGitHubSecret, "github-client-secret", "", "GitHub OAuth client secret")
	authConfigCmd.Flags().StringVar(&authConfigEmailVerify, "email-verify", "", "Require email verification (true/false)")
	authConfigCmd.Flags().StringVar(&authConfigAllowedOrigins, "allowed-origins", "", "Comma-separated allowed origins")
	authConfigCmd.Flags().IntVar(&authConfigJWTExpiry, "jwt-expiry", 0, "JWT token expiry in seconds")
	authConfigCmd.Flags().IntVar(&authConfigRefreshExpiry, "refresh-expiry", 0, "Refresh token expiry in seconds")

	authCmd.AddCommand(authCreateCmd)
	authCmd.AddCommand(authListCmd)
	authCmd.AddCommand(authInfoCmd)
	authCmd.AddCommand(authConfigCmd)
	authCmd.AddCommand(authUsersCmd)
	authCmd.AddCommand(authStatsCmd)
	authCmd.AddCommand(authDeleteCmd)
	authCmd.AddCommand(authRotateKeysCmd)
	rootCmd.AddCommand(authCmd)
}
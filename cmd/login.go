package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
	if existing, err := client.ListAPITokens(false); err == nil {
		for _, tok := range existing {
			if tok.Name == name {
				client.DeleteAPIToken(tok.ID)
			}
		}
	}

	created, err := client.CreateAPIToken(api.CreateAPITokenInput{Name: name, Scope: cliTokenScope, ExpiresInDays: cliTokenTTLDays})
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

// loginFlow is how a login waits and asks. Tests swap the clock, the browser
// and the prompt.
type loginFlow struct {
	interval      time.Duration // between polls
	wait          time.Duration // how long to poll when the server names no expiry
	timeout       time.Duration // per create or poll request
	now           func() time.Time
	sleep         func(time.Duration)
	openBrowser   func(string)
	acceptPrivacy bool
	askPrivacy    func() (string, error)
}

var defaultLoginFlow = loginFlow{
	interval:    5 * time.Second,
	wait:        5 * time.Minute,
	timeout:     30 * time.Second,
	now:         time.Now,
	sleep:       time.Sleep,
	openBrowser: openBrowser,
	askPrivacy:  askPrivacyConsent,
}

var loginAcceptPrivacy bool

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Login to Ghayma",
	Long: `Login to Ghayma, in the browser or with --email.

If your account has not accepted the Privacy Policy (` + privacyPolicyURL + `),
login asks you to first. --accept-privacy answers that question in advance.`,
	Run: func(cmd *cobra.Command, args []string) {
		cfg := config.Load()
		// A login is the one deliberate act that should persist the host, so
		// both ways of naming one go through SetHost: --host says it outright,
		// and an active GHAYMA_API_HOST is the backend this token is being
		// minted against. Everywhere else the variable stays per-command and
		// Save() drops it. A plain assignment to cfg.APIHost would be treated
		// as such an override and lost.
		if h, _ := cmd.Flags().GetString("host"); h != "" {
			cfg.SetHost(h)
		} else if os.Getenv("GHAYMA_API_HOST") != "" {
			cfg.SetHost(cfg.APIHost)
		}
		client := api.NewClient(cfg)

		flow := defaultLoginFlow
		flow.acceptPrivacy = loginAcceptPrivacy

		var err error
		if useEmail, _ := cmd.Flags().GetBool("email"); useEmail {
			emailPrompt := promptui.Prompt{Label: "Email"}
			email, _ := emailPrompt.Run()

			passPrompt := promptui.Prompt{Label: "Password", Mask: '*'}
			password, _ := passPrompt.Run()

			err = emailLogin(cfg, client, email, password, flow)
		} else {
			err = browserLogin(cfg, client, flow)
		}
		if err != nil {
			failf("%v", err)
		}
	},
}

func emailLogin(cfg *config.Config, client *api.Client, email, password string, flow loginFlow) error {
	resp, err := client.Login(email, password)
	if err != nil {
		return fmt.Errorf("Login failed: %s", refusalText(err))
	}
	if resp.Token == "" {
		return errors.New("Login failed: the server sent no session. Please try again.")
	}
	return completeLogin(cfg, client, resp.Token, resp.User.Email, flow)
}

func browserLogin(cfg *config.Config, client *api.Client, flow loginFlow) error {
	ctx, cancel := context.WithTimeout(context.Background(), flow.timeout)
	session, err := client.CreateCLISession(ctx)
	cancel()
	if err != nil {
		return fmt.Errorf("Could not start the login: %s", refusalText(err))
	}
	if session.Code == "" || session.LoginURL == "" {
		return errors.New("Could not start the login: the server sent an incomplete login session. Please try again.")
	}

	fmt.Println("🔐 Opening browser for login...")
	flow.openBrowser(session.LoginURL)
	fmt.Printf("\n   If browser didn't open, visit:\n   %s\n\n", session.LoginURL)
	fmt.Println("⏳ Waiting for approval in browser... (Ctrl+C to cancel)")

	wait := flow.wait
	if session.ExpiresIn > 0 {
		wait = time.Duration(session.ExpiresIn) * time.Second
	}
	approved, err := waitForApproval(client, session.Code, flow.now().Add(wait), flow)
	if err != nil {
		return err
	}
	return completeLogin(cfg, client, approved.Token, approved.Email, flow)
}

// waitForApproval polls until the login is approved, refused or past the
// deadline. A throttled poll waits as long as the server asked; server trouble
// is said once and polling goes on.
func waitForApproval(client *api.Client, code string, deadline time.Time, flow loginFlow) (*api.CLISessionPoll, error) {
	delay := flow.interval
	var troubleSaid, throttleSaid bool
	for {
		remaining := deadline.Sub(flow.now())
		if remaining <= 0 {
			break
		}
		flow.sleep(min(delay, remaining))
		if !flow.now().Before(deadline) {
			break
		}
		delay = flow.interval

		ctx, cancel := context.WithTimeout(context.Background(), min(flow.timeout, deadline.Sub(flow.now())))
		poll, err := client.PollCLISession(ctx, code)
		cancel()

		var apiErr *api.APIError
		switch {
		case err == nil && poll.Status == http.StatusOK && poll.Confirmed:
			if poll.Token == "" {
				return nil, errors.New("Login failed: the server approved the login but sent no session. Please try again.")
			}
			return poll, nil
		case err == nil:
			continue
		case !errors.As(err, &apiErr) || apiErr.Status >= 500:
			if !troubleSaid {
				fmt.Printf("⚠️  Server unavailable, retrying… (%v)\n", err)
				troubleSaid = true
			}
		case apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusGone:
			return nil, errors.New("Login session expired. Please try again.")
		case apiErr.Status == http.StatusTooManyRequests:
			delay = max(apiErr.RetryAfter, flow.interval)
			if !throttleSaid {
				fmt.Printf("⏳ The server asked to slow down; checking again in %s.\n", waitText(min(delay, deadline.Sub(flow.now()))))
				throttleSaid = true
			}
		default:
			return nil, fmt.Errorf("Login failed: %v", err)
		}
	}
	return nil, errors.New("Login timed out. Please try again.")
}

// completeLogin asks for privacy consent when the account needs it, on the
// fresh session and before any credential is saved, then saves the login.
func completeLogin(cfg *config.Config, client *api.Client, jwt, email string, flow loginFlow) error {
	ctx, cancel := context.WithTimeout(context.Background(), flow.timeout)
	me, err := client.GetMe(ctx, jwt)
	cancel()
	if err != nil {
		return fmt.Errorf("Could not check your account: %v. Please try again.", err)
	}
	if me.ID == "" {
		return errors.New("Could not check your account: the server sent no account details. Please try again.")
	}

	if me.PrivacyConsentRequired {
		fmt.Println()
		fmt.Println("Before you continue, please read and accept the Ghayma Privacy Policy.")
		fmt.Println(privacyConsentText())
		if !flow.acceptPrivacy && !stdinIsTerminalFn() {
			return errors.New("Login cancelled: the Privacy Policy must be accepted. Run again in a terminal, or with --accept-privacy.")
		}
		if !confirmPrivacy(flow.acceptPrivacy, flow.askPrivacy) {
			return errors.New("Login cancelled: the Privacy Policy must be accepted.")
		}
		ctx, cancel := context.WithTimeout(context.Background(), flow.timeout)
		err := client.AcceptPrivacy(ctx, jwt, privacyPolicyVersion)
		cancel()
		if err != nil {
			return fmt.Errorf("Could not record your acceptance of the Privacy Policy: %v. Please try again.", err)
		}
	}

	cfg.UserID = me.ID
	cfg.Email = me.Email
	if cfg.Email == "" {
		cfg.Email = email
	}
	fmt.Printf("✅ Logged in as %s\n", cfg.Email)
	provisionCLIToken(client, cfg, jwt)
	return nil
}

// refusalText is the server's message, with the wait it asked for unless the
// message already says when to try again.
func refusalText(err error) string {
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 && !strings.Contains(strings.ToLower(apiErr.Message), "try again") {
		return fmt.Sprintf("%v (try again in %s)", err, waitText(apiErr.RetryAfter))
	}
	return err.Error()
}

// waitText rounds a wait up to whole seconds, or whole minutes from one minute.
func waitText(d time.Duration) string {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 60 {
		return plural(max(secs, 1), "second")
	}
	return plural((secs+59)/60, "minute")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
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
	loginCmd.Flags().String("host", "", "API host to log in to (e.g. https://api.staging.ghayma.tech); stored in the config file")
	loginCmd.Flags().BoolVar(&loginAcceptPrivacy, "accept-privacy", false, "accept the Privacy Policy ("+privacyPolicyURL+") without being asked, if your account needs to")
	rootCmd.AddCommand(loginCmd)
}

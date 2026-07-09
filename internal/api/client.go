package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"paas-cli/internal/config"
)

type Client struct {
	cfg    *config.Config
	http   *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{cfg: cfg, http: &http.Client{}}
}

// Auth

type AuthResponse struct {
	Token    string `json:"token"`
	APIToken string `json:"api_token"`
	User     struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Name  string `json:"name"`
	} `json:"user"`
}

func (c *Client) Login(email, password string) (*AuthResponse, error) {
	body, _ := json.Marshal(map[string]string{"email": email, "password": password})
	resp, err := c.http.Post(c.cfg.APIHost+"/api/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("login failed (status %d)", resp.StatusCode)
	}

	var result AuthResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return &result, nil
}

type RegisterResponse struct {
	Token    string `json:"token"`
	APIToken string `json:"api_token"`
	User     struct {
		ID            string `json:"id"`
		Email         string `json:"email"`
		Name          string `json:"name"`
		EmailVerified bool   `json:"email_verified"`
	} `json:"user"`
	Message string `json:"message"`
}

func (c *Client) Register(email, password, name string) (*RegisterResponse, error) {
	body, _ := json.Marshal(map[string]string{
		"email":    email,
		"password": password,
		"name":     name,
	})
	resp, err := c.http.Post(c.cfg.APIHost+"/api/v1/auth/register", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("%s", errResp["error"])
	}

	var result RegisterResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return &result, nil
}

type MeResponse struct {
	ID    string `json:"id"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (c *Client) GetMe(token string) (*MeResponse, error) {
	req, err := http.NewRequest("GET", c.cfg.APIHost+"/api/v1/auth/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get user info")
	}

	var result MeResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return &result, nil
}

// Projects

type Project struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Framework string `json:"framework"`
}

func (c *Client) CreateProject(name, framework, billingAccountID, plan string) (*Project, error) {
	payload := map[string]string{"name": name, "framework": framework}
	// The API requires billing_account_id for billable plans (every
	// seeded plan is billable post-Phase 6; init uses the default
	// "hobby" plan). Omit the field entirely when unset so the server's
	// own default/validation applies.
	if billingAccountID != "" {
		payload["billing_account_id"] = billingAccountID
	}
	// Send the chosen plan only when set; empty → omit → the server
	// applies its default (preserving pre-plan-selection init behavior).
	if plan != "" {
		payload["plan"] = plan
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("POST", "/api/v1/projects", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		// decodeAPIError surfaces the server's {"error":...} message
		// instead of dumping the raw JSON body at the user.
		return nil, decodeAPIError(resp)
	}

	var project Project
	json.NewDecoder(resp.Body).Decode(&project)
	return &project, nil
}

// FixedPlan mirrors one row of GET /api/v1/billing/plans' "fixed_plans"
// array. Only the fields the CLI's plan picker renders are declared; the
// backend response carries many more (bucket, limits, resources, yearly
// pricing) that init doesn't need.
type FixedPlan struct {
	Slug             string `json:"slug"`
	DisplayName      string `json:"display_name"`
	PriceDZDPerMonth int64  `json:"price_dzd_per_month"`
	Points           int    `json:"points"`
	MaxAppTier       string `json:"max_app_tier"`
	MaxDBTier        string `json:"max_db_tier"`
}

// GetPlans fetches the active fixed plans from GET /api/v1/billing/plans
// ({"fixed_plans":[...],"payg":{...}}). Only the fixed_plans array is
// returned — init picks a flat-fee plan, never PAYG. An older/self-hosted
// server without the endpoint returns a non-200, surfaced as an error so
// the caller can fall back to the server's default plan.
func (c *Client) GetPlans() ([]FixedPlan, error) {
	resp, err := c.authRequest("GET", "/api/v1/billing/plans", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}

	var out struct {
		FixedPlans []FixedPlan `json:"fixed_plans"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.FixedPlans, nil
}

// Billing accounts

type BillingAccount struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Role       string `json:"role"`
	IsPersonal bool   `json:"is_personal"`
}

// ListBillingAccounts returns the caller's billing accounts
// (GET /api/v1/billing-accounts → {"accounts":[...]}). The server filters
// to accounts the caller owns or is a member of.
func (c *Client) ListBillingAccounts() ([]BillingAccount, error) {
	resp, err := c.authRequest("GET", "/api/v1/billing-accounts", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, decodeAPIError(resp)
	}

	var out struct {
		Accounts []BillingAccount `json:"accounts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Accounts, nil
}

// EligibleBillingAccounts filters to accounts that can back a new project:
// active, and the caller is owner or admin. Mirrors the dashboard's
// project-create gate. Pure function — unit-tested.
func EligibleBillingAccounts(accounts []BillingAccount) []BillingAccount {
	var out []BillingAccount
	for _, a := range accounts {
		if a.Status == "active" && (a.Role == "owner" || a.Role == "admin") {
			out = append(out, a)
		}
	}
	return out
}

func (c *Client) ListProjects() ([]Project, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var projects []Project
	json.NewDecoder(resp.Body).Decode(&projects)
	return projects, nil
}

// Domains

// Sites

type Site struct {
	ID        string `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	Status    string `json:"status"`
	// Points-allowance marketplace: every app carries its compute tier slug +
	// replica count so `site scale` can show the current size before changing it
	// and price the change as AppCost(tier.PointsCost, replicas).
	AppTierSlug string `json:"app_tier_slug"`
	Replicas    int    `json:"replicas"`
}

func (c *Client) CreateSite(projectID, name string) (*Site, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/sites", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return nil, fmt.Errorf("%s", errResp["error"])
	}

	var site Site
	json.NewDecoder(resp.Body).Decode(&site)
	return &site, nil
}

func (c *Client) ListSites(projectID string) ([]Site, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to list sites (status %d): %s", resp.StatusCode, string(respBody))
	}

	var sites []Site
	json.NewDecoder(resp.Body).Decode(&sites)
	return sites, nil
}

// SetAppTier changes an app's compute tier and/or replica count via
// PUT /api/v1/projects/:id/sites/:siteId/tier (the sites handler is mounted
// project-scoped, so the project id is part of the path even though the handler
// keys off siteId). Both fields are always sent — app_tier_slug is required on
// the wire, so the caller defaults an unset flag to the site's current value.
// Non-2xx responses route through classifyAPIError so the marketplace classes
// render: 409 maxtier (ErrAppTierExceedsPlan) / 409 insufficient (points
// budget) / 503 capacity; a 400 replicas-below-minimum surfaces its raw reason
// (the CLI validates replicas >= 1 client-side before ever calling this).
func (c *Client) SetAppTier(projectID, siteID, tierSlug string, replicas int) (*Site, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"app_tier_slug": tierSlug,
		"replicas":      replicas,
	})
	resp, err := c.authRequest("PUT", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/tier", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, classifyAPIError(resp.StatusCode, respBody)
	}

	var site Site
	json.NewDecoder(resp.Body).Decode(&site)
	return &site, nil
}

// Domains

func (c *Client) AddDomain(projectID, siteID, domain string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"project_id": projectID,
		"site_id":    siteID,
		"domain":     domain,
		"is_primary": true,
	})
	resp, err := c.authRequest("POST", "/api/v1/domains", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to add domain: %s", string(respBody))
	}
	return nil
}

// Deploy

type DeployResponse struct {
	DeploymentID string `json:"deployment_id"`
	Status       string `json:"status"`
	Message      string `json:"message"`
}

// DeployBuildConfig carries the .ghayma.json build fields sent in the upload
// form. Empty fields are omitted (the backend persist-back ignores them).
type DeployBuildConfig struct {
	Framework       string
	BuildCommand    string
	InstallCommand  string
	StartCommand    string
	OutputDirectory string
	Port            int
}

// Deploy uploads a tarball to /api/v1/deploy/upload and starts a build.
// dockerfilePath is the optional Part 2 PR-C explicit override; pass empty
// string to fall back to the platform convention (literal `Dockerfile` at
// appDir, only honored when projects.custom_dockerfile_enabled is TRUE).
func (c *Client) Deploy(projectID, siteID, sourceDir, commitMessage string, isProduction bool, rootDirectory, dockerfilePath string, bc DeployBuildConfig, rules *IgnoreRules) (*DeployResponse, error) {
	tarPath := filepath.Join(os.TempDir(), "paas-source.tar.gz")
	defer os.Remove(tarPath)

	if err := createTarball(sourceDir, tarPath, rules); err != nil {
		return nil, fmt.Errorf("failed to create tarball: %w", err)
	}

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	writer.WriteField("project_id", projectID)
	if siteID != "" {
		writer.WriteField("site_id", siteID)
	}
	writer.WriteField("commit_message", commitMessage)
	if isProduction {
		writer.WriteField("is_production", "true")
	}
	if rootDirectory != "" {
		writer.WriteField("root_directory", rootDirectory)
	}
	if dockerfilePath != "" {
		writer.WriteField("dockerfile_path", dockerfilePath)
	}
	if bc.Framework != "" {
		writer.WriteField("framework", bc.Framework)
	}
	if bc.BuildCommand != "" {
		writer.WriteField("build_command", bc.BuildCommand)
	}
	if bc.InstallCommand != "" {
		writer.WriteField("install_command", bc.InstallCommand)
	}
	if bc.StartCommand != "" {
		writer.WriteField("start_command", bc.StartCommand)
	}
	if bc.OutputDirectory != "" {
		writer.WriteField("output_directory", bc.OutputDirectory)
	}
	if bc.Port > 0 {
		writer.WriteField("port", strconv.Itoa(bc.Port))
	}

	file, err := os.Open(tarPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	part, err := writer.CreateFormFile("source", "source.tar.gz")
	if err != nil {
		return nil, err
	}
	io.Copy(part, file)
	writer.Close()

	req, err := http.NewRequest("POST", c.cfg.APIHost+"/api/v1/deploy/upload", &buf)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("deploy failed: %s", string(respBody))
	}

	var result DeployResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return &result, nil
}

// Deployment Status

type Deployment struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	ImageTag string `json:"image_tag"`
	Domains  []string `json:"domains"`

}

func (c *Client) GetDeployment(id string) (*Deployment, error) {
	resp, err := c.authRequest("GET", "/api/v1/deployments/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var deployment Deployment
	json.NewDecoder(resp.Body).Decode(&deployment)
	return &deployment, nil
}

func (c *Client) GetDeploymentLogs(id string) (string, error) {
	resp, err := c.authRequest("GET", "/api/v1/deployments/"+id+"/logs", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var result map[string]string
	json.NewDecoder(resp.Body).Decode(&result)
	return result["logs"], nil
}

func (c *Client) ListDomains(projectID string) ([]string, error) {
	resp, err := c.authRequest("GET", "/api/v1/domains/project/"+projectID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var domains []struct {
		Domain string `json:"domain"`
	}
	json.NewDecoder(resp.Body).Decode(&domains)

	var result []string
	for _, d := range domains {
		result = append(result, d.Domain)
	}
	return result, nil
}

func (c *Client) RemoveDomain(projectID, domain string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/domains/"+domain+"?project_id="+projectID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed: %s", string(respBody))
	}
	return nil
}

// EnvVarsSnapshot captures both the key→value map and the set of keys that
// are marked build-time (forwarded to the Docker build as --build-arg).
type EnvVarsSnapshot struct {
	Values        map[string]string
	BuildTimeKeys []string
}

// IsBuildTime reports whether `key` is marked as a build-time var.
func (s *EnvVarsSnapshot) IsBuildTime(key string) bool {
	for _, k := range s.BuildTimeKeys {
		if k == key {
			return true
		}
	}
	return false
}

func (c *Client) GetEnvVars(projectID string) (map[string]string, error) {
	snap, err := c.GetEnvVarsSnapshot(projectID)
	if err != nil {
		return nil, err
	}
	return snap.Values, nil
}

func (c *Client) SetEnvVars(projectID string, envVars map[string]string) error {
	return c.SetEnvVarsWithBuildTime(projectID, envVars, nil, false)
}

// GetEnvVarsSnapshot fetches values + build-time markers for the project's
// site. Returns an error if the project has more than one site.
func (c *Client) GetEnvVarsSnapshot(projectID string) (*EnvVarsSnapshot, error) {
	sites, err := c.ListSites(projectID)
	if err != nil {
		return nil, err
	}
	if len(sites) == 0 {
		return nil, fmt.Errorf("project has no sites")
	}
	if len(sites) > 1 {
		return nil, fmt.Errorf("project has multiple sites; add 'site_id' to .ghayma.json and re-run")
	}
	return c.GetEnvVarsSnapshotBySite(projectID, sites[0].ID)
}

// SetEnvVarsWithBuildTime is the richer form that carries build-time markers.
func (c *Client) SetEnvVarsWithBuildTime(projectID string, envVars map[string]string, buildTimeKeys []string, force bool) error {
	sites, err := c.ListSites(projectID)
	if err != nil {
		return err
	}
	if len(sites) == 0 {
		return fmt.Errorf("project has no sites")
	}
	if len(sites) > 1 {
		return fmt.Errorf("project has multiple sites; add 'site_id' to .ghayma.json and re-run")
	}
	return c.SetEnvVarsBySiteWithBuildTime(projectID, sites[0].ID, envVars, buildTimeKeys, force)
}

// GetEnvVarsBySite fetches env vars for a specific site (values only —
// kept for callers that don't care about the build-time distinction).
func (c *Client) GetEnvVarsBySite(projectID, siteID string) (map[string]string, error) {
	snap, err := c.GetEnvVarsSnapshotBySite(projectID, siteID)
	if err != nil {
		return nil, err
	}
	return snap.Values, nil
}

// GetEnvVarsSnapshotBySite returns values and build-time markers.
func (c *Client) GetEnvVarsSnapshotBySite(projectID, siteID string) (*EnvVarsSnapshot, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		EnvVars       map[string]string `json:"env_vars"`
		BuildTimeKeys []string          `json:"build_time_keys"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.EnvVars == nil {
		result.EnvVars = make(map[string]string)
	}
	return &EnvVarsSnapshot{Values: result.EnvVars, BuildTimeKeys: result.BuildTimeKeys}, nil
}

// SetEnvVarsBySite replaces all env vars for a specific site (runtime-only).
func (c *Client) SetEnvVarsBySite(projectID, siteID string, envVars map[string]string) error {
	return c.SetEnvVarsBySiteWithBuildTime(projectID, siteID, envVars, nil, false)
}

// SetEnvVarsBySiteWithBuildTime replaces env vars and marks the listed keys
// as build-time. force=true bypasses the server's secret-pattern rejection.
func (c *Client) SetEnvVarsBySiteWithBuildTime(projectID, siteID string, envVars map[string]string, buildTimeKeys []string, force bool) error {
	envJSON, _ := json.Marshal(envVars)
	payload := map[string]interface{}{
		"env_vars":        string(envJSON),
		"build_time_keys": buildTimeKeys,
		"force":           force,
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("PUT", "/api/v1/projects/"+projectID+"/sites/"+siteID+"/env", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		var errResp struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("%s", errResp.Error)
		}
		return fmt.Errorf("failed: %s", string(respBody))
	}
	return nil
}

type DeploymentInfo struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	ImageTag      string `json:"image_tag"`
	CommitMessage string `json:"commit_message"`
	CreatedAt     string `json:"created_at"`
}

type RollbackResponse struct {
	ID      string   `json:"id"`
	Status  string   `json:"status"`
	Domains []string `json:"domains"`
}

func (c *Client) ListDeployments(projectID string) ([]DeploymentInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/deployments/project/"+projectID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var deployments []DeploymentInfo
	json.NewDecoder(resp.Body).Decode(&deployments)
	return deployments, nil
}

func (c *Client) Rollback(deploymentID string) (*RollbackResponse, error) {
	resp, err := c.authRequest("POST", "/api/v1/deployments/"+deploymentID+"/rollback", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed: %s", string(respBody))
	}

	var result RollbackResponse
	json.NewDecoder(resp.Body).Decode(&result)
	return &result, nil
}

// GetAppLogs fetches runtime log entries ({"entries":[…],"truncated":…})
// and renders them one per line as "<ts> [<pod>] <msg>". Returns "" when
// the app has no entries. The pre-2026-07 decoder read a legacy
// {"logs":"…"} shape, so every response printed as nothing — including
// during incidents when the logs were the whole point.
func (c *Client) GetAppLogs(projectID string, lines int) (string, error) {
	resp, err := c.authRequest("GET", fmt.Sprintf("/api/v1/projects/%s/logs?lines=%d", projectID, lines), nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed: %s", string(respBody))
	}

	var result struct {
		Entries []struct {
			TS  string `json:"ts"`
			Msg string `json:"msg"`
			Pod string `json:"pod"`
		} `json:"entries"`
		Truncated bool `json:"truncated"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("parse logs response: %w", err)
	}

	var b strings.Builder
	for _, e := range result.Entries {
		fmt.Fprintf(&b, "%s [%s] %s\n", e.TS, e.Pod, e.Msg)
	}
	if result.Truncated {
		b.WriteString("… (truncated — request more with --lines)\n")
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

func (c *Client) DeleteProject(projectID string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/projects/"+projectID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed: %s", string(respBody))
	}
	return nil
}

// Databases

type DatabaseInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	DBName      string `json:"db_name"`
	Username    string `json:"username"`
	StorageMB   int    `json:"storage_mb"`
	CPULimit    string `json:"cpu_limit"`
	MemoryLimit string `json:"memory_limit"`
	ProjectID   string `json:"project_id,omitempty"`
	ReplicaSet  bool   `json:"replica_set,omitempty"`
	CreatedAt   string `json:"created_at"`
	// Points-marketplace footprint fields (mirror managed_databases columns).
	// TierSlug/DiskGB/BackupTierSlug drive the resize preview and the
	// client-side grow-only disk check.
	TierSlug       string `json:"tier_slug,omitempty"`
	DiskGB         int    `json:"disk_gb,omitempty"`
	BackupTierSlug string `json:"backup_tier_slug,omitempty"`
}

// CreateDatabase creates a managed database. replicaSet is only meaningful for
// MongoDB and is sent only when non-nil so the server's default (true for new
// MongoDB instances) applies when the caller doesn't override it.
//
// The points-marketplace selectors are sent when set: tierSlug (wire field
// "tier"), diskGB ("disk_gb", omitted at 0 so the server falls back to
// ceil(size_mb/1024)), and backupSlug ("backup_tier_slug", omitted when blank
// so the server applies the free weekly default). Non-201 responses route
// through classifyAPIError so max-tier / insufficient-points / capacity classes
// render.
func (c *Client) CreateDatabase(name, dbType, projectID string, replicaSet *bool, tierSlug string, diskGB int, backupSlug string) (*DatabaseInfo, error) {
	payload := map[string]interface{}{"name": name, "type": dbType, "project_id": projectID}
	if replicaSet != nil {
		payload["replica_set"] = *replicaSet
	}
	if tierSlug != "" {
		payload["tier"] = tierSlug
	}
	if diskGB > 0 {
		payload["disk_gb"] = diskGB
	}
	if backupSlug != "" {
		payload["backup_tier_slug"] = backupSlug
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("POST", "/api/v1/databases", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, classifyAPIError(resp.StatusCode, respBody)
	}

	var result struct {
		Database DatabaseInfo `json:"database"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.Database, nil
}

// RetierDatabase changes a database's tier, disk, and/or backup schedule via
// PATCH /api/v1/databases/:id/tier. At least one of tier/diskGB/backupSlug must
// be meaningful; unset fields (blank slug, 0 disk) are omitted so a single-axis
// change doesn't clobber the others. Non-200 responses route through
// classifyAPIError so the marketplace classes render (disk grow-only is checked
// client-side before this call).
func (c *Client) RetierDatabase(id, tier string, diskGB int, backupSlug string) (*DatabaseInfo, error) {
	payload := map[string]interface{}{}
	if tier != "" {
		payload["tier"] = tier
	}
	if diskGB > 0 {
		payload["disk_gb"] = diskGB
	}
	if backupSlug != "" {
		payload["backup_tier_slug"] = backupSlug
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("PATCH", "/api/v1/databases/"+id+"/tier", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, classifyAPIError(resp.StatusCode, respBody)
	}

	var result struct {
		Database DatabaseInfo `json:"database"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.Database, nil
}

func (c *Client) ListDatabases() ([]DatabaseInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/databases", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Databases []DatabaseInfo `json:"databases"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Databases, nil
}

func (c *Client) GetDatabase(id string) (*DatabaseInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/databases/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("database not found")
	}

	var result struct {
		Database DatabaseInfo `json:"database"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.Database, nil
}

func (c *Client) DeleteDatabase(id string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/databases/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed: %s", string(respBody))
	}
	return nil
}

func (c *Client) LinkDatabase(dbID, projectID string) error {
	body, _ := json.Marshal(map[string]string{"project_id": projectID})
	resp, err := c.authRequest("POST", "/api/v1/databases/"+dbID+"/link", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) UnlinkDatabase(dbID string) error {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+dbID+"/unlink", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

// Helpers

func (c *Client) authRequest(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.cfg.APIHost+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	return c.http.Do(req)
}


func (c *Client) ExposeDatabase(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+id+"/expose", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", result["error"])
	}
	return result, nil
}

func (c *Client) UnexposeDatabase(id string) error {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+id+"/unexpose", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) GetDatabaseCredentials(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("GET", "/api/v1/databases/"+id+"/credentials", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", result["error"])
	}
	return result, nil
}

func (c *Client) StopDatabase(id string) error {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+id+"/stop", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) StartDatabase(id string) error {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+id+"/start", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) RotatePassword(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("POST", "/api/v1/databases/"+id+"/rotate", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", result["error"])
	}
	return result, nil
}


// Storage

type BucketInfo struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	GarageBucket      string `json:"garage_bucket"`
	StorageUsedBytes  int64  `json:"storage_used_bytes"`
	StorageLimitBytes int64  `json:"storage_limit_bytes"`
	IsPublic          bool   `json:"is_public"`
	ExternalAccess    bool   `json:"external_access"`
	Status            string `json:"status"`
	ProjectID         string `json:"project_id,omitempty"`
	CreatedAt         string `json:"created_at"`
}

// CreateBucket creates an object-storage bucket. sizeMB is the optional
// per-bucket quota in MB (0 = omit → the server applies the plan's default);
// the backend field is size_mb (there is NO quota_gb — the CLI's --quota-gb is
// converted to MB by the caller). Non-201 responses route through
// classifyAPIError so the marketplace insufficient-points / capacity classes
// render.
func (c *Client) CreateBucket(name, projectID string, sizeMB int) (*BucketInfo, error) {
	payload := map[string]interface{}{"name": name, "project_id": projectID}
	if sizeMB > 0 {
		payload["size_mb"] = sizeMB
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("POST", "/api/v1/storage", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, classifyAPIError(resp.StatusCode, respBody)
	}

	var result struct {
		Bucket BucketInfo `json:"bucket"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.Bucket, nil
}

func (c *Client) ListBuckets() ([]BucketInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/storage", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Buckets []BucketInfo `json:"buckets"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Buckets, nil
}

func (c *Client) GetBucket(id string) (*BucketInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/storage/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("bucket not found")
	}

	var result struct {
		Bucket BucketInfo `json:"bucket"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.Bucket, nil
}

func (c *Client) DeleteBucket(id string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/storage/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) GetBucketCredentials(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("GET", "/api/v1/storage/"+id+"/credentials", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Credentials map[string]interface{} `json:"credentials"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get credentials")
	}
	return result.Credentials, nil
}

func (c *Client) RotateBucketCredentials(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("POST", "/api/v1/storage/"+id+"/rotate", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		Credentials map[string]interface{} `json:"credentials"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to rotate credentials")
	}
	return result.Credentials, nil
}

func (c *Client) LinkBucket(bucketID, projectID string) error {
	body, _ := json.Marshal(map[string]string{"project_id": projectID})
	resp, err := c.authRequest("POST", "/api/v1/storage/"+bucketID+"/link", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) UnlinkBucket(bucketID string) error {
	resp, err := c.authRequest("POST", "/api/v1/storage/"+bucketID+"/unlink", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) ExposeBucket(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("POST", "/api/v1/storage/"+id+"/expose", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("%s", result["error"])
	}
	return result, nil
}

func (c *Client) UnexposeBucket(id string) error {
	resp, err := c.authRequest("POST", "/api/v1/storage/"+id+"/unexpose", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}


// Auth Apps

type AuthAppInfo struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	AppID                     string   `json:"app_id"`
	ProjectID                 string   `json:"project_id"`
	JWTExpirySeconds          int      `json:"jwt_expiry_seconds"`
	RefreshExpirySeconds      int      `json:"refresh_expiry_seconds"`
	AllowedOrigins            []string `json:"allowed_origins"`
	EmailVerificationRequired bool     `json:"email_verification_required"`
	GoogleClientID            string   `json:"google_client_id"`
	GitHubClientID            string   `json:"github_client_id"`
	Status                    string   `json:"status"`
	CreatedAt                 string   `json:"created_at"`
}

type AuthUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	EmailVerified bool   `json:"email_verified"`
	Provider      string `json:"provider"`
	Disabled      bool   `json:"disabled"`
	LastLoginAt   string `json:"last_login_at,omitempty"`
	CreatedAt     string `json:"created_at"`
}

// CreateAuthApp creates a managed auth app. authTierSlug is the optional
// user-capacity bracket (auth_tiers.slug — 1k/10k/100k/1m; blank = server
// default). 2FA/SMS are NOT set here (the create endpoint doesn't accept them);
// they are enabled afterward via UpdateAuthApp. Non-201 responses route through
// classifyAPIError so the marketplace insufficient-points / capacity classes
// render.
func (c *Client) CreateAuthApp(name, appID, projectID, authTierSlug string) (*AuthAppInfo, error) {
	payload := map[string]interface{}{"name": name, "app_id": appID, "project_id": projectID}
	if authTierSlug != "" {
		payload["auth_tier_slug"] = authTierSlug
	}
	body, _ := json.Marshal(payload)
	resp, err := c.authRequest("POST", "/api/v1/auth-apps", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 201 {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, classifyAPIError(resp.StatusCode, respBody)
	}

	var result struct {
		AuthApp AuthAppInfo `json:"auth_app"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.AuthApp, nil
}

func (c *Client) ListAuthApps() ([]AuthAppInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/auth-apps", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result struct {
		AuthApps []AuthAppInfo `json:"auth_apps"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.AuthApps, nil
}

func (c *Client) GetAuthApp(id string) (*AuthAppInfo, error) {
	resp, err := c.authRequest("GET", "/api/v1/auth-apps/"+id, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("auth app not found")
	}

	var result struct {
		AuthApp AuthAppInfo `json:"auth_app"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return &result.AuthApp, nil
}

// UpdateAuthApp PATCHes an auth app's settings (OAuth providers, expiries, and
// the points-priced two_fa_enabled / sms_enabled toggles). Non-200 responses
// route through classifyAPIError so a 409/503 points rejection when enabling
// 2FA/SMS surfaces as a typed *MarketplaceError (rendered via
// formatMarketplaceError), not swallowed as raw text.
func (c *Client) UpdateAuthApp(id string, updates map[string]interface{}) error {
	body, _ := json.Marshal(updates)
	resp, err := c.authRequest("PUT", "/api/v1/auth-apps/"+id, bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		respBody, _ := io.ReadAll(resp.Body)
		return classifyAPIError(resp.StatusCode, respBody)
	}
	return nil
}

func (c *Client) DeleteAuthApp(id string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/auth-apps/"+id, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) GetAuthAppStats(id string) (map[string]interface{}, error) {
	resp, err := c.authRequest("GET", "/api/v1/auth-apps/"+id+"/stats", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("failed to get stats")
	}
	return result, nil
}

func (c *Client) RotateAuthAppKeys(id string) error {
	resp, err := c.authRequest("POST", "/api/v1/auth-apps/"+id+"/rotate-keys", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) ListAuthAppUsers(appID string) ([]AuthUserInfo, int, error) {
	resp, err := c.authRequest("GET", "/api/v1/auth-apps/"+appID+"/users", nil)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	var result struct {
		Users []AuthUserInfo `json:"users"`
		Total int            `json:"total"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Users, result.Total, nil
}

func (c *Client) DisableAuthUser(appID, userID string) error {
	resp, err := c.authRequest("POST", "/api/v1/auth-apps/"+appID+"/users/"+userID+"/disable", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) EnableAuthUser(appID, userID string) error {
	resp, err := c.authRequest("POST", "/api/v1/auth-apps/"+appID+"/users/"+userID+"/enable", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}

func (c *Client) DeleteAuthUser(appID, userID string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/auth-apps/"+appID+"/users/"+userID, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		var errResp map[string]string
		json.NewDecoder(resp.Body).Decode(&errResp)
		return fmt.Errorf("%s", errResp["error"])
	}
	return nil
}
// ==================== Project Transfer ====================

type TransferInitiateResponse struct {
	TransferID string `json:"transfer_id"`
	ToEmail    string `json:"to_email"`
	ExpiresAt  string `json:"expires_at"`
	Message    string `json:"message"`
}

type TransferStatus struct {
	Pending     bool   `json:"pending"`
	TransferID  string `json:"transfer_id,omitempty"`
	ToEmail     string `json:"to_email,omitempty"`
	InitiatedAt string `json:"initiated_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
}

type TransferAcceptResponse struct {
	Message   string `json:"message"`
	ProjectID string `json:"project_id"`
}

// InitiateProjectTransfer asks the server to create a pending transfer to
// toEmail. The raw token is emailed to the recipient; the CLI never sees it.
func (c *Client) InitiateProjectTransfer(projectID, toEmail string) (*TransferInitiateResponse, error) {
	body, _ := json.Marshal(map[string]string{"to_email": toEmail})
	resp, err := c.authRequest("POST", "/api/v1/projects/"+projectID+"/transfer", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var out TransferInitiateResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetProjectTransferStatus returns the pending transfer (if any) for a project.
func (c *Client) GetProjectTransferStatus(projectID string) (*TransferStatus, error) {
	resp, err := c.authRequest("GET", "/api/v1/projects/"+projectID+"/transfer", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var out TransferStatus
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CancelProjectTransfer cancels the current project's pending transfer.
func (c *Client) CancelProjectTransfer(projectID string) error {
	resp, err := c.authRequest("DELETE", "/api/v1/projects/"+projectID+"/transfer", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return decodeAPIError(resp)
	}
	return nil
}

// AcceptProjectTransfer consumes a raw base64url token (as received in the
// invite email) and completes the transfer. The server hashes internally.
func (c *Client) AcceptProjectTransfer(rawToken string) (*TransferAcceptResponse, error) {
	resp, err := c.authRequest("POST", "/api/v1/transfers/accept/"+rawToken, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeAPIError(resp)
	}
	var out TransferAcceptResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}

// decodeAPIError reads a JSON {"error": "..."} body and returns a Go error
// with the server's message. Falls back to the raw body on parse failure.
func decodeAPIError(resp *http.Response) error {
	raw, _ := io.ReadAll(resp.Body)
	var errResp struct{ Error string `json:"error"` }
	if json.Unmarshal(raw, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("%s", errResp.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(raw))
}

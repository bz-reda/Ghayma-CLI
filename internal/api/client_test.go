package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"paas-cli/internal/config"
)

func newTestClient(url string) *Client {
	return NewClient(&config.Config{APIHost: url, Token: "test-token"})
}

// TestEligibleBillingAccounts pins the project-create filter: only
// active accounts the caller owns or admins are eligible (mirrors the
// dashboard gate). Suspended/closed accounts and viewer-role accounts
// must be excluded so init never offers an account the API would reject.
func TestEligibleBillingAccounts(t *testing.T) {
	accounts := []BillingAccount{
		{ID: "1", Name: "personal", Status: "active", Role: "owner", IsPersonal: true},
		{ID: "2", Name: "team-admin", Status: "active", Role: "admin"},
		{ID: "3", Name: "team-viewer", Status: "active", Role: "viewer"}, // drop: viewer
		{ID: "4", Name: "suspended", Status: "suspended", Role: "owner"}, // drop: not active
		{ID: "5", Name: "closed", Status: "closed", Role: "owner"},       // drop: not active
	}
	got := EligibleBillingAccounts(accounts)
	if len(got) != 2 {
		t.Fatalf("got %d eligible; want 2 (active owner+admin). got=%+v", len(got), got)
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Errorf("eligible ids = %s,%s; want 1,2 (preserves order)", got[0].ID, got[1].ID)
	}
}

// TestListBillingAccounts_ParsesEnvelope confirms the client unwraps the
// {"accounts":[...]} envelope and sends the Bearer token.
func TestListBillingAccounts_ParsesEnvelope(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/billing-accounts" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, `{"accounts":[{"id":"a1","name":"Personal","status":"active","role":"owner","is_personal":true}]}`)
	}))
	defer ts.Close()

	accounts, err := newTestClient(ts.URL).ListBillingAccounts()
	if err != nil {
		t.Fatalf("ListBillingAccounts: %v", err)
	}
	if len(accounts) != 1 || accounts[0].ID != "a1" || !accounts[0].IsPersonal {
		t.Errorf("parsed = %+v; want one personal account a1", accounts)
	}
}

// TestCreateProject_SendsBillingAccountID is the core regression pin for
// the CLI billing blocker: CreateProject MUST include billing_account_id
// in the POST body when provided.
func TestCreateProject_SendsBillingAccountID(t *testing.T) {
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"p1","name":"demo","slug":"demo","framework":"nextjs"}`)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).CreateProject("demo", "nextjs", "acct-123", "")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.ID != "p1" {
		t.Errorf("project id = %q; want p1", p.ID)
	}
	if gotBody["billing_account_id"] != "acct-123" {
		t.Errorf("body billing_account_id = %q; want acct-123 (body=%v)", gotBody["billing_account_id"], gotBody)
	}
	if gotBody["name"] != "demo" || gotBody["framework"] != "nextjs" {
		t.Errorf("body name/framework wrong: %v", gotBody)
	}
}

// TestCreateProject_SendsPlan pins the Task 6 fix: a chosen plan slug is
// sent as "plan" in the POST body so init no longer silently defaults every
// new project to hobby.
func TestCreateProject_SendsPlan(t *testing.T) {
	var gotBody map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"p1","name":"demo","slug":"demo"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).CreateProject("demo", "nextjs", "acct-123", "pro"); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if gotBody["plan"] != "pro" {
		t.Errorf("body plan = %q; want pro (body=%v)", gotBody["plan"], gotBody)
	}
}

// TestCreateProject_OmitsPlanWhenEmpty confirms an empty plan is absent from
// the body (not sent as "") so the server's default applies — the exact
// pre-Task-6 behavior for callers that don't choose a plan.
func TestCreateProject_OmitsPlanWhenEmpty(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"p1"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).CreateProject("demo", "nextjs", "acct-123", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, present := gotBody["plan"]; present {
		t.Errorf("plan should be omitted when empty; body=%v", gotBody)
	}
}

// TestCreateProject_OmitsBillingAccountWhenEmpty confirms the field is
// absent (not sent as "") when no account is supplied, so the server's
// own default/validation applies cleanly.
func TestCreateProject_OmitsBillingAccountWhenEmpty(t *testing.T) {
	var gotBody map[string]json.RawMessage
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"p1"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).CreateProject("demo", "nextjs", "", ""); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, present := gotBody["billing_account_id"]; present {
		t.Errorf("billing_account_id should be omitted when empty; body=%v", gotBody)
	}
}

// TestGetPlans_ParsesFixedPlans pins the 200 round-trip: correct path, Bearer
// header, and the fixed_plans array parsed with the price/points/max-tier
// fields the plan picker needs (payg is ignored).
func TestGetPlans_ParsesFixedPlans(t *testing.T) {
	const plansJSON = `{
	  "fixed_plans": [
	    {"slug":"hobby","display_name":"Hobby","price_dzd_per_month":2500,"points":10,"max_app_tier":"b","max_db_tier":"s"},
	    {"slug":"pro","display_name":"Pro","price_dzd_per_month":9000,"points":40,"max_app_tier":"c","max_db_tier":"m"}
	  ],
	  "payg": {"slug":"pay_as_you_go"}
	}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/billing/plans" || r.Method != http.MethodGet {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("auth header = %q; want Bearer test-token", got)
		}
		io.WriteString(w, plansJSON)
	}))
	defer ts.Close()

	plans, err := newTestClient(ts.URL).GetPlans()
	if err != nil {
		t.Fatalf("GetPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("got %d plans; want 2", len(plans))
	}
	if plans[0].Slug != "hobby" || plans[0].PriceDZDPerMonth != 2500 || plans[0].Points != 10 {
		t.Errorf("plans[0] = %+v; want hobby 2500 DZD 10 pts", plans[0])
	}
	if plans[1].Slug != "pro" || plans[1].MaxAppTier != "c" || plans[1].MaxDBTier != "m" {
		t.Errorf("plans[1] = %+v; want pro max tiers c/m", plans[1])
	}
}

// TestGetPlans_404_Errors pins the fail-soft seam: an older/self-hosted server
// without the endpoint returns an error so init falls back to the server
// default plan rather than crashing.
func TestGetPlans_404_Errors(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"error":"404 page not found"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).GetPlans(); err == nil {
		t.Fatal("want error on 404")
	}
}

// TestCreateProject_DecodesAPIError confirms a non-201 surfaces the
// server's {"error":...} message (e.g. the BILLING_ACCOUNT_REQUIRED 400)
// rather than the old raw-JSON dump.
func TestCreateProject_DecodesAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		io.WriteString(w, `{"error":"billing_account_id is required for plan=hobby","code":"BILLING_ACCOUNT_REQUIRED"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).CreateProject("demo", "nextjs", "", "")
	if err == nil {
		t.Fatal("want error on 400")
	}
	if !strings.Contains(err.Error(), "billing_account_id is required") {
		t.Errorf("error = %q; want the decoded server message", err.Error())
	}
	if strings.Contains(err.Error(), "failed to create project: {") {
		t.Errorf("error still dumps the raw JSON body: %q", err.Error())
	}
}

// TestRegister_InviteCodeInBody pins the gate contract: the invite code is
// sent as invite_code only when set, so an un-flagged register on an open
// server stays byte-identical to the pre-beta payload.
func TestRegister_InviteCodeInBody(t *testing.T) {
	var got map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/auth/register" || r.Method != http.MethodPost {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		got = map[string]string{}
		json.Unmarshal(raw, &got)
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"user":{"id":"u1","email":"t@t.dz","name":"t"}}`))
	}))
	defer ts.Close()
	c := newTestClient(ts.URL)

	if _, err := c.Register("t@t.dz", "password123", "t", "GYB-AB12CD34"); err != nil {
		t.Fatalf("register with invite: %v", err)
	}
	if got["invite_code"] != "GYB-AB12CD34" {
		t.Errorf("invite_code = %q; want GYB-AB12CD34", got["invite_code"])
	}

	if _, err := c.Register("t@t.dz", "password123", "t", ""); err != nil {
		t.Fatalf("register without invite: %v", err)
	}
	if _, present := got["invite_code"]; present {
		t.Error("empty invite must not add an invite_code key to the payload")
	}
}

// TestRegister_SurfacesGateRefusal pins that a beta-gate 403 comes back as an
// *APIError carrying the server's message AND its machine code, which the
// register command matches to print the --invite hint.
func TestRegister_SurfacesGateRefusal(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"error":"Ghayma is in private beta. A valid invitation code is required to register.","code":"invite_required"}`))
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).Register("t@t.dz", "password123", "t", "")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Status != http.StatusForbidden || apiErr.Code != "invite_required" {
		t.Errorf("got status=%d code=%q; want 403 invite_required", apiErr.Status, apiErr.Code)
	}
	if !strings.Contains(apiErr.Message, "private beta") {
		t.Errorf("message %q lost the server's text", apiErr.Message)
	}
}

// createWithChoice is one of the three creates that take the sites to connect
// the new service to.
type createWithChoice struct {
	kind     string
	path     string
	resource string // the resource half of the 201 body
	id       string
	create   func(c *Client, siteIDs []string) (string, *ConnectChoice, error)
}

var createsWithChoice = []createWithChoice{
	{"database", "/api/v1/databases", `"database":{"id":"d1","name":"shop-db","type":"postgres"}`, "d1",
		func(c *Client, siteIDs []string) (string, *ConnectChoice, error) {
			db, choice, err := c.CreateDatabase("shop-db", "postgres", "p1", nil, "", 0, "", siteIDs)
			if err != nil {
				return "", choice, err
			}
			return db.ID, choice, nil
		}},
	{"bucket", "/api/v1/storage", `"bucket":{"id":"b1","name":"media"}`, "b1",
		func(c *Client, siteIDs []string) (string, *ConnectChoice, error) {
			bucket, choice, err := c.CreateBucket("media", "p1", 0, siteIDs)
			if err != nil {
				return "", choice, err
			}
			return bucket.ID, choice, nil
		}},
	{"auth app", "/api/v1/auth-apps", `"auth_app":{"id":"a1","name":"shop-auth","app_id":"shop-auth"}`, "a1",
		func(c *Client, siteIDs []string) (string, *ConnectChoice, error) {
			app, choice, err := c.CreateAuthApp("shop-auth", "shop-auth", "p1", "", siteIDs)
			if err != nil {
				return "", choice, err
			}
			return app.ID, choice, nil
		}},
}

// createServer answers a create with a 201 carrying body and records the
// request body it received.
func createServer(t *testing.T, path, body string) (*httptest.Server, map[string]json.RawMessage) {
	t.Helper()
	sent := map[string]json.RawMessage{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != path {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &sent)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, sent
}

// TestCreates_SendTheSiteChoice: every create body states which sites to
// connect; no choice is [] — never null, never a missing key.
func TestCreates_SendTheSiteChoice(t *testing.T) {
	for _, tc := range createsWithChoice {
		for _, choice := range []struct {
			siteIDs []string
			want    string
		}{
			{[]string{"s1", "s2"}, `["s1","s2"]`},
			{nil, `[]`},
		} {
			ts, sent := createServer(t, tc.path, "{"+tc.resource+"}")
			if _, _, err := tc.create(newTestClient(ts.URL), choice.siteIDs); err != nil {
				t.Fatalf("%s: %v", tc.kind, err)
			}
			if got := string(sent["connect_site_ids"]); got != choice.want {
				t.Errorf("%s with %q: connect_site_ids = %s; want %s", tc.kind, choice.siteIDs, got, choice.want)
			}
		}
	}
}

// TestCreates_DecodeTheConnections: the 201 reports, beside the resource, what
// became of each chosen site.
func TestCreates_DecodeTheConnections(t *testing.T) {
	const connections = `"connections":{"connected":["s1"],"pending":["s2"],"failed":[{"site_id":"s3","error":"could not reach the database"}]}`
	for _, tc := range createsWithChoice {
		ts, _ := createServer(t, tc.path, "{"+tc.resource+","+connections+"}")
		id, choice, err := tc.create(newTestClient(ts.URL), []string{"s1", "s2", "s3"})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		if id != tc.id {
			t.Errorf("%s: id = %q; want %q", tc.kind, id, tc.id)
		}
		if choice == nil {
			t.Fatalf("%s: choice = nil; want the reported connections", tc.kind)
		}
		if !reflect.DeepEqual(choice.Connected, []string{"s1"}) || !reflect.DeepEqual(choice.Pending, []string{"s2"}) {
			t.Errorf("%s: connected %v, pending %v; want [s1], [s2]", tc.kind, choice.Connected, choice.Pending)
		}
		if want := []SiteConnectFailure{{SiteID: "s3", Error: "could not reach the database"}}; !reflect.DeepEqual(choice.Failed, want) {
			t.Errorf("%s: failed = %+v; want %+v", tc.kind, choice.Failed, want)
		}
	}
}

// TestCreates_WithoutConnectionsIsNil: a server that predates the field
// answers without it and may still connect on its own, so the create succeeds
// with no choice at all — never an empty one, which reads as "connected
// nothing". A server that sends the field, even empty, has answered.
func TestCreates_WithoutConnectionsIsNil(t *testing.T) {
	for _, tc := range createsWithChoice {
		ts, _ := createServer(t, tc.path, "{"+tc.resource+"}")
		id, choice, err := tc.create(newTestClient(ts.URL), []string{"s1"})
		if err != nil || id != tc.id {
			t.Fatalf("%s: id, err = %q, %v; want %q", tc.kind, id, err, tc.id)
		}
		if choice != nil {
			t.Errorf("%s: choice = %+v; want nil", tc.kind, *choice)
		}

		ts, _ = createServer(t, tc.path, "{"+tc.resource+`,"connections":{"connected":[],"pending":[],"failed":[]}}`)
		if _, choice, err = tc.create(newTestClient(ts.URL), nil); err != nil || choice == nil {
			t.Errorf("%s: choice, err = %v, %v; want an empty choice, not nil", tc.kind, choice, err)
		}
	}
}

// TestCreates_UnreadableConnectionsAreUnknown: the resource exists once the
// server answers 201, so connections this CLI cannot read leave the choice
// unknown (nil) rather than half-read, and never fail the create.
func TestCreates_UnreadableConnectionsAreUnknown(t *testing.T) {
	for _, tc := range createsWithChoice {
		ts, _ := createServer(t, tc.path, "{"+tc.resource+`,"connections":{"connected":"s1"}}`)
		id, choice, err := tc.create(newTestClient(ts.URL), []string{"s1"})
		if err != nil || id != tc.id {
			t.Fatalf("%s: id, err = %q, %v; want %q and no error", tc.kind, id, err, tc.id)
		}
		if choice != nil {
			t.Errorf("%s: choice = %+v; want nil", tc.kind, *choice)
		}
	}
}

// uploadServer answers the deploy upload with a 201 carrying body and records
// the connect_resources form values (none when the upload carried no field).
func uploadServer(t *testing.T, body string) (*httptest.Server, *[]string) {
	t.Helper()
	var connect []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/deploy/upload" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			t.Errorf("upload is not a multipart form: %v", err)
			return
		}
		connect = r.MultipartForm.Value["connect_resources"]
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, body)
	}))
	t.Cleanup(ts.Close)
	return ts, &connect
}

func testDeploy(t *testing.T, url string, connect []ConnectionItem) (*DeployResponse, error) {
	return newTestClient(url).Deploy("p1", "s1", t.TempDir(), "CLI deploy", false, "", "", DeployBuildConfig{}, nil, connect)
}

// TestDeploy_SendsConnectResourcesOnlyWhenChosen: the chosen services ride on
// the upload as one JSON array; with none the field is left out, so a server
// that predates it never sees it.
func TestDeploy_SendsConnectResourcesOnlyWhenChosen(t *testing.T) {
	for _, tc := range []struct {
		name    string
		connect []ConnectionItem
		want    []string
	}{
		{"nil", nil, nil},
		{"empty", []ConnectionItem{}, nil},
		{"two services", []ConnectionItem{{Kind: "database", ResourceID: "d1"}, {Kind: "bucket", ResourceID: "b1"}},
			[]string{`[{"kind":"database","resource_id":"d1"},{"kind":"bucket","resource_id":"b1"}]`}},
	} {
		ts, sent := uploadServer(t, `{"deployment_id":"dep-1","status":"queued"}`)
		if _, err := testDeploy(t, ts.URL, tc.connect); err != nil {
			t.Fatalf("%s: Deploy: %v", tc.name, err)
		}
		if !reflect.DeepEqual(*sent, tc.want) {
			t.Errorf("%s: connect_resources = %q; want %q", tc.name, *sent, tc.want)
		}
	}
}

// TestDeploy_DecodesTheConnections: the upload answers with the site it
// deploys and, keyed by resource, what became of each chosen service; a server
// that predates the fields leaves both empty.
func TestDeploy_DecodesTheConnections(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want DeployResponse
	}{
		{
			name: "connected and failed",
			body: `{"deployment_id":"dep-1","status":"queued","site_id":"s1","connections":{"connected":[{"kind":"database","resource_id":"d1"}],"failed":[{"kind":"bucket","resource_id":"b1","error":"bucket 'media' could not be connected"}]}}`,
			want: DeployResponse{
				DeploymentID: "dep-1",
				Status:       "queued",
				SiteID:       "s1",
				Connections: DeployConnections{
					Connected: []ConnectionItem{{Kind: "database", ResourceID: "d1"}},
					Failed:    []DeployConnectFailure{{Kind: "bucket", ResourceID: "b1", Error: "bucket 'media' could not be connected"}},
				},
			},
		},
		{
			name: "older server",
			body: `{"deployment_id":"dep-1","status":"queued"}`,
			want: DeployResponse{DeploymentID: "dep-1", Status: "queued"},
		},
	} {
		ts, _ := uploadServer(t, tc.body)
		resp, err := testDeploy(t, ts.URL, []ConnectionItem{{Kind: "database", ResourceID: "d1"}, {Kind: "bucket", ResourceID: "b1"}})
		if err != nil {
			t.Fatalf("%s: Deploy: %v", tc.name, err)
		}
		if !reflect.DeepEqual(*resp, tc.want) {
			t.Errorf("%s: response = %+v; want %+v", tc.name, *resp, tc.want)
		}
	}
}

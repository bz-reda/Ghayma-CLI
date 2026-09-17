package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Environments client (ENVIRONMENTS-DESIGN-2026-09-17). These pin the wire
// contract the CLI's rendering rests on: the machine code a refusal carries,
// what an omitted environment sends, and the shape of the resolved listing.

// TestCreateSite_EnvironmentIsOptionalOnTheWire: an omitted kind must leave the
// key OUT, so the server applies its own default (development for a secondary
// site) and a backend that predates the field is unaffected.
func TestCreateSite_EnvironmentIsOptionalOnTheWire(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"s9","name":"dev","slug":"dev","environment":"development","inherit_env":true}`)
	}))
	defer ts.Close()

	site, err := newTestClient(ts.URL).CreateSite("p1", "dev", "")
	if err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if _, present := body["environment"]; present {
		t.Errorf("body = %v; an unnamed kind must not be sent", body)
	}
	if site.Environment != "development" || !site.InheritEnv {
		t.Errorf("site = %+v; want the server's answer decoded", site)
	}

	if _, err := newTestClient(ts.URL).CreateSite("p1", "stg", "staging"); err != nil {
		t.Fatalf("CreateSite: %v", err)
	}
	if body["environment"] != "staging" {
		t.Errorf("body = %v; a named kind travels verbatim", body)
	}
}

// TestSetSiteEnvironment_CarriesTheRefusalCode: the CLI branches on the code,
// not the prose, so the code has to survive the decode.
func TestSetSiteEnvironment_CarriesTheRefusalCode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/projects/p1/sites/s1/environment" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusConflict)
		io.WriteString(w, `{"error":"the default site is always the production environment and cannot be changed","code":"default_site_environment_locked"}`)
	}))
	defer ts.Close()

	_, err := newTestClient(ts.URL).SetSiteEnvironment("p1", "s1", "development")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v; want *APIError", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != CodeDefaultSiteEnvironmentLocked {
		t.Errorf("err = %+v; want the 409 and its code", apiErr)
	}
	if apiErr.Message == "" {
		t.Error("the server's own sentence must survive — it is what the user reads")
	}
}

// TestSetSiteInheritEnv_SendsFalseExplicitly: false is the meaningful "stop
// inheriting" value and the server binds a *bool, so it must be on the wire.
func TestSetSiteInheritEnv_SendsFalseExplicitly(t *testing.T) {
	var raw string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		raw = string(b)
		io.WriteString(w, `{"id":"s2","slug":"dev","inherit_env":false}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).SetSiteInheritEnv("p1", "s2", false); err != nil {
		t.Fatalf("SetSiteInheritEnv: %v", err)
	}
	if raw != `{"inherit_env":false}` {
		t.Errorf("body = %s; want an explicit false", raw)
	}
}

// TestGetEnvVarsSnapshotBySite_DecodesTheLadder: the own rows keep their old
// meaning (they are what the replace-all PUT takes) while the resolved rows
// arrive alongside them.
func TestGetEnvVarsSnapshotBySite_DecodesTheLadder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env_vars":{"API_URL":"https://dev"},"build_time_keys":["API_URL"],`+
			`"vars":[{"key":"API_URL","value":"https://dev","build_time":true,"source":"own"},`+
			`{"key":"REGION","value":"eu","source":"inherited"}],`+
			`"inherit_env":true,"inherited_from":{"site_id":"s1","slug":"main"}}`)
	}))
	defer ts.Close()

	snap, err := newTestClient(ts.URL).GetEnvVarsSnapshotBySite("p1", "s2")
	if err != nil {
		t.Fatalf("GetEnvVarsSnapshotBySite: %v", err)
	}
	if len(snap.Values) != 1 || snap.Values["API_URL"] != "https://dev" {
		t.Errorf("own rows = %v; the write path's mirror must not become the resolved set", snap.Values)
	}
	if len(snap.Vars) != 2 || snap.Vars[1].Source != SourceInherited {
		t.Errorf("vars = %+v; want the resolved ladder with provenance", snap.Vars)
	}
	if !snap.InheritEnv || snap.InheritedFrom == nil || snap.InheritedFrom.Slug != "main" {
		t.Errorf("snapshot = %+v; want the base site named", snap)
	}

	// A platform without inheritance sends neither key, and nothing may be
	// invented from their absence.
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env_vars":{"A":"1"},"build_time_keys":[]}`)
	}))
	defer old.Close()
	snap, err = newTestClient(old.URL).GetEnvVarsSnapshotBySite("p1", "s2")
	if err != nil || len(snap.Vars) != 0 || snap.InheritEnv || snap.InheritedFrom != nil {
		t.Errorf("old-shape snapshot = %+v, %v; want no ladder", snap, err)
	}
}

// TestDeleteEnvVar_EscapesTheKeyAndDecodesTheAnswer.
func TestDeleteEnvVar_EscapesTheKeyAndDecodesTheAnswer(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		io.WriteString(w, `{"key":"API_URL","deleted":true,"inherited_value_restored":true,"inherited_from":"main"}`)
	}))
	defer ts.Close()

	res, err := newTestClient(ts.URL).DeleteEnvVar("p1", "s2", "API/URL")
	if err != nil {
		t.Fatalf("DeleteEnvVar: %v", err)
	}
	if path != "/api/v1/projects/p1/sites/s2/env/API%2FURL" {
		t.Errorf("path = %q; a key is one path segment, never a new one", path)
	}
	if !res.InheritedValueRestored || res.InheritedFrom != "main" {
		t.Errorf("result = %+v; want what the delete actually did", res)
	}
}

// TestEffectiveSiteEnv_CarriesProvenance, and never nil maps: `env pull`
// iterates both without nil checks.
func TestEffectiveSiteEnv_CarriesProvenance(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":{"DATABASE_URL":"postgresql://dev","REGION":"eu"},"sources":{"DATABASE_URL":"derived","REGION":"inherited"}}`)
	}))
	defer ts.Close()

	out, err := newTestClient(ts.URL).EffectiveSiteEnv("p1", "s2")
	if err != nil {
		t.Fatalf("EffectiveSiteEnv: %v", err)
	}
	if out.Sources["DATABASE_URL"] != SourceDerived || out.Sources["REGION"] != SourceInherited {
		t.Errorf("sources = %v; a derived credential must never read as inherited", out.Sources)
	}

	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"env":{"A":"1"}}`)
	}))
	defer bare.Close()
	out, err = newTestClient(bare.URL).EffectiveSiteEnv("p1", "s2")
	if err != nil || out.Sources == nil || len(out.Sources) != 0 {
		t.Errorf("out, err = %+v, %v; a server without provenance yields an empty map, not nil", out, err)
	}
}

// TestPromoteSite_PayloadAndDecode: an omitted deployment means "the live one",
// so the key must be absent rather than empty.
func TestPromoteSite_PayloadAndDecode(t *testing.T) {
	var body map[string]any
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"dep-9","site_id":"s1","status":"queued","trigger":"promote","source_image_ref":"p1-dev:t","source_image_digest":"sha256:abc","source_site":"dev","source_deployment_id":"dep-1"}`)
	}))
	defer ts.Close()

	p, err := newTestClient(ts.URL).PromoteSite("p1", "s1", "dev", "", "")
	if err != nil {
		t.Fatalf("PromoteSite: %v", err)
	}
	if path != "/api/v1/projects/p1/sites/s1/promote" {
		t.Errorf("path = %q; the TARGET site owns the route", path)
	}
	if body["from_site"] != "dev" {
		t.Errorf("body = %v; want the source", body)
	}
	if _, present := body["deployment_id"]; present {
		t.Errorf("body = %v; an unnamed deployment means the source's live one", body)
	}
	if p.Trigger != "promote" || p.SourceSite != "dev" || p.SourceImageDigest != "sha256:abc" {
		t.Errorf("promotion = %+v; want the provenance decoded", p)
	}
}

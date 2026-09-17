package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestMintRegistryToken_RoundTrip pins the mint call `ghayma docker push`
// makes: POST /registry/token with the project (and site, when there is one),
// and the credential read back out — the repository name included, which the
// CLI must take from the server rather than derive from the slugs.
func TestMintRegistryToken_RoundTrip(t *testing.T) {
	var (
		gotMethod, gotPath, gotAuth string
		gotBody                     map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		io.WriteString(w, `{"username":"dev@example.com","token":"ghr_secret","repo":"shop-admin","server":"registry.ghayma.tech","expires_at":"2026-09-17T12:30:00Z"}`)
	}))
	defer ts.Close()

	cred, err := newTestClient(ts.URL).MintRegistryToken("p1", "s2")
	if err != nil {
		t.Fatalf("MintRegistryToken: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/registry/token" {
		t.Errorf("request = %s %s; want POST /api/v1/registry/token", gotMethod, gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q; the mint is a normal bearer-auth call", gotAuth)
	}
	if gotBody["project"] != "p1" || gotBody["site"] != "s2" {
		t.Errorf("body = %v; want the project and site", gotBody)
	}
	if cred.Repo != "shop-admin" || cred.Server != "registry.ghayma.tech" || cred.Token != "ghr_secret" || cred.Username != "dev@example.com" {
		t.Errorf("credential = %+v; want every field of the mint response", cred)
	}
}

// TestMintRegistryToken_SiteLessOmitsTheSite: an empty site is how a project
// with no site row yet is authorized for its own main repository. Sending
// `"site": ""` instead would be a site reference the server has to look up.
func TestMintRegistryToken_SiteLessOmitsTheSite(t *testing.T) {
	var body map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		io.WriteString(w, `{"token":"ghr_x","repo":"shop","server":"registry.ghayma.tech"}`)
	}))
	defer ts.Close()

	if _, err := newTestClient(ts.URL).MintRegistryToken("p1", "  "); err != nil {
		t.Fatalf("MintRegistryToken: %v", err)
	}
	if _, present := body["site"]; present {
		t.Errorf("body = %v; a site-less mint must omit the site key", body)
	}
}

// TestMintRegistryToken_Refusals: the server's own message is what the user
// needs to read, carried as *APIError.
func TestMintRegistryToken_Refusals(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusForbidden, `{"error":"insufficient role"}`)
	_, err := newTestClient(ts.URL).MintRegistryToken("p1", "")
	if err == nil || !strings.Contains(err.Error(), "insufficient role") {
		t.Errorf("err = %v; want the server's refusal", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Errorf("err = %v; want an *APIError carrying 403", err)
	}
}

// TestNewImageDeployRequest_TagVersusDigest pins the routing: a sha256:
// reference is a digest, everything else is a tag, and exactly one field is
// ever populated — the endpoint refuses both.
func TestNewImageDeployRequest_TagVersusDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	cases := []struct{ ref, wantTag, wantDigest string }{
		{"v1", "v1", ""},
		{"  v1  ", "v1", ""},
		{"latest", "latest", ""},
		{digest, "", digest},
	}
	for _, tc := range cases {
		req := newImageDeployRequest(tc.ref, "", false)
		if req.Tag != tc.wantTag || req.Digest != tc.wantDigest {
			t.Errorf("newImageDeployRequest(%q) = tag %q / digest %q; want %q / %q", tc.ref, req.Tag, req.Digest, tc.wantTag, tc.wantDigest)
		}
		raw, _ := json.Marshal(req)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		if _, tag := body["tag"]; tag {
			if _, dig := body["digest"]; dig {
				t.Errorf("%q serialized both tag and digest: %s", tc.ref, raw)
			}
		}
	}
}

// TestDeployImage_RoundTrip pins the path, the body and the created row — its
// id key is `id`, not the upload endpoint's `deployment_id`.
func TestDeployImage_RoundTrip(t *testing.T) {
	var (
		gotMethod, gotPath string
		gotBody            map[string]any
	)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"dep-1","site_id":"s1","status":"queued","trigger":"image","source_image_ref":"v1"}`)
	}))
	defer ts.Close()

	dep, err := newTestClient(ts.URL).DeployImage("p1", "s1", "v1", "", true)
	if err != nil {
		t.Fatalf("DeployImage: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v1/projects/p1/sites/s1/deployments/image" {
		t.Errorf("request = %s %s; want the site-scoped image entry", gotMethod, gotPath)
	}
	if gotBody["tag"] != "v1" || gotBody["is_production"] != true {
		t.Errorf("body = %v; want the tag and is_production", gotBody)
	}
	if _, present := gotBody["commit_message"]; present {
		t.Errorf("body = %v; an empty commit message is omitted so the server labels the row", gotBody)
	}
	if dep.ID != "dep-1" || dep.Trigger != "image" || dep.Status != "queued" {
		t.Errorf("deployment = %+v; want the created row", dep)
	}
}

// TestDeployImage_RefusalCarriesTheStatus: the CLI branches on the status for
// a mid-rename conflict, so it must survive the round trip.
func TestDeployImage_RefusalCarriesTheStatus(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusConflict, `{"error":"this site is being renamed"}`)
	_, err := newTestClient(ts.URL).DeployImage("p1", "s1", "v1", "", false)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || !strings.Contains(apiErr.Message, "renamed") {
		t.Errorf("err = %v; want an *APIError carrying 409 and the message", err)
	}
}

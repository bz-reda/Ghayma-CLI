package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

// TestDeploy_SendsBuilderOnlyWhenSet: the manifest's builder rides on the
// upload as the `builder` form field, and an empty one is never sent, so the
// server keeps the site's stored choice.
func TestDeploy_SendsBuilderOnlyWhenSet(t *testing.T) {
	for _, tc := range []struct {
		builder string
		want    []string
	}{
		{"", nil},
		{"auto", []string{"auto"}},
		{"dockerfile", []string{"dockerfile"}},
	} {
		var got []string
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				t.Errorf("upload is not a multipart form: %v", err)
				return
			}
			got = r.MultipartForm.Value["builder"]
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"deployment_id":"dep-1","status":"queued"}`)
		}))
		bc := DeployBuildConfig{Builder: tc.builder}
		_, err := newTestClient(ts.URL).Deploy("p1", "s1", t.TempDir(), "CLI deploy", false, "", "", bc, nil, nil)
		ts.Close()
		if err != nil {
			t.Fatalf("builder %q: Deploy: %v", tc.builder, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("builder %q: form builder = %q; want %q", tc.builder, got, tc.want)
		}
	}
}

// TestListSites_DecodesTheBuilder: the site list carries each site's stored
// builder under build_settings; a server that predates the field leaves it
// empty, which deploy reads as "ask the project".
func TestListSites_DecodesTheBuilder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `[
			{"id":"s1","slug":"web","build_settings":{"builder":"dockerfile","dockerfile_path":"docker/Dockerfile"}},
			{"id":"s2","slug":"api","build_settings":{"builder":"auto"}},
			{"id":"s3","slug":"old"}
		]`)
	}))
	defer ts.Close()

	sites, err := newTestClient(ts.URL).ListSites("p1")
	if err != nil {
		t.Fatalf("ListSites: %v", err)
	}
	got := []string{sites[0].BuildSettings.Builder, sites[1].BuildSettings.Builder, sites[2].BuildSettings.Builder}
	if want := []string{"dockerfile", "auto", ""}; !reflect.DeepEqual(got, want) {
		t.Errorf("builders = %q; want %q", got, want)
	}
}

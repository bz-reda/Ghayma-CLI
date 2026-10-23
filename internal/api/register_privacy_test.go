package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientRegister_SendsPrivacyVersion(t *testing.T) {
	var got map[string]string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = map[string]string{}
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"user":{"id":"u1","email":"t@t.dz","name":"t"}}`))
	}))
	defer ts.Close()
	c := newTestClient(ts.URL)

	if _, err := c.Register("t@t.dz", "password123", "t", "", "1.0"); err != nil {
		t.Fatalf("register with privacy version: %v", err)
	}
	if got["privacy_version"] != "1.0" {
		t.Errorf("privacy_version = %q; want 1.0", got["privacy_version"])
	}

	if _, err := c.Register("t@t.dz", "password123", "t", "", ""); err != nil {
		t.Fatalf("register without privacy version: %v", err)
	}
	if _, present := got["privacy_version"]; present {
		t.Error("empty privacy version must not add a privacy_version key to the payload")
	}
}

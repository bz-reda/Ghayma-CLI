package api

import (
	"os"
	"reflect"
	"strings"
	"testing"
)

// The project a managed database or bucket belongs to is fixed for the
// resource's life (decided 2026-09-07). The backend has dropped
// POST /api/v1/databases/:id/link, /unlink and POST /api/v1/storage/:id/link,
// /unlink; the client must not keep callable wrappers for routes that 404.

func TestClient_NoLinkOrUnlinkMethods(t *testing.T) {
	c := reflect.TypeOf(&Client{})
	for _, gone := range []string{"LinkDatabase", "UnlinkDatabase", "LinkBucket", "UnlinkBucket"} {
		if _, ok := c.MethodByName(gone); ok {
			t.Errorf("Client.%s still exists; its endpoint was removed from the API", gone)
		}
	}
}

// Source-level pin so a re-added call is caught even if it is spelled with a
// different method name.
func TestClient_NoLinkOrUnlinkEndpoints(t *testing.T) {
	src, err := os.ReadFile("client.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{`"/link"`, `"/unlink"`} {
		if strings.Contains(string(src), gone) {
			t.Errorf("client.go still builds a %s request path; the route no longer exists", gone)
		}
	}
}

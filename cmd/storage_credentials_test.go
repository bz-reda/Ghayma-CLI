package cmd

import (
	"strings"
	"testing"
)

// No shared credentials, part 4: `storage credentials` shows where a bucket is
// and what each connected site receives, never a key. The endpoint is the one
// the server reports for the bucket, never a host the CLI composes.

const uploadsBucket = `{"id":"b1","name":"uploads","garage_bucket":"uploads-b1c2d3e4","endpoint":"https://storage.staging.example","status":"active","external_access":false,"project_id":"p1"}`

const uploadsConnections = `{"connections":[
	{"site_id":"s1","site_slug":"main","kind":"bucket","resource_id":"b1","resource_name":"uploads","level":"read-write","env_names":["STORAGE_ENDPOINT","STORAGE_BUCKET","STORAGE_ACCESS_KEY","STORAGE_SECRET_KEY"]},
	{"site_id":"s2","site_slug":"admin","kind":"bucket","resource_id":"b1","resource_name":"uploads","level":"read","env_names":["STORAGE_UPLOADS_ENDPOINT"]},
	{"site_id":"s1","site_slug":"main","kind":"database","resource_id":"d1","resource_name":"shop","level":"connect","env_names":["DATABASE_URL"]}]}`

func TestStorageCredentials_ShowsConnectionDetailsWithoutAKey(t *testing.T) {
	ts, calls := detailsStub(t, "/api/v1/storage", `{"buckets":[`+uploadsBucket+`]}`, uploadsConnections)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "credentials", "uploads")

	want := "🔌 Connection details for bucket 'uploads'\n" +
		"\n" +
		"   Endpoint:  https://storage.staging.example\n" +
		"   Bucket:    uploads-b1c2d3e4\n" +
		"\n" +
		"   Connected sites and the variables they receive:\n" +
		"     main   read-write  STORAGE_ENDPOINT, STORAGE_BUCKET, STORAGE_ACCESS_KEY, STORAGE_SECRET_KEY\n" +
		"     admin  read        STORAGE_UPLOADS_ENDPOINT\n" +
		"\n" +
		"   No key is shown: each site has its own key, delivered in these variables.\n" +
		"   From your laptop:     ghayma env pull\n" +
		"   From outside Ghayma:  ghayma access add bucket uploads --name <principal>\n"
	if out != want {
		t.Fatalf("output:\n%s\nwant:\n%s", out, want)
	}
	for _, never := range []string{"Access Key", "Secret Key", "s3.ghayma.tech", "connect --local", "DATABASE_URL"} {
		if strings.Contains(out, never) {
			t.Errorf("output must not contain %q:\n%s", never, out)
		}
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
	if strings.Contains(strings.Join(*calls, " "), "/credentials") {
		t.Errorf("requests = %v; the credentials route must not be called", *calls)
	}
}

// A backend older than the endpoint field sends none: the row is left out
// rather than filled with a guessed host.
func TestStorageCredentials_NoEndpointFromAnOlderBackend(t *testing.T) {
	row := strings.Replace(uploadsBucket, `"endpoint":"https://storage.staging.example",`, "", 1)
	ts, _ := detailsStub(t, "/api/v1/storage", `{"buckets":[`+row+`]}`, uploadsConnections)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "credentials", "uploads")

	if strings.Contains(out, "Endpoint:") || strings.Contains(out, "ghayma.tech") {
		t.Errorf("output = %s; want no endpoint row", out)
	}
	if !strings.Contains(out, "\n   Bucket:    uploads-b1c2d3e4\n") {
		t.Errorf("output = %s; want the bucket row", out)
	}
}

func TestStorageCredentials_NotConnectedToAnySite(t *testing.T) {
	ts, _ := detailsStub(t, "/api/v1/storage", `{"buckets":[`+uploadsBucket+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "credentials", "uploads")

	if !strings.Contains(out, "\n   Not connected to any site. Connect one with: ghayma connect bucket uploads --site <slug>\n") {
		t.Errorf("output = %s; want the not-connected line", out)
	}
}

func TestStorageCredentials_BucketWithNoProject(t *testing.T) {
	row := strings.Replace(uploadsBucket, `,"project_id":"p1"`, "", 1)
	ts, calls := detailsStub(t, "/api/v1/storage", `{"buckets":[`+row+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "storage", "credentials", "uploads")

	if out != "❌ Bucket 'uploads' belongs to no project.\n" || lastExitCode != 1 {
		t.Fatalf("exit=%d output = %q", lastExitCode, out)
	}
	if len(*calls) != 1 {
		t.Errorf("requests = %v; want only the bucket list", *calls)
	}
}

package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// errFake stands in for docker's non-zero exit.
var errFake = errors.New("exit status 1")

// The push credential is the one secret this command handles. Every test here
// exists because of one of two failure shapes: the token leaking somewhere it
// can be read (argv, output, a dangling docker login), or the CLI pushing to
// the wrong repository.

// stubDocker substitutes the exec seam and records every call. The reply
// function answers one call at a time, keyed on its first argument.
func stubDocker(t *testing.T, reply func(args []string) (string, error)) *[]dockerCall {
	t.Helper()
	var calls []dockerCall
	origRun, origAvail := runDocker, dockerAvailable
	t.Cleanup(func() { runDocker, dockerAvailable = origRun, origAvail })

	dockerAvailable = func() bool { return true }
	runDocker = func(call dockerCall) (string, error) {
		calls = append(calls, call)
		if reply == nil {
			return "", nil
		}
		return reply(call.Args)
	}
	return &calls
}

// pushOutput is what docker prints at the end of a successful push.
const pushOutput = `The push refers to repository [registry.ghayma.tech/shop]
5f70bf18a086: Layer already exists
v1: digest: sha256:1111111111111111111111111111111111111111111111111111111111111111 size: 1152`

// dockerStub serves the mint, the site list and (for --deploy) the image
// deploy, recording "METHOD /path" for each and the mint body verbatim.
func dockerStub(t *testing.T, sites string) (*httptest.Server, *[]string, *map[string]any) {
	t.Helper()
	var seen []string
	mintBody := map[string]any{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/registry/token":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &mintBody)
			io.WriteString(w, `{"username":"dev@example.com","token":"ghr_secret","repo":"shop","server":"registry.ghayma.tech","expires_at":"2026-09-17T12:30:00Z"}`)
		case strings.HasSuffix(r.URL.Path, "/sites"):
			io.WriteString(w, sites)
		case strings.HasSuffix(r.URL.Path, "/deployments/image"):
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"id":"dep-1","site_id":"s1","status":"queued","trigger":"image","source_image_ref":"v1"}`)
		case strings.HasPrefix(r.URL.Path, "/api/v1/deployments/"):
			io.WriteString(w, `{"id":"dep-1","status":"live","domains":["shop.ghayma.app"]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, &seen, &mintBody
}

const oneSite = `[{"id":"s1","name":"main","slug":"main"}]`

// linkedDir is a directory linked to project p1's main site.
func linkedDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop","site_id":"s1","site_name":"main","site_slug":"main"}`})
	return dir
}

// fastPolls shortens the deploy wait so a command-level test does not sit out
// a real three-second poll.
func fastPolls(t *testing.T) {
	t.Helper()
	orig := deployWaitInterval
	t.Cleanup(func() { deployWaitInterval = orig })
	deployWaitInterval = time.Millisecond
}

func TestSplitImageRef(t *testing.T) {
	cases := []struct{ ref, name, tag string }{
		{"my-app", "my-app", ""},
		{"my-app:v1", "my-app", "v1"},
		{"ghcr.io/acme/my-app:v1", "ghcr.io/acme/my-app", "v1"},
		// A registry port is a colon that does NOT introduce a tag.
		{"localhost:5000/my-app", "localhost:5000/my-app", ""},
		{"localhost:5000/my-app:dev", "localhost:5000/my-app", "dev"},
		// A digest reference carries no tag at all.
		{"my-app@sha256:abc", "my-app@sha256:abc", ""},
	}
	for _, tc := range cases {
		name, tag := splitImageRef(tc.ref)
		if name != tc.name || tag != tc.tag {
			t.Errorf("splitImageRef(%q) = (%q, %q); want (%q, %q)", tc.ref, name, tag, tc.name, tc.tag)
		}
	}
}

// TestRemoteTag: --tag wins, then the local tag, then docker's own default.
func TestRemoteTag(t *testing.T) {
	cases := []struct {
		local, flag, want string
		wantErr           bool
	}{
		{local: "my-app:dev", flag: "v1", want: "v1"},
		{local: "my-app:dev", want: "dev"},
		{local: "my-app", want: "latest"},
		{local: "localhost:5000/my-app", want: "latest"},
		{local: "my-app@sha256:abc", want: "latest"},
		{local: "my-app", flag: "v1.2_3-rc", want: "v1.2_3-rc"},
		{local: "my-app", flag: "not/a/tag", wantErr: true},
		{local: "my-app", flag: ".leading-dot", wantErr: true},
		{local: "my-app", flag: strings.Repeat("x", 129), wantErr: true},
	}
	for _, tc := range cases {
		got, err := remoteTag(tc.local, tc.flag)
		if tc.wantErr {
			if err == nil {
				t.Errorf("remoteTag(%q, %q) = %q; want an error", tc.local, tc.flag, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("remoteTag(%q, %q) = (%q, %v); want %q", tc.local, tc.flag, got, err, tc.want)
		}
	}
}

func TestRemoteRefAndPushedDigest(t *testing.T) {
	if got := remoteRef("registry.ghayma.tech", "shop-admin", "v1"); got != "registry.ghayma.tech/shop-admin:v1" {
		t.Errorf("remoteRef = %q", got)
	}
	want := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	if got := pushedDigest(pushOutput); got != want {
		t.Errorf("pushedDigest = %q; want %q", got, want)
	}
	if got := pushedDigest("no digest here, and sha256:short is not one"); got != "" {
		t.Errorf("pushedDigest(no digest) = %q; want empty", got)
	}
}

// TestDockerLoginArgs_SecretIsNeverInArgv is the one that matters most: argv is
// readable by every process on the machine (`ps`), so the login must take the
// token on stdin. RED-verified by putting the token in argv — the assertions
// below fail immediately.
func TestDockerLoginArgs_SecretIsNeverInArgv(t *testing.T) {
	args := dockerLoginArgs("registry.ghayma.tech", "dev@example.com")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--password-stdin") {
		t.Errorf("login args = %v; the token must be read from stdin", args)
	}
	for _, banned := range []string{"--password", "-p"} {
		for _, arg := range args {
			if arg == banned {
				t.Errorf("login args = %v; %q puts the secret in argv", args, banned)
			}
		}
	}
}

// TestPushImage_LoginTakesTheTokenOnStdinOnly drives the real push through the
// exec seam and checks the token is in the process's STDIN and nowhere in any
// argv — the same property, asserted on what would actually be executed.
// RED-verified: swapping the login for `--password <token>` fails this.
func TestPushImage_LoginTakesTheTokenOnStdinOnly(t *testing.T) {
	calls := stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, _, _ := dockerStub(t, oneSite)
	client, _ := rotateTarget(ts.URL)

	out := captureStdout(t, func() {
		if _, err := pushImage(client, &pushTarget{ProjectID: "p1", SiteRef: "s1"}, "my-app:dev", "v1"); err != nil {
			t.Fatalf("pushImage: %v", err)
		}
	})

	var login *dockerCall
	for i := range *calls {
		if (*calls)[i].Args[0] == "login" {
			login = &(*calls)[i]
		}
	}
	if login == nil {
		t.Fatalf("calls = %v; want a login", *calls)
	}
	if login.Stdin != "ghr_secret" {
		t.Errorf("login stdin = %q; the token must be fed on stdin", login.Stdin)
	}
	for _, call := range *calls {
		for _, arg := range call.Args {
			if strings.Contains(arg, "ghr_secret") {
				t.Fatalf("docker %v carries the token in argv — every process on the machine can read it", call.Args)
			}
		}
	}
	if strings.Contains(out, "ghr_secret") {
		t.Errorf("output = %q; the token must never be printed", out)
	}
}

// TestPushImage_RunsTagLoginPushLogout pins the whole sequence, the remote ref
// it builds, and that the login is always dropped again — a credential left in
// the user's docker config outlives the push it was minted for.
func TestPushImage_RunsTagLoginPushLogout(t *testing.T) {
	calls := stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, seen, mint := dockerStub(t, oneSite)
	client, _ := rotateTarget(ts.URL)

	var image *pushedImage
	captureStdout(t, func() {
		var err error
		image, err = pushImage(client, &pushTarget{ProjectID: "p1", SiteRef: "s1"}, "my-app:dev", "v1")
		if err != nil {
			t.Fatalf("pushImage: %v", err)
		}
	})

	want := [][]string{
		{"tag", "my-app:dev", "registry.ghayma.tech/shop:v1"},
		{"login", "registry.ghayma.tech", "--username", "dev@example.com", "--password-stdin"},
		{"push", "registry.ghayma.tech/shop:v1"},
		{"logout", "registry.ghayma.tech"},
	}
	if len(*calls) != len(want) {
		t.Fatalf("calls = %v; want %v", *calls, want)
	}
	for i, w := range want {
		if strings.Join((*calls)[i].Args, " ") != strings.Join(w, " ") {
			t.Errorf("call %d = %v; want %v", i, (*calls)[i].Args, w)
		}
	}
	if !(*calls)[2].Stream {
		t.Error("the push must stream docker's output — layer progress only exists there")
	}
	if image.Ref != "registry.ghayma.tech/shop:v1" || image.Tag != "v1" {
		t.Errorf("pushed = %+v; want the registry ref and the tag", image)
	}
	if !strings.HasPrefix(image.Digest, "sha256:") {
		t.Errorf("digest = %q; want the one docker reported", image.Digest)
	}
	if len(*seen) != 1 || (*seen)[0] != "POST /api/v1/registry/token" {
		t.Errorf("requests = %v; want exactly one mint", *seen)
	}
	if (*mint)["project"] != "p1" || (*mint)["site"] != "s1" {
		t.Errorf("mint body = %v; want the project and the resolved site", *mint)
	}
}

// TestPushImage_LogoutRunsAfterAFailedPush: the token dies in half an hour, but
// a failed push must not leave a live credential sitting in docker's config.
func TestPushImage_LogoutRunsAfterAFailedPush(t *testing.T) {
	calls := stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return "denied: requested access to the resource is denied", errFake
		}
		return "", nil
	})
	ts, _, _ := dockerStub(t, oneSite)
	client, _ := rotateTarget(ts.URL)

	var err error
	captureStdout(t, func() {
		_, err = pushImage(client, &pushTarget{ProjectID: "p1", SiteRef: "s1"}, "my-app", "latest")
	})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("err = %v; want docker's own refusal", err)
	}
	if last := (*calls)[len(*calls)-1]; last.Args[0] != "logout" {
		t.Errorf("last call = %v; want the logout", last.Args)
	}
}

// TestDockerPush_NoDockerBinaryRefusesBeforeMinting: a machine that cannot push
// must not be issued a push credential — and must be told what to install.
func TestDockerPush_NoDockerBinaryRefusesBeforeMinting(t *testing.T) {
	stubDocker(t, nil)
	dockerAvailable = func() bool { return false }
	ts, seen, _ := dockerStub(t, oneSite)
	cliHome(t, ts.URL)

	out := runCLI(t, linkedDir(t), "docker", "push", "my-app")

	if !strings.Contains(out, "no 'docker' was found on PATH") {
		t.Errorf("output = %q; want the missing-docker message", out)
	}
	if len(*seen) != 0 {
		t.Errorf("requests = %v; want none when the push cannot run", *seen)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDockerPush_PushesTheLinkedSiteAndPrintsTheDeployHint drives the real
// command tree end to end.
func TestDockerPush_PushesTheLinkedSiteAndPrintsTheDeployHint(t *testing.T) {
	calls := stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, _, mint := dockerStub(t, oneSite)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "docker", "push", "my-app:dev", "--tag", "v1")

	if !strings.Contains(out, "✅ Pushed registry.ghayma.tech/shop:v1") {
		t.Errorf("output = %q; want the pushed reference", out)
	}
	if !strings.Contains(out, "Deploy it with: ghayma deploy --image v1") {
		t.Errorf("output = %q; want the follow-up hint", out)
	}
	if !strings.Contains(out, "digest sha256:1111") {
		t.Errorf("output = %q; want the digest docker reported", out)
	}
	if (*mint)["site"] != "s1" {
		t.Errorf("mint body = %v; want the linked site", *mint)
	}
	if len(*calls) != 4 {
		t.Errorf("docker calls = %v; want tag, login, push, logout", *calls)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
}

// TestDockerPush_SiteLessProjectPushesToTheMainRepository: pushing before the
// first deploy is the whole point of the feature, so a project with no site
// must push — with NO site in the mint request, which is how the backend
// authorizes the project's own main repository — and must be told plainly that
// there is nothing to deploy it to yet.
func TestDockerPush_SiteLessProjectPushesToTheMainRepository(t *testing.T) {
	stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, _, mint := dockerStub(t, `[]`)
	cliHome(t, ts.URL)
	noPrompt(t)

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop"}`})
	out := runCLI(t, dir, "docker", "push", "my-app", "--tag", "v1")

	if !strings.Contains(out, "✅ Pushed registry.ghayma.tech/shop:v1") {
		t.Fatalf("output = %q; a site-less project must still push", out)
	}
	if site, present := (*mint)["site"]; present {
		t.Errorf("mint body = %v; a site-less push must not name a site (got %v)", *mint, site)
	}
	if !strings.Contains(out, "ghayma site create main") {
		t.Errorf("output = %q; want the way to get a site", out)
	}
	if !strings.Contains(out, "ghayma deploy --image v1") {
		t.Errorf("output = %q; want the deploy command for later", out)
	}
}

// TestDockerPush_DeployIsRefusedBeforeTheUploadOnASiteLessProject: pushing
// gigabytes and then saying "there is no site" is the wrong order.
func TestDockerPush_DeployIsRefusedBeforeTheUploadOnASiteLessProject(t *testing.T) {
	calls := stubDocker(t, nil)
	ts, _, _ := dockerStub(t, `[]`)
	cliHome(t, ts.URL)
	noPrompt(t)

	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{projectConfigName: `{"project_id":"p1","name":"shop","slug":"shop"}`})
	out := runCLI(t, dir, "docker", "push", "my-app", "--deploy")

	if !strings.Contains(out, "deploying an image needs one") {
		t.Errorf("output = %q; want the site-less refusal", out)
	}
	if len(*calls) != 0 {
		t.Errorf("docker calls = %v; want none before the refusal", *calls)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDockerPush_ProdWithoutDeployIsRefused: --prod describes a deploy, and a
// bare push is not one. Silently ignoring it would let someone believe they
// had shipped to production.
func TestDockerPush_ProdWithoutDeployIsRefused(t *testing.T) {
	calls := stubDocker(t, nil)
	ts, seen, _ := dockerStub(t, oneSite)
	cliHome(t, ts.URL)
	noPrompt(t)

	out := runCLI(t, linkedDir(t), "docker", "push", "my-app", "--tag", "v1", "--prod")

	if !strings.Contains(out, "--prod applies to a deploy") {
		t.Errorf("output = %q; want the refusal", out)
	}
	if !strings.Contains(out, "--deploy --prod") || !strings.Contains(out, "ghayma deploy --image v1 --prod") {
		t.Errorf("output = %q; want both ways to actually deploy to production", out)
	}
	if len(*calls) != 0 || len(*seen) != 0 {
		t.Errorf("docker calls = %v, requests = %v; want none before the refusal", *calls, *seen)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDockerPush_DeployFlagDeploysWhatItPushed covers the shorthand: one push,
// then the ordinary image deploy against the same tag.
func TestDockerPush_DeployFlagDeploysWhatItPushed(t *testing.T) {
	stubDocker(t, func(args []string) (string, error) {
		if args[0] == "push" {
			return pushOutput, nil
		}
		return "", nil
	})
	ts, seen, _ := dockerStub(t, oneSite)
	cliHome(t, ts.URL)
	noPrompt(t)
	fastPolls(t)

	out := runCLI(t, linkedDir(t), "docker", "push", "my-app", "--tag", "v1", "--deploy")

	if !containsPath(*seen, "POST /api/v1/projects/p1/sites/s1/deployments/image") {
		t.Errorf("requests = %v; want the image deploy", *seen)
	}
	if !strings.Contains(out, "✅ Deployed successfully!") {
		t.Errorf("output = %q; want the deploy to be waited on", out)
	}
}

func containsPath(seen []string, want string) bool {
	for _, s := range seen {
		if s == want {
			return true
		}
	}
	return false
}

// TestDockerPushWiring pins the command tree and its flags: `ghayma docker
// push` with --site/--tag, reachable from root.
func TestDockerPushWiring(t *testing.T) {
	group, _, err := rootCmd.Find([]string{"docker", "push"})
	if err != nil || group.Name() != "push" || group.Parent().Name() != "docker" {
		t.Fatalf("Find(docker push) = %v, %v; want the push subcommand of docker", group, err)
	}
	for _, flag := range []string{"site", "tag", "deploy", "prod"} {
		if group.Flags().Lookup(flag) == nil {
			t.Errorf("docker push should expose --%s", flag)
		}
	}
	if group.Args == nil {
		t.Error("docker push must require the local image argument — there is no safe default")
	}
	out := runCLI(t, t.TempDir(), "docker", "push")
	if !strings.Contains(out, "requires a local image") {
		t.Errorf("no argument = %q; want the missing-argument message", out)
	}
}

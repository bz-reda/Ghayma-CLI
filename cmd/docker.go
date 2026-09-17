package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"paas-cli/internal/api"
	"paas-cli/internal/config"

	"github.com/spf13/cobra"
)

// Docker image deploys (DOCKER-DEPLOY-DESIGN-2026-09-17 §§2-3, §8). A customer
// builds an image the way they always have, `ghayma docker push` sends it to
// their site's repository in the Ghayma registry, and `ghayma deploy --image`
// ships it.
//
// The docker CLI is the engine on purpose: it is what built the image, and its
// layer-existence checks are what make a repeat push upload only the layers
// that changed. The credential it logs in with is minted per push, scoped to
// one repository and expires in about half an hour — so it is fed on STDIN,
// never as an argument (argv is world-readable in `ps`), and the login is
// dropped again when the push ends.

var (
	dockerPushSite   string
	dockerPushTag    string
	dockerPushDeploy bool
	dockerPushProd   bool
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "Push locally built images to Ghayma",
	Long: `Work with locally built Docker images.

Build your image with docker as usual, push it to your site's repository in
the Ghayma registry, then deploy it:

  docker build -t my-app .
  ghayma docker push my-app --tag v1
  ghayma deploy --image v1`,
}

var dockerPushCmd = &cobra.Command{
	Use:   "push <image[:tag]>",
	Short: "Push a locally built image to this site's Ghayma repository",
	Long: `Push a locally built image to the Ghayma registry.

The image is tagged for your site's repository, pushed with a short-lived
credential minted for this push alone, and the login is dropped again when it
finishes. Only the layers the registry does not already have are uploaded.

The remote tag is the local image's tag unless --tag says otherwise. The site
is the one this directory is linked to, or --site; a project with no site yet
pushes to its main repository, so a first push can precede a first deploy.

Requires the docker CLI on PATH.

Examples:
  ghayma docker push my-app                  # pushes my-app:latest as :latest
  ghayma docker push my-app:dev --tag v1     # pushes the local dev image as v1
  ghayma docker push my-app --site admin
  ghayma docker push my-app --tag v1 --deploy --prod`,
	Args: requireOneArg("local image", ""),
	Run:  runDockerPush,
}

// dockerCall is one invocation of the docker CLI.
type dockerCall struct {
	// Args is the argv after the binary name. A secret NEVER goes here: every
	// process on the machine can read another's arguments.
	Args []string
	// Stdin is what the process reads — where the registry token goes.
	Stdin string
	// Stream mirrors docker's own output to the terminal. Layer progress only
	// exists there, so a push without it looks hung.
	Stream bool
}

// runDocker is the exec seam: tests substitute it to assert what the CLI would
// run, and with which secret on which channel, without a docker daemon.
var runDocker = execDocker

// dockerAvailable reports whether a docker binary is on PATH, indirected for
// the same reason.
var dockerAvailable = func() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}

// execDocker runs one docker command and returns everything it printed. The
// output is captured even while streaming, because the digest the registry
// assigned is in it.
func execDocker(call dockerCall) (string, error) {
	cmd := exec.Command("docker", call.Args...)
	if call.Stdin != "" {
		cmd.Stdin = strings.NewReader(call.Stdin)
	}
	var buf bytes.Buffer
	if call.Stream {
		cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	} else {
		cmd.Stdout, cmd.Stderr = &buf, &buf
	}
	err := cmd.Run()
	return buf.String(), err
}

// dockerMissingMessage is what a machine with no docker is told. The push is
// docker's own upload; there is no fallback to offer yet (design §9).
const dockerMissingMessage = "'ghayma docker push' runs the docker CLI, and no 'docker' was found on PATH.\n   Install Docker (or start Docker Desktop) and try again."

// dockerLoginArgs builds the login argv. --password-stdin is the whole point:
// the token arrives on stdin, so it is never in this slice.
func dockerLoginArgs(server, username string) []string {
	return []string{"login", server, "--username", username, "--password-stdin"}
}

// splitImageRef splits a local image reference into its repository part and
// its tag. A registry host may carry a port ("localhost:5000/app"), so a colon
// only names a tag when no slash follows it; a digest reference has no tag at
// all.
func splitImageRef(ref string) (name, tag string) {
	ref = strings.TrimSpace(ref)
	if at := strings.Index(ref, "@"); at >= 0 {
		return ref, ""
	}
	colon := strings.LastIndex(ref, ":")
	if colon < 0 || strings.Contains(ref[colon+1:], "/") {
		return ref, ""
	}
	return ref[:colon], ref[colon+1:]
}

// remoteTag decides the tag the image gets in the Ghayma registry: --tag wins,
// then the local image's own tag, then "latest" — docker's own default for a
// reference with no tag.
func remoteTag(localRef, flag string) (string, error) {
	tag := strings.TrimSpace(flag)
	if tag == "" {
		_, tag = splitImageRef(localRef)
	}
	if tag == "" {
		tag = "latest"
	}
	if err := validateTag(tag); err != nil {
		return "", err
	}
	return tag, nil
}

// validateTag applies docker's own tag grammar. The deploy endpoint refuses a
// tag carrying path characters anyway; refusing here means the user hears it
// before an upload rather than after one.
func validateTag(tag string) error {
	if len(tag) > 128 {
		return fmt.Errorf("tag %q is too long (128 characters max)", tag)
	}
	for i, r := range tag {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_':
		case (r == '.' || r == '-') && i > 0:
		default:
			return fmt.Errorf("tag %q is not a valid image tag — use letters, digits, '.', '_' and '-' (starting with a letter, digit or '_')", tag)
		}
	}
	return nil
}

// remoteRef is the full reference the image is pushed to.
func remoteRef(server, repo, tag string) string {
	return server + "/" + repo + ":" + tag
}

// pushedDigest pulls the digest out of docker's push output — the line reading
// "<tag>: digest: sha256:… size: …". Empty when docker said nothing of the
// sort: the digest is a nicety here, not something to fail a good push over.
func pushedDigest(out string) string {
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, api.DigestPrefix) && len(field) == len(api.DigestPrefix)+64 {
			return field
		}
	}
	return ""
}

// dockerFailure renders a failed docker command: docker's own last words, then
// ours. Its message is the one that says what went wrong (a missing image, a
// daemon that is not running, a refused push).
func dockerFailure(action, out string, err error) string {
	if tail := lastLine(out); tail != "" {
		return fmt.Sprintf("%s: %s", action, tail)
	}
	return fmt.Sprintf("%s: %v", action, err)
}

// lastLine returns the last non-empty line of docker's output.
func lastLine(out string) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// pushTarget is the project and, when the project has one, the live site a
// push goes to. An empty SiteRef is the site-less project: the front-door
// authorizes it for its own main repository (design §2), which is what lets a
// first push precede a first deploy.
type pushTarget struct {
	ProjectID   string
	ProjectName string
	SiteRef     string
}

// SiteLess reports the project-with-no-site case.
func (t *pushTarget) SiteLess() bool { return t.SiteRef == "" }

// resolvePushTarget resolves which project and site a push goes to, through
// the ladder every site-scoped command uses, and maps the linked site onto a
// LIVE one — a config may name a site that no longer exists, or name none at
// all. A project with no sites is not an error here, unless --site named one.
func resolvePushTarget(client *api.Client, siteFlag string) (*pushTarget, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	ctx, err := resolveSiteContext(cwd, siteFlag, "push an image for")
	switch {
	case errors.Is(err, errAttachCancelled):
		return nil, errors.New("Cancelled")
	case errors.Is(err, errNoProjectConfig):
		return nil, errors.New("no project config found — run 'ghayma init' first")
	case err != nil:
		return nil, err
	}

	target := &pushTarget{ProjectID: ctx.ProjectID, ProjectName: ctx.ProjectName}
	site, err := liveSiteOf(client, ctx, siteFlag)
	switch {
	case errors.Is(err, errNoSite) && siteFlag == "":
		return target, nil
	case err != nil:
		return nil, err
	}
	target.SiteRef = site.ID
	return target, nil
}

// pushedImage is what a completed push produced: the reference in the Ghayma
// registry, the tag to deploy, and the digest when docker reported one.
type pushedImage struct {
	Ref    string
	Tag    string
	Digest string
}

// pushImage is the acting half: mint, tag, login, push, logout. The login is
// always dropped again — the token dies in half an hour anyway, and leaving a
// dead credential in the user's docker config is a confusing thing to inherit.
func pushImage(client *api.Client, target *pushTarget, localImage, tag string) (*pushedImage, error) {
	cred, err := client.MintRegistryToken(target.ProjectID, target.SiteRef)
	if err != nil {
		return nil, fmt.Errorf("could not get a push credential: %v", err)
	}
	ref := remoteRef(cred.Server, cred.Repo, tag)

	fmt.Printf("🐳 Pushing %s → %s\n", localImage, ref)
	if out, err := runDocker(dockerCall{Args: []string{"tag", localImage, ref}}); err != nil {
		return nil, errors.New(dockerFailure("could not tag "+localImage, out, err))
	}

	if out, err := runDocker(dockerCall{Args: dockerLoginArgs(cred.Server, cred.Username), Stdin: cred.Token}); err != nil {
		return nil, errors.New(dockerFailure("could not sign in to "+cred.Server, out, err))
	}
	// The logout runs whatever happens next: a failed push must not leave a
	// live push credential behind either.
	defer runDocker(dockerCall{Args: []string{"logout", cred.Server}})

	fmt.Println("⬆️  Uploading (only the layers the registry doesn't have)...")
	out, err := runDocker(dockerCall{Args: []string{"push", ref}, Stream: true})
	if err != nil {
		return nil, errors.New(dockerFailure("push failed", out, err))
	}
	return &pushedImage{Ref: ref, Tag: tag, Digest: pushedDigest(out)}, nil
}

// printPushResult is everything the user reads after a successful push: what
// landed where, and the one command that deploys it.
func printPushResult(target *pushTarget, image *pushedImage) {
	fmt.Printf("\n✅ Pushed %s\n", image.Ref)
	if image.Digest != "" {
		fmt.Printf("   digest %s\n", image.Digest)
	}
	if target.SiteLess() {
		fmt.Println("\nℹ️  This project has no site yet, so there is nothing to deploy the image to.")
		fmt.Printf("   Create one with: ghayma site create main\n   Then deploy it with: ghayma deploy --image %s\n", image.Tag)
		return
	}
	fmt.Printf("\n   Deploy it with: ghayma deploy --image %s\n", image.Tag)
}

func runDockerPush(cmd *cobra.Command, args []string) {
	cfg := config.Load()
	if !cfg.LoggedIn() {
		failf("Please login first: ghayma login")
		return
	}
	localImage := strings.TrimSpace(args[0])
	if localImage == "" {
		failf("Name the image to push, e.g. 'ghayma docker push my-app:latest'")
		return
	}
	tag, err := remoteTag(localImage, dockerPushTag)
	if err != nil {
		failf("%v", err)
		return
	}
	// --prod is a property of a deploy, and a push is not one. Refused rather
	// than ignored: a user who types it believes something happened.
	if dockerPushProd && !dockerPushDeploy {
		failf("--prod applies to a deploy, and this command only pushes.\n   Push and deploy with: ghayma docker push %s --deploy --prod\n   Or deploy separately with: ghayma deploy --image %s --prod", localImage, tag)
		return
	}
	// Checked before anything is minted: a machine with no docker cannot push,
	// and a credential nobody can use is not worth issuing.
	if !dockerAvailable() {
		failf("%s", dockerMissingMessage)
		return
	}

	client := api.NewClient(cfg)
	target, err := resolvePushTarget(client, dockerPushSite)
	if err != nil {
		reportSiteError(err)
		exitFn(1)
		return
	}
	// --deploy is refused BEFORE the upload rather than after it: pushing
	// gigabytes and then saying "there is no site" is the wrong order.
	if dockerPushDeploy && target.SiteLess() {
		fmt.Println(noSiteForImageMessage)
		exitFn(1)
		return
	}

	image, err := pushImage(client, target, localImage, tag)
	if err != nil {
		failf("%v", err)
		return
	}
	if !dockerPushDeploy {
		printPushResult(target, image)
		return
	}
	fmt.Printf("\n✅ Pushed %s\n", image.Ref)
	deployPushedImage(client, target.ProjectID, target.SiteRef, image.Tag, dockerPushProd)
}

func init() {
	dockerPushCmd.Flags().StringVar(&dockerPushSite, "site", "", "Site whose repository to push to (name or slug)")
	dockerPushCmd.Flags().StringVar(&dockerPushTag, "tag", "", "Tag to push as (default: the local image's tag, else latest)")
	dockerPushCmd.Flags().BoolVar(&dockerPushDeploy, "deploy", false, "Deploy the image once it is pushed")
	dockerPushCmd.Flags().BoolVar(&dockerPushProd, "prod", false, "With --deploy, deploy to production")
	dockerCmd.AddCommand(dockerPushCmd)
	rootCmd.AddCommand(dockerCmd)
}

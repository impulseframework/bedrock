package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
	"github.com/impulseframework/bedrock/internal/org"
	rootorg "github.com/impulseframework/bedrock/internal/root"
)

// noCloudWorld is GitHub before the bootstrap of a root organization without a cloud: the
// organization imp-example with GitHub Actions' app, and the two apps the organization
// brings, made and installed, their keys in files. Nothing of Google's is wired: the
// clients carry GitHub alone. It answers the deps, the stand-in, the flags that bring the
// apps and the infrastructure app's key.
func noCloudWorld(t *testing.T) (d deps, srv *githubtest.Server, flags []string, infra []byte) {
	t.Helper()

	srv = githubtest.New(t)
	srv.PublicApps["github-actions"] = github.App{ID: 15368, Slug: "github-actions", Name: "GitHub Actions"}
	d = orgDeps("")
	d.org.root = &rootorg.Clients{
		GitHub: func(token string) *github.Client { return github.New(srv.URL, token) },
		Sleep:  noWait,
	}
	d.githubToken = func(context.Context) (string, error) { return "", nil }
	shape := &org.Placement{Kind: org.RootKind, GithubOrganization: "imp-example", Libraries: []org.Library{{Repository: "workflows"}}}
	keys := t.TempDir()
	for i, m := range shape.AppManifests() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		id := int64(5000 + i)
		owner := github.Account{Login: "imp-example", Type: "Organization"}
		record := github.App{ID: id, Slug: m.Name, Name: m.Name, ClientID: fmt.Sprintf("Iv23client%d", id), Owner: owner, Permissions: m.Permissions}
		srv.PublicApps[m.Name] = record
		record.PEM = string(keyPEM)
		srv.Apps[fmt.Sprint(id)] = &githubtest.App{
			Key: &key.PublicKey, Record: record,
			Installations: []github.Installation{{ID: id + 1, AppID: id, Account: owner, RepositorySelection: github.AllRepositories}},
		}
		srv.OrgInstallations["imp-example"] = append(srv.OrgInstallations["imp-example"], github.OrgInstallation{ID: id + 1, AppID: id, AppSlug: m.Name, RepositorySelection: github.AllRepositories})
		file := filepath.Join(keys, string(m.Role)+".pem")
		if err := os.WriteFile(file, keyPEM, 0o600); err != nil {
			t.Fatal(err)
		}
		flags = append(flags, "--"+string(m.Role)+"-app", fmt.Sprint(id), "--"+string(m.Role)+"-key", file)
		if m.Role == org.InfrastructureApp {
			infra = keyPEM
		}
	}

	return d, srv, flags, infra
}

// TestOrgBootstrapNoCloud: with --no-cloud the bootstrap drafts a root organization from
// its flags alone, reads no Google credentials and asks for no token, makes the
// organization on GitHub with the apps brought, and says what follows. The placement in
// the directory decides the shape from then on: a run without --no-cloud is refused, and
// so are the flags that name something of Google's.
func TestOrgBootstrapNoCloud(t *testing.T) {
	// No t.Parallel(): the later runs read the process environment.
	t.Setenv(github.TokenEnv, "")
	d, srv, brought, _ := noCloudWorld(t)
	dir := filepath.Join(t.TempDir(), "imf-root-infrastructure")
	args := append([]string{"org", "bootstrap", dir, "--no-cloud", "--prefix", "imf", "--github-organization", "imp-example", "--public-library", "workflows", "--library", "notes", "--no-browser"}, brought...)
	out, err := execute(d, "", args...)
	if err != nil {
		t.Fatalf("org bootstrap --no-cloud = %v; printed:\n%s", err, out)
	}
	for _, want := range []string{
		"Drafted the imf root organization, without a cloud: GitHub organization imp-example, 2 libraries, bedrock " + headVersion + ".",
		"the infrastructure app: App 5000 (imp-example-infrastructure), brought with its key, holds what bedrock declares and is installed on imp-example.",
		"Created the repository imp-example/imf-root-infrastructure, private.",
		"GitHub: turns immutable releases on for every repository of the imp-example organization (its policy names no repository today)",
		"libraries: creates the public repository imp-example/workflows, with an initial commit on master",
		"Bootstrapped the imf root organization on GitHub alone",
		"Next, each where it happens:",
		"the\n     workflow's plan posts what the merge changes, and its apply makes it on the merge",
		"The keys of the GitHub Apps are GitHub secrets now and nowhere else",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("printed:\n%s\nwant it to contain %q", out, want)
		}
	}
	if strings.Contains(out, "Bot Account") || strings.Contains(out, "bedrock upgrade, then the pull") {
		t.Errorf("printed:\n%s\nwant no Bot Account, and no wait for a pin this bedrock recorded", out)
	}
	p, err := org.ReadPlacement(filepath.Join(dir, "placement.json"))
	if err != nil {
		t.Fatalf("the placement the bootstrap wrote: %v", err)
	}
	if p.HasCloud() || p.GithubInfrastructureAppID != "5000" || p.GithubReleaseAppID != "5001" || p.BedrockVersion != headVersion || len(p.Libraries) != 2 || !p.Libraries[1].Public {
		t.Errorf("the placement: %+v", p)
	}
	if repo := srv.Repos["imp-example/imf-root-infrastructure"]; repo == nil || repo.Environments[org.RootEnvironment] == nil {
		t.Errorf("the repository and its environment were not made: %+v", repo)
	}
	if got := srv.ImmutableReleases["imp-example"]; got != github.ImmutableReleasesAll {
		t.Errorf("after the bootstrap the organization's policy on immutable releases is %q, want %q", got, github.ImmutableReleasesAll)
	}

	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "the same directory without --no-cloud", args: []string{dir, "--no-browser"}, wantErr: "names no Google Cloud organization (organizationId): a root organization without a cloud, which holds libraries alone and is kept on GitHub, is made with --no-cloud"},
		{name: "a domain", args: []string{filepath.Join(t.TempDir(), "x"), "--no-cloud", "--prefix", "imf", "--github-organization", "imp-example", "--domain", "imp.example"}, wantErr: "--domain names something of Google's, and --no-cloud makes a root organization that has none"},
		{name: "a library without --no-cloud", args: []string{filepath.Join(t.TempDir(), "x"), "--prefix", "imf", "--github-organization", "imp-example", "--library", "notes"}, wantErr: "--library and --public-library go with --no-cloud"},
		{name: "a naming flag over the directory's placement", args: []string{dir, "--no-cloud", "--library", "more"}, wantErr: "already records the imf root organization, and names everything: run again without --library"},
		{name: "no key of the recorded app", args: []string{dir, "--no-cloud", "--no-browser"}, wantErr: "--infrastructure-app 5000 --infrastructure-key <the downloaded file>"},
	}
	// One after another, not as parallel subtests: the runs share the directory.
	for _, tt := range tests {
		_, err := execute(d, "", append([]string{"org", "bootstrap"}, tt.args...)...)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: org bootstrap %v = %v, want %q", tt.name, tt.args, err, tt.wantErr)
		}
	}
}

// TestOrgRunsInWorkflow: in the repository of a root organization without a cloud, org
// plan and org apply are the workflow's jobs. Handed the infrastructure app's key, the
// commit and the pull request in the environment, the plan says what a changed placement
// would change and posts it on the pull request as the infrastructure app, changing
// nothing; the apply makes it. Without the key each says where it runs. The check
// compares the organization with GitHub as the app when it is handed the key, and with
// --issue keeps one issue: opened by a finding, given a comment while it lasts, closed
// when the organization stands as the placement says.
func TestOrgRunsInWorkflow(t *testing.T) {
	// No t.Parallel(): the runs read the process environment.
	t.Setenv(github.TokenEnv, "")
	d, srv, brought, key := noCloudWorld(t)
	dir := filepath.Join(t.TempDir(), "imf-root-infrastructure")
	args := append([]string{"org", "bootstrap", dir, "--no-cloud", "--prefix", "imf", "--github-organization", "imp-example", "--public-library", "workflows", "--no-browser"}, brought...)
	if out, err := execute(d, "", args...); err != nil {
		t.Fatalf("org bootstrap --no-cloud = %v; printed:\n%s", err, out)
	}

	// A render names no trigger, project or connection here: the organization has none.
	rendered, err := execute(d, "", "org", "render", "--dir", dir)
	if err != nil || !strings.Contains(rendered, "Rendered the imf foundation into") || strings.Contains(rendered, "trigger") || strings.Contains(rendered, "Cloud Build") {
		t.Errorf("org render = %v; printed:\n%s", err, rendered)
	}
	_, err = execute(d, "", "org", "plan", "--dir", dir)
	if want := "BEDROCK_INFRASTRUCTURE_APP_KEY is not set: bedrock org plan in the imf root organization runs from its repository's workflow (.github/workflows/bedrock.yml)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("org plan without the key = %v, want %q", err, want)
	}
	out, err := execute(d, "", "org", "check", "--dir", dir)
	if err != nil || !strings.Contains(out, "The root organization was not compared with GitHub: no token") || !strings.Contains(out, "owned file(s) match the placement") {
		t.Errorf("org check without a key or a token = %v; printed:\n%s", err, out)
	}

	t.Setenv(org.InfrastructureKeySecret, string(key))
	t.Setenv(rootorg.CommitEnv, "0123456789abcdef0123456789abcdef01234567")
	t.Setenv(rootorg.PullRequestEnv, "7")
	out, err = execute(d, "", "org", "check", "--dir", dir)
	if err != nil || !strings.Contains(out, "The root organization stands as placement.json says: nothing to change.") {
		t.Errorf("org check with the key = %v; printed:\n%s", err, out)
	}

	// A pull request that lists a second library.
	p, err := org.ReadPlacement(filepath.Join(dir, "placement.json"))
	if err != nil {
		t.Fatal(err)
	}
	p.Libraries = append(p.Libraries, org.Library{Repository: "notes"})
	if err := p.Write(filepath.Join(dir, "placement.json")); err != nil {
		t.Fatal(err)
	}
	out, err = execute(d, "", "org", "plan", "--dir", dir)
	if err != nil {
		t.Fatalf("org plan = %v; printed:\n%s", err, out)
	}
	for _, want := range []string{
		"libraries: the apply creates the private repository imp-example/notes, with an initial commit on master",
		"Posted the report on pull request #7 as the infrastructure app.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("org plan printed:\n%s\nwant it to contain %q", out, want)
		}
	}
	root := srv.Repos["imp-example/imf-root-infrastructure"]
	if comments := root.Comments[7]; len(comments) != 1 || !strings.Contains(comments[0].Body, "### what the merge changes in the imf root organization") || !strings.Contains(comments[0].Body, "At 0123456:") || !strings.Contains(comments[0].Body, "creates the private repository imp-example/notes") {
		t.Errorf("the comment on the pull request: %+v", comments)
	}
	if srv.Repos["imp-example/notes"] != nil {
		t.Error("the plan made the library")
	}
	out, err = execute(d, "", "org", "apply", "--dir", dir)
	if err != nil || !strings.Contains(out, "libraries: creates the private repository imp-example/notes") || !strings.Contains(out, "change(s); 0 finding(s).") {
		t.Fatalf("org apply = %v; printed:\n%s", err, out)
	}
	if notes := srv.Repos["imp-example/notes"]; notes == nil || !notes.Private {
		t.Errorf("the apply did not make the library private: %+v", notes)
	}

	// The weekly check: the release app's key gone from its secret is a finding.
	out, err = execute(d, "", "org", "check", "--dir", dir, "--issue")
	if err != nil || strings.Contains(out, "issue #") {
		t.Errorf("org check --issue on an organization that stands = %v; printed:\n%s", err, out)
	}
	sealed := srv.OrgSecrets["imp-example"][org.ReleaseKeyOrgSecret]
	srv.Update(func() { delete(srv.OrgSecrets["imp-example"], org.ReleaseKeyOrgSecret) })
	out, err = execute(d, "", "org", "check", "--dir", dir, "--issue")
	if err != nil || !strings.Contains(out, "Opened issue #") {
		t.Fatalf("org check --issue with a finding = %v; printed:\n%s", err, out)
	}
	if len(root.Issues) != 1 || root.Issues[0].State != "open" || !strings.Contains(root.IssueBodies[root.Issues[0].Number], "the organization secret RELEASE_PLEASE_APP_PEM does not exist") {
		t.Fatalf("the issue: %+v, bodies %v", root.Issues, root.IssueBodies)
	}
	number := root.Issues[0].Number
	out, err = execute(d, "", "org", "check", "--dir", dir, "--issue")
	if err != nil || !strings.Contains(out, fmt.Sprintf("Added what the check found to issue #%d.", number)) || len(root.Issues) != 1 {
		t.Errorf("a second check with the finding = %v; printed:\n%s", err, out)
	}
	srv.Update(func() { srv.OrgSecrets["imp-example"][org.ReleaseKeyOrgSecret] = sealed })
	out, err = execute(d, "", "org", "check", "--dir", dir, "--issue")
	if err != nil || !strings.Contains(out, fmt.Sprintf("Closed issue #%d", number)) || root.Issues[0].State != "closed" {
		t.Errorf("the check once the finding is gone = %v; printed:\n%s\nissue: %+v", err, out, root.Issues[0])
	}
}

// TestOrgCheckIssueElsewhere: --issue is the weekly check of a root organization without
// a cloud, and is refused in any other organization's repository.
func TestOrgCheckIssueElsewhere(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{"imr", "imp"} {
		_, err := execute(orgDeps(""), "", "org", "check", "--dir", filepath.Join("..", "org", "testdata", fixture), "--issue")
		if err == nil || !strings.Contains(err.Error(), "--issue is the weekly check of a root organization without a cloud") {
			t.Errorf("org check --issue in %s = %v", fixture, err)
		}
	}
}

// newAppKey gives the stand-in's app a private key generated anew, as its settings page
// does, and answers the file that holds it and the key.
func newAppKey(t *testing.T, srv *githubtest.Server, id, name string) (file string, keyPEM []byte) {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	srv.Update(func() { srv.Apps[id].Key = &key.PublicKey })
	file = filepath.Join(t.TempDir(), name+".pem")
	if err := os.WriteFile(file, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}

	return file, keyPEM
}

// TestOrgKeysNoCloud: in a root organization without a cloud the apps' keys are GitHub
// secrets. secrets check says where each is; secret add proves a key generated anew signs
// as the app and seals it, the infrastructure app's with the token the key itself mints
// and the release app's as the infrastructure app; a key of another app, a secret the
// organization does not hold and a pin are refused. The destroy then takes the
// organization's keeping off GitHub and leaves the repositories.
func TestOrgKeysNoCloud(t *testing.T) {
	// No t.Parallel(): the runs read the process environment.
	t.Setenv(github.TokenEnv, "")
	d, srv, brought, _ := noCloudWorld(t)
	dir := filepath.Join(t.TempDir(), "imf-root-infrastructure")
	args := append([]string{"org", "bootstrap", dir, "--no-cloud", "--prefix", "imf", "--github-organization", "imp-example", "--public-library", "workflows", "--no-browser"}, brought...)
	if out, err := execute(d, "", args...); err != nil {
		t.Fatalf("org bootstrap --no-cloud = %v; printed:\n%s", err, out)
	}
	root := srv.Repos["imp-example/imf-root-infrastructure"]
	env := root.Environments[org.RootEnvironment]

	infraFile, infraKey := newAppKey(t, srv, "5000", "infrastructure")
	out, err := execute(d, "", "secret", "add", org.InfrastructureKey, "--dir", dir, "--from-file", infraFile)
	if err != nil || !strings.Contains(out, "Sealed the key of the infrastructure app (App 5000, imp-example-infrastructure) into the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of imp-example/imf-root-infrastructure") || !strings.Contains(out, "https://github.com/organizations/imp-example/settings/apps/imp-example-infrastructure") {
		t.Fatalf("secret add %s = %v; printed:\n%s", org.InfrastructureKey, err, out)
	}
	if !bytes.Equal(env.Secrets[org.InfrastructureKeySecret].Value, infraKey) {
		t.Error("the new key was not sealed into the environment's secret")
	}

	releaseFile, releaseKey := newAppKey(t, srv, "5001", "release")
	_, err = execute(d, "", "secret", "add", org.ReleaseKey, "--dir", dir, "--from-file", releaseFile)
	if want := "sealing the release app's key needs a token that writes the organization's secrets"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("secret add %s with no token = %v, want %q", org.ReleaseKey, err, want)
	}
	out, err = execute(d, "", "secret", "add", org.ReleaseKey, "--dir", dir, "--from-file", releaseFile, "--infrastructure-key", infraFile)
	if err != nil || !strings.Contains(out, "Sealed the key of the release app (App 5001, imp-example-release) into the organization secret RELEASE_PLEASE_APP_PEM, and its client ID into RELEASE_PLEASE_APP_CLIENT_ID, both read by the libraries alone (workflows)") {
		t.Fatalf("secret add %s as the infrastructure app = %v; printed:\n%s", org.ReleaseKey, err, out)
	}
	if sealed := srv.OrgSecrets["imp-example"][org.ReleaseKeyOrgSecret]; !bytes.Equal(sealed.Value, releaseKey) || sealed.Visibility != github.VisibilitySelected || len(sealed.RepositoryIDs) != 1 {
		t.Errorf("the organization secret after the rotation: visibility %q, readers %v, the new key %t", sealed.Visibility, sealed.RepositoryIDs, bytes.Equal(sealed.Value, releaseKey))
	}

	refusals := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "a key of another app", args: []string{"secret", "add", org.InfrastructureKey, "--dir", dir, "--from-file", releaseFile}, wantErr: "the key does not sign as the infrastructure app (App 5000), so it is not sealed"},
		{name: "a secret the organization does not hold", args: []string{"secret", "add", org.DeployerKey, "--dir", dir, "--from-file", releaseFile}, wantErr: `"github-deployer-key" is not a key the imf root organization holds`},
		{name: "a place in Secret Manager", args: []string{"secret", "add", org.InfrastructureKey, "--dir", dir, "--from-file", infraFile, "--project", "imf-root-gbl-core-a1b2"}, wantErr: "the keys of a root organization without a cloud are GitHub secrets"},
		{name: "a pin", args: []string{"secret", "pin", org.InfrastructureKey, "1", "--dir", dir}, wantErr: "GitHub secrets, which carry no version to pin"},
		{name: "a version to check", args: []string{"org", "secrets", "check", org.InfrastructureKey, "1", "--dir", dir}, wantErr: "secrets check takes no argument in the imf root organization"},
		{name: "no token to check with", args: []string{"org", "secrets", "check", "--dir", dir}, wantErr: "no GitHub token to read the keys' places with"},
	}
	// One after another, not as parallel subtests: the runs share the directory.
	for _, tt := range refusals {
		_, err := execute(d, "", tt.args...)
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: %v = %v, want %q", tt.name, tt.args, err, tt.wantErr)
		}
	}

	out, err = execute(d, "", "org", "secrets", "check", "--dir", dir, "--infrastructure-key", infraFile)
	if err != nil {
		t.Fatalf("org secrets check = %v; printed:\n%s", err, out)
	}
	for _, want := range []string{
		"the infrastructure app (App 5000): its key is the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of imp-example/imf-root-infrastructure, last sealed ",
		"the workflow's jobs on master alone read it.",
		"the release app (App 5001): its key is the organization secret RELEASE_PLEASE_APP_PEM of imp-example, last sealed ",
		"read by workflows.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("org secrets check printed:\n%s\nwant it to contain %q", out, want)
		}
	}

	_, err = execute(d, "", "org", "destroy", "--dir", dir)
	if err == nil || !strings.Contains(err.Error(), "asks for the prefix typed back at the terminal") {
		t.Errorf("org destroy with no terminal and no --yes = %v", err)
	}
	out, err = execute(d, "", "org", "destroy", "--dir", dir, "--yes", "--infrastructure-key", infraFile)
	if err != nil {
		t.Fatalf("org destroy = %v; printed:\n%s", err, out)
	}
	for _, want := range []string{
		"bedrock org destroy takes off GitHub what bedrock keeps for the imf root organization as a whole, in the imp-example GitHub organization:",
		"Deleted the organization secret RELEASE_PLEASE_APP_PEM.",
		"Deleted the organization secret RELEASE_PLEASE_APP_CLIENT_ID.",
		"Deleted the organization variable RELEASE_APP_LOGIN.",
		`Deleted the organization's ruleset "version tags"`,
		"Deleted the infrastructure app's key from the environment bedrock of imp-example/imf-root-infrastructure",
		`Took the workflow's plan (bedrock plan) out of the ruleset "default branch" of imp-example/imf-root-infrastructure: the workflow has no key to run it with`,
		"The repositories are as they were, and the GitHub Apps stay installed: on https://github.com/organizations/imp-example/settings/apps, delete each",
		"bedrock org bootstrap <dir> --no-cloud again",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("org destroy printed:\n%s\nwant it to contain %q", out, want)
		}
	}
	if len(srv.OrgSecrets["imp-example"]) != 0 || len(env.Secrets) != 0 || len(srv.OrgRulesets["imp-example"]) != 0 {
		t.Errorf("after the destroy: organization secrets %v, the environment's %v, organization rulesets %v; want none", srv.OrgSecrets["imp-example"], env.Secrets, srv.OrgRulesets["imp-example"])
	}
	if _, stands := srv.Variables["imp-example"][org.ReleaseLoginVariable]; stands {
		t.Error("the release app's login variable stands after the destroy")
	}
	if srv.Repos["imp-example/workflows"] == nil || srv.Repos["imp-example/imf-root-infrastructure"] == nil {
		t.Error("the destroy removed a repository")
	}
	if got := srv.ImmutableReleases["imp-example"]; got != github.ImmutableReleasesAll {
		t.Errorf("after the destroy the organization's policy on immutable releases is %q, want %q: bedrock never turns it off", got, github.ImmutableReleasesAll)
	}
	journal, err := os.ReadFile(filepath.Join(dir, "JOURNAL.md"))
	if err != nil || !strings.Contains(string(journal), "bedrock org destroy took the root organization's keeping off GitHub") {
		t.Errorf("the journal: %v\n%s", err, journal)
	}
	out, err = execute(d, "", "org", "destroy", "--dir", dir, "--yes", "--infrastructure-key", infraFile)
	if err != nil || !strings.Contains(out, "The organization secret RELEASE_PLEASE_APP_PEM is gone already.") || !strings.Contains(out, "holds no key already") || !strings.Contains(out, "requires no check already") {
		t.Errorf("org destroy again = %v; printed:\n%s", err, out)
	}
	out, err = execute(d, "", "org", "secrets", "check", "--dir", dir, "--infrastructure-key", infraFile)
	var exit exitError
	if !asExit(err, &exit) || exit.code != 1 || !strings.Contains(out, "the infrastructure app (App 5000): no key is sealed.") || !strings.Contains(out, "the release app (App 5001): no key is sealed.") {
		t.Errorf("org secrets check after the destroy = %v; printed:\n%s", err, out)
	}
}

package root_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// githubRootDraft is the placement of a root organization without a cloud as a person
// writes it before the bootstrap: the GitHub organization, the repository, and two
// libraries, one public with the checks its pull requests report and one private.
func githubRootDraft() *org.Placement {
	return &org.Placement{
		Kind: org.RootKind, Prefix: "imf", GithubOrganization: "imp-example", GithubDefaultBranch: "master", SourceRepo: "imf-root-infrastructure",
		Libraries: []org.Library{
			{Repository: "workflows", Public: true, Checks: []string{"actionlint", "semantic-titles / Validate PR title"}},
			{Repository: "notes"},
		},
	}
}

// githubRootWorld is GitHub before the bootstrap of a root organization without a cloud:
// the organization, empty, and GitHub Actions' own app, which reports every check. There
// is nothing of Google's, and the clients carry no seam to it: a call to one would be a
// nil function's.
func githubRootWorld(t *testing.T) (*githubtest.Server, *root.Clients) {
	t.Helper()

	srv := githubtest.New(t)
	srv.PublicApps["github-actions"] = github.App{ID: 15368, Slug: "github-actions", Name: "GitHub Actions"}
	clients := &root.Clients{
		GitHub: func(token string) *github.Client { return github.New(srv.URL, token) },
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}

	return srv, clients
}

// installOnOrganization records an app the stand-in knows as installed on the
// organization, in the app's own listing and in the organization's, and its record as
// GitHub serves it by slug, as an owner's click on Install leaves it.
func installOnOrganization(srv *githubtest.Server, id int64) {
	srv.Update(func() {
		app := srv.Apps[fmt.Sprint(id)]
		app.Record.ClientID = "Iv23client" + fmt.Sprint(id)
		app.Installations = []github.Installation{{ID: id + 1, AppID: id, Account: github.Account{Login: "imp-example", Type: "Organization"}, RepositorySelection: github.AllRepositories}}
		srv.OrgInstallations["imp-example"] = append(srv.OrgInstallations["imp-example"], github.OrgInstallation{ID: id + 1, AppID: id, AppSlug: app.Record.Slug, RepositorySelection: github.AllRepositories})
		public := app.Record
		public.PEM = ""
		srv.PublicApps[app.Record.Slug] = public
	})
}

// makeInBrowser plays the person's browser for the apps the page offers: for each it
// follows the redirect GitHub sends once the app is made, with a code the stand-in
// converts, then installs the app. The ids start at first.
func makeInBrowser(t *testing.T, srv *githubtest.Server, manifests []org.AppManifest, first int64) func(url string) error {
	t.Helper()

	return func(url string) error {
		go func() {
			for i, m := range manifests {
				page := get(t, url+"/")
				form := regexp.MustCompile(`state=([0-9a-f]+)"><input type="hidden" name="manifest" value="{&#34;name&#34;:&#34;` + regexp.QuoteMeta(m.Name) + `&#34;`).FindStringSubmatch(page)
				if form == nil {
					t.Errorf("the page has no form for %s:\n%s", m.Name, page)

					return
				}
				id := first + int64(i)
				registerApp(t, srv, m, id, false)
				srv.Update(func() { srv.Conversions["code-"+m.Name] = &srv.Apps[fmt.Sprint(id)].Record })
				get(t, url+"/made?code=code-"+m.Name+"&state="+form[1])
				installOnOrganization(srv, id)
				get(t, url+"/installed?installation_id="+fmt.Sprint(id+1)+"&setup_action=install")
			}
		}()

		return nil
	}
}

// TestBootstrapWithoutCloud: the bootstrap of a root organization without a cloud needs
// GitHub alone. It makes the two apps the placement calls for in the browser, the
// repository, and then, before the first commit, everything the engine keeps: the
// repository's rules, the environment with the infrastructure app's key sealed into it,
// each library, and the release app's credentials where the libraries read them. The
// first commit carries the placement, the README and the workflow, and the check that
// follows is clean. The keys are in GitHub's secrets and in no file.
func TestBootstrapWithoutCloud(t *testing.T) {
	t.Parallel()

	srv, clients := githubRootWorld(t)
	p := githubRootDraft()
	dir := t.TempDir()
	var out bytes.Buffer
	res, err := root.Bootstrap(context.Background(), clients, &root.BootstrapRequest{Placement: p, Dir: dir, OpenURL: makeInBrowser(t, srv, p.AppManifests(), 6000), FlowTimeout: time.Minute}, &out)
	if err != nil {
		t.Fatalf("Bootstrap() error = %v\n%s", err, out.String())
	}
	if res.Placement.GithubInfrastructureAppID != "6000" || res.Placement.GithubReleaseAppID != "6001" || res.Placement.GithubDeployerAppID != "" {
		t.Errorf("apps: infrastructure %q, release %q, deployer %q; want 6000, 6001 and none", res.Placement.GithubInfrastructureAppID, res.Placement.GithubReleaseAppID, res.Placement.GithubDeployerAppID)
	}
	for _, line := range []string{
		"make and install imp-example-infrastructure, imp-example-release: http://127.0.0.1:",
		"The keys go from GitHub into their GitHub secrets and never touch this computer's disk.",
		"Created the repository imp-example/imf-root-infrastructure, private.",
		"GitHub: seals the infrastructure app's key into the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of imp-example/imf-root-infrastructure",
		"libraries: creates the public repository imp-example/workflows, with an initial commit on master",
		"libraries: creates the private repository imp-example/notes, with an initial commit on master",
		"libraries: seals the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (workflows, notes)",
		"its workflow's apply runs on it",
		"Bootstrapped the imf root organization on GitHub alone: repository imp-example/imf-root-infrastructure at ",
		"the apps are infrastructure 6000, release 6001.",
	} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("the output lacks %q:\n%s", line, out.String())
		}
	}
	if !res.Report.Clean() {
		t.Errorf("the check after the bootstrap is not clean: %v %v", whats(res.Report), res.Report.Findings)
	}
	repo := srv.Repos["imp-example/imf-root-infrastructure"]
	if repo == nil || !repo.Private {
		t.Fatalf("the root repository: %+v, want it made private", repo)
	}
	env := repo.Environments[org.RootEnvironment]
	if env == nil || !env.Custom || !slices.Equal(env.Branches, []string{"master"}) {
		t.Fatalf("the environment %s: %+v, want it to admit master alone", org.RootEnvironment, env)
	}
	if sealed := env.Secrets[org.InfrastructureKeySecret]; sealed == nil || string(sealed.Value) != srv.Apps["6000"].Record.PEM {
		t.Errorf("the environment's secret does not hold the infrastructure app's key: %+v", sealed)
	}
	secrets := srv.OrgSecrets["imp-example"]
	if sealed := secrets[org.ReleaseKeyOrgSecret]; sealed == nil || string(sealed.Value) != srv.Apps["6001"].Record.PEM || sealed.Visibility != github.VisibilitySelected {
		t.Errorf("the organization secret %s does not hold the release app's key for the libraries alone: %+v", org.ReleaseKeyOrgSecret, sealed)
	}
	if sealed := secrets[org.ReleaseClientIDOrgSecret]; sealed == nil || string(sealed.Value) != "Iv23client6001" {
		t.Errorf("the organization secret %s does not hold the release app's client ID: %+v", org.ReleaseClientIDOrgSecret, sealed)
	}
	if workflows, notes := srv.Repos["imp-example/workflows"], srv.Repos["imp-example/notes"]; workflows == nil || workflows.Private || notes == nil || !notes.Private {
		t.Errorf("the libraries: workflows %+v, notes %+v; want one public and one private", workflows, notes)
	}
	// The apply comes before the first commit: the key is sealed and the branch ruled
	// when the workflow the commit carries starts.
	sealed := slices.IndexFunc(srv.Calls, func(call string) bool {
		return strings.HasPrefix(call, "PUT ") && strings.HasSuffix(call, "/environments/"+org.RootEnvironment+"/secrets/"+org.InfrastructureKeySecret)
	})
	pushed := slices.IndexFunc(srv.Calls, func(call string) bool {
		return strings.HasSuffix(call, "/repos/imp-example/imf-root-infrastructure/git/trees")
	})
	if sealed < 0 || pushed < 0 || sealed > pushed {
		t.Errorf("the key was sealed at call %d and the first commit's tree made at call %d; want the sealing first", sealed, pushed)
	}
	// What was pushed is what the directory holds: the placement with the apps recorded,
	// the README and the workflow; and no key is in any file of it.
	for _, name := range []string{org.PlacementFile, "README.md", org.WorkflowFile} {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("%s was not rendered into the directory: %v", name, err)
		}
	}
	recorded, err := org.ReadPlacement(filepath.Join(dir, org.PlacementFile))
	if err != nil || recorded.GithubInfrastructureAppID != "6000" || recorded.GithubReleaseAppSlug != "imp-example-release" {
		t.Errorf("the placement in the directory: %+v, %v", recorded, err)
	}
	var rendered []string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rendered = append(rendered, path)
		}

		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range rendered {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(content, []byte("PRIVATE KEY")) {
			t.Errorf("%s holds a private key", path)
		}
	}
}

// TestBootstrapWithoutCloudAgain: a run again finds the apps recorded in the directory
// and makes no second one, and since GitHub never gives a sealed key back it holds none:
// it says how to bring one. With a key of the infrastructure app generated anew and
// brought, it finishes: the key is sealed again, nothing is pushed over the foundation,
// and the check is clean.
func TestBootstrapWithoutCloudAgain(t *testing.T) {
	t.Parallel()

	srv, clients := githubRootWorld(t)
	p := githubRootDraft()
	dir := t.TempDir()
	var out bytes.Buffer
	if _, err := root.Bootstrap(context.Background(), clients, &root.BootstrapRequest{Placement: p, Dir: dir, OpenURL: makeInBrowser(t, srv, p.AppManifests(), 6000), FlowTimeout: time.Minute}, &out); err != nil {
		t.Fatalf("Bootstrap() error = %v\n%s", err, out.String())
	}
	again, err := org.ReadPlacement(filepath.Join(dir, org.PlacementFile))
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_, err = root.Bootstrap(context.Background(), clients, &root.BootstrapRequest{Placement: again, Dir: dir}, &out)
	if err == nil || !strings.Contains(err.Error(), "this run holds no key of it: GitHub never gives a sealed key back") || !strings.Contains(err.Error(), "--infrastructure-app 6000 --infrastructure-key <the downloaded file>") {
		t.Fatalf("a run again without a key: error = %v", err)
	}
	if !strings.Contains(out.String(), "the infrastructure app is App 6000 (recorded); this run holds no key of it.") {
		t.Errorf("output:\n%s", out.String())
	}
	// A key generated anew on the app's settings page: the app now signs with it.
	fresh, public := keyPair(t)
	srv.Update(func() {
		srv.Apps["6000"].Key = public
		srv.Apps["6000"].Record.PEM = string(fresh)
	})
	out.Reset()
	commits := len(srv.Messages)
	res, err := root.Bootstrap(context.Background(), clients, &root.BootstrapRequest{Placement: again, Dir: dir, Brought: map[org.AppRole]root.BroughtApp{org.InfrastructureApp: {ID: "6000", Key: fresh}}}, &out)
	if err != nil {
		t.Fatalf("a run again with the key brought: error = %v\n%s", err, out.String())
	}
	if !res.Report.Clean() {
		t.Errorf("the check after the run again is not clean: %v %v", whats(res.Report), res.Report.Findings)
	}
	if len(srv.Messages) != commits || !strings.Contains(out.String(), "carries the foundation already") {
		t.Errorf("the run again made %d commit(s); want none over the foundation:\n%s", len(srv.Messages)-commits, out.String())
	}
	if sealed := srv.Repos["imp-example/imf-root-infrastructure"].Environments[org.RootEnvironment].Secrets[org.InfrastructureKeySecret]; !bytes.Equal(sealed.Value, fresh) {
		t.Error("the key brought was not sealed into the environment's secret")
	}
	if len(srv.Apps) != 2 {
		t.Errorf("the stand-in knows %d apps; want the two the first run made", len(srv.Apps))
	}
}

// TestBootstrapWithoutCloudRefusals: an app the organization does not call for is
// refused before anything is made, and so is a brought app that is not the one the
// placement records.
func TestBootstrapWithoutCloudRefusals(t *testing.T) {
	t.Parallel()

	_, clients := githubRootWorld(t)
	tests := []struct {
		name    string
		mutate  func(p *org.Placement)
		brought map[org.AppRole]root.BroughtApp
		wantErr string
	}{
		{name: "a deployer app brought", brought: map[org.AppRole]root.BroughtApp{org.DeployerApp: {ID: "7", Key: []byte("key")}}, wantErr: "--deployer-app names the deployer app, which the imf root organization does not call for"},
		{name: "a release app brought and no library listed", mutate: func(p *org.Placement) { p.Libraries = nil }, brought: map[org.AppRole]root.BroughtApp{org.ReleaseApp: {ID: "7", Key: []byte("key")}}, wantErr: "--release-app names the release app, which the imf root organization does not call for"},
		{name: "another app than the one recorded", mutate: func(p *org.Placement) { p.GithubInfrastructureAppID = "6000" }, brought: map[org.AppRole]root.BroughtApp{org.InfrastructureApp: {ID: "7", Key: []byte("key")}}, wantErr: "placement.json records the infrastructure app as App 6000, and --infrastructure-app names App 7"},
		{name: "a placement that is not valid", mutate: func(p *org.Placement) { p.SourceRepo = "" }, wantErr: "sourceRepo is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := githubRootDraft()
			if tt.mutate != nil {
				tt.mutate(p)
			}
			var out bytes.Buffer
			_, err := root.Bootstrap(context.Background(), clients, &root.BootstrapRequest{Placement: p, Dir: t.TempDir(), Brought: tt.brought}, &out)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Bootstrap() error = %v, want it to say %q", err, tt.wantErr)
			}
		})
	}
}

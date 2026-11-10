package org

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// noCloudRoot is a root organization without a cloud: the GitHub organization, its two
// apps and three libraries, and nothing of Google's.
func noCloudRoot() *Placement {
	return &Placement{
		Kind: RootKind, Prefix: "imf", GithubOrganization: "imf-example", GithubDefaultBranch: "master", SourceRepo: "imf-root-infrastructure",
		GithubInfrastructureAppID: "6001", GithubReleaseAppID: "6002", GithubReleaseAppSlug: "imf-example-release",
		Libraries: []Library{
			{Repository: "workflows", Public: true, Checks: []string{"actionlint", "semantic-titles / Validate PR title"}},
			{Repository: "impulse", Public: true, Checks: []string{"golang-ci / Run tests and builds", "golang-ci / Run tests and builds (dev)"}},
			{Repository: "notes"},
		},
	}
}

// TestNoCloudRootValidate: a root organization that names no Google Cloud organization
// has no cloud, and its placement is held to that: the GitHub organization's own settings
// and its libraries, and none of what describes Google's side or what a root organization
// with a cloud holds for its hosting organizations.
func TestNoCloudRootValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(p *Placement)
		wantErr string
	}{
		{name: "libraries alone", mutate: func(*Placement) {}},
		{name: "no library yet", mutate: func(p *Placement) { p.Libraries = nil; p.GithubReleaseAppID, p.GithubReleaseAppSlug = "", "" }},
		{name: "no app made yet", mutate: func(p *Placement) {
			p.GithubInfrastructureAppID, p.GithubReleaseAppID, p.GithubReleaseAppSlug = "", "", ""
		}},
		{name: "a larger runner, the GitHub organization's own", mutate: func(p *Placement) { p.CiLargeRunner = "ci-large" }},
		{name: "a bedrock pinned", mutate: func(p *Placement) {
			p.BedrockVersion, p.BedrockSHA256 = "v0.1.0", strings.Repeat("ab", 32)
		}},
		{name: "no GitHub organization", mutate: func(p *Placement) { p.GithubOrganization = "" }, wantErr: "githubOrganization is empty"},
		{name: "no repository", mutate: func(p *Placement) { p.SourceRepo = "" }, wantErr: "sourceRepo is empty"},
		{name: "no default branch", mutate: func(p *Placement) { p.GithubDefaultBranch = "" }, wantErr: "githubDefaultBranch is empty"},
		{name: "a GitHub organization that is not a login", mutate: func(p *Placement) { p.GithubOrganization = "imf example" }, wantErr: `githubOrganization "imf example" is not a GitHub organization's login`},
		{name: "a billing account", mutate: func(p *Placement) { p.BillingAccount = "012345-6789AB-CDEF01" }, wantErr: "billingAccount is set, and this root organization has no cloud (placement.json names no organizationId)"},
		{name: "an organization domain", mutate: func(p *Placement) { p.OrganizationDomain = "imf.example" }, wantErr: "organizationDomain is set, and this root organization has no cloud"},
		{name: "a break-glass account", mutate: func(p *Placement) { p.BreakGlassAccount = "seed@imf.example" }, wantErr: "breakGlassAccount is set, and this root organization has no cloud"},
		{name: "a Bot Account", mutate: func(p *Placement) { p.GithubBotAccount = "imf-bot" }, wantErr: "githubBotAccount is set, and this root organization has no cloud"},
		{name: "a deployer app", mutate: func(p *Placement) { p.GithubDeployerAppID = "6003" }, wantErr: "githubDeployerAppId is set, and this root organization has no cloud"},
		{name: "a key pinned in Secret Manager", mutate: func(p *Placement) { p.GithubInfrastructureKeyVersion = "1" }, wantErr: "githubInfrastructureKeyVersion is set, and this root organization has no cloud"},
		{name: "a region", mutate: func(p *Placement) { p.Regions = []Region{{Name: "us-central1", Code: "uc1"}} }, wantErr: "regions is set, and this root organization has no cloud"},
		{name: "a spend budget", mutate: func(p *Placement) { p.MonthlyBudget = 100 }, wantErr: "monthlyBudget is set, and this root organization has no cloud"},
		{name: "a domain", mutate: func(p *Placement) { p.Domains = map[string]Domain{"imf.example": {}} }, wantErr: "domains is set, and this root organization has no cloud"},
		{name: "a hosting organization", mutate: func(p *Placement) {
			p.HostingOrganizations = []HostingOrganization{{Prefix: "imh", BootProject: "imh-boot-gbl-core-a1b2"}}
		}, wantErr: "hostingOrganizations is set, and this root organization has no cloud"},
		{name: "a key operator", mutate: func(p *Placement) { p.KeyOperators = []string{"group:ops@imf.example"} }, wantErr: "keyOperators is set, and this root organization has no cloud"},
		{name: "a project", mutate: func(p *Placement) { p.Projects = map[string]string{RootProjectKey: "imf-root-gbl-core-c3d4"} }, wantErr: "projects is set, and this root organization has no cloud"},
		{name: "a label", mutate: func(p *Placement) { p.Labels = map[string]string{"team": "framework"} }, wantErr: "labels is set, and this root organization has no cloud"},
		{name: "an apps domain", mutate: func(p *Placement) { p.AppsDomain = "apps.imf.example" }, wantErr: "appsDomain is set, and a root organization has no use for it"},
		{name: "the root of another organization", mutate: func(p *Placement) { p.Root = &RootPlace{Organization: "imq", Project: "imq-root-gbl-core-0a0b"} }, wantErr: "root is set, and a root organization holds the GitHub Apps' keys itself"},
		{name: "a library named twice", mutate: func(p *Placement) { p.Libraries = append(p.Libraries, Library{Repository: "Impulse"}) }, wantErr: "libraries[Impulse] is listed twice"},
		{name: "a library that is the root's own repository", mutate: func(p *Placement) { p.Libraries[2].Repository = "imf-root-infrastructure" }, wantErr: "libraries[imf-root-infrastructure] is this organization's own repository (sourceRepo)"},
		{name: "a library whose name is not a repository's", mutate: func(p *Placement) { p.Libraries[2].Repository = "notes and more" }, wantErr: `libraries[2]: repository "notes and more" is not a repository's name`},
		{name: "a check named twice", mutate: func(p *Placement) { p.Libraries[0].Checks = append(p.Libraries[0].Checks, "actionlint") }, wantErr: `libraries[workflows] names the check "actionlint" twice`},
		{name: "a blank check", mutate: func(p *Placement) { p.Libraries[0].Checks = append(p.Libraries[0].Checks, " ") }, wantErr: `libraries[workflows]: checks[2] " " is blank or carries a space at an end`},
		{name: "an app prefix too long for the two names", mutate: func(p *Placement) { p.GithubAppPrefix = "the-framework-libraries-of-imf" }, wantErr: "the GitHub App name the-framework-libraries-of-imf-infrastructure is 45 characters, and GitHub allows 34"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := noCloudRoot()
			tt.mutate(p)
			err := p.Validate()
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Validate() error = %v, want none", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Validate() error = %v, want it to say %q", err, tt.wantErr)
			}
		})
	}
}

// TestLibrariesOfOtherOrganizations: a root organization with a cloud lists libraries
// beside its domains and hosting organizations, and a hosting organization lists none.
func TestLibrariesOfOtherOrganizations(t *testing.T) {
	t.Parallel()

	root := rootPlacement(t)
	root.Libraries = []Library{{Repository: "shared-types", Checks: []string{"ci"}}}
	if err := root.Validate(); err != nil {
		t.Errorf("a root organization with a cloud and a library: Validate() error = %v", err)
	}
	if !root.HasCloud() || noCloudRoot().HasCloud() {
		t.Errorf("HasCloud() = %t for the root with an organizationId and %t for the one without; want true and false", root.HasCloud(), noCloudRoot().HasCloud())
	}
	hosting := testPlacement(t)
	if !hosting.HasCloud() {
		t.Error("a hosting organization has a cloud")
	}
	hosting.Libraries = []Library{{Repository: "shared-types"}}
	if err := hosting.Validate(); err == nil || !strings.Contains(err.Error(), "libraries is set, and a hosting organization holds none") {
		t.Errorf("a hosting organization with a library: Validate() error = %v", err)
	}
}

// TestAppsFollowThePlacement: the infrastructure app always; the deployer app with a
// cloud; the release app with a cloud or a library. Their permissions follow too: what a
// root that keeps libraries adds, and what a root without a cloud adds to the
// infrastructure app, whose key is then an organization secret.
func TestAppsFollowThePlacement(t *testing.T) {
	t.Parallel()

	none := noCloudRoot()
	none.Libraries = nil
	withLibrary := rootPlacement(t)
	withLibrary.Libraries = []Library{{Repository: "shared-types"}}
	tests := []struct {
		name string
		p    *Placement
		want []AppRole
	}{
		{name: "a root with a cloud", p: rootPlacement(t), want: []AppRole{InfrastructureApp, DeployerApp, ReleaseApp}},
		{name: "a root with a cloud and a library", p: withLibrary, want: []AppRole{InfrastructureApp, DeployerApp, ReleaseApp}},
		{name: "a hosting organization", p: testPlacement(t), want: []AppRole{InfrastructureApp, DeployerApp, ReleaseApp}},
		{name: "a root without a cloud, with libraries", p: noCloudRoot(), want: []AppRole{InfrastructureApp, ReleaseApp}},
		{name: "a root without a cloud, with none", p: none, want: []AppRole{InfrastructureApp}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.p.Apps(); !slices.Equal(got, tt.want) {
				t.Errorf("Apps() = %v, want %v", got, tt.want)
			}
			var made []AppRole
			for _, m := range tt.p.AppManifests() {
				made = append(made, m.Role)
			}
			if !slices.Equal(made, tt.want) {
				t.Errorf("AppManifests() declares %v, want %v", made, tt.want)
			}
		})
	}

	base := rootPlacement(t).AppManifest(InfrastructureApp).Permissions
	wantLibraries := map[string]string{PermissionOrgSecrets: LevelWrite, PermissionChecks: LevelRead}
	wantNoCloud := map[string]string{PermissionOrgSecrets: LevelWrite, PermissionChecks: LevelRead, PermissionWorkflows: LevelWrite, PermissionEnvironments: LevelWrite, PermissionIssues: LevelWrite}
	if diff := cmp.Diff(wantLibraries, added(base, withLibrary.AppManifest(InfrastructureApp).Permissions)); diff != "" {
		t.Errorf("the infrastructure app of a root with a cloud and a library adds (-want +got):\n%s", diff)
	}
	infra := noCloudRoot().AppManifest(InfrastructureApp)
	if diff := cmp.Diff(wantNoCloud, added(base, infra.Permissions)); diff != "" {
		t.Errorf("the infrastructure app of a root without a cloud adds (-want +got):\n%s", diff)
	}
	if strings.Contains(infra.Description, "Secret Manager") || !strings.Contains(infra.Description, "environment") {
		t.Errorf("the infrastructure app's description without a cloud = %q", infra.Description)
	}
	releaseBase := rootPlacement(t).AppManifest(ReleaseApp).Permissions
	if diff := cmp.Diff(map[string]string{PermissionWorkflows: LevelWrite}, added(releaseBase, noCloudRoot().AppManifest(ReleaseApp).Permissions)); diff != "" {
		t.Errorf("the release app of a root with libraries adds (-want +got):\n%s", diff)
	}
	if got := added(base, testPlacement(t).AppManifest(InfrastructureApp).Permissions); len(got) != 0 {
		t.Errorf("a hosting organization's placement adds %v to the infrastructure app", got)
	}
}

// added are the permissions got holds beyond base.
func added(base, got map[string]string) map[string]string {
	extra := map[string]string{}
	for name, level := range got {
		if base[name] != level {
			extra[name] = level
		}
	}

	return extra
}

// TestNoCloudPlacementFile: a root organization without a cloud writes a file that names
// what it has and leaves out the keys of Google's side, reads back as written, and names
// the login its release app's actions carry.
func TestNoCloudPlacementFile(t *testing.T) {
	t.Parallel()

	p := noCloudRoot()
	data, err := p.JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	text := string(data)
	for _, absent := range []string{"organizationId", "organizationDomain", "billingAccount", "breakGlassAccount", "githubBotAccount", "regions", "contactDomains", "applications", "labels", ": null", `: ""`} {
		if strings.Contains(text, absent) {
			t.Errorf("the file carries %q:\n%s", absent, text)
		}
	}
	for _, present := range []string{`"kind": "root"`, `"githubOrganization": "imf-example"`, `"libraries": [`, `"repository": "impulse"`, `"golang-ci / Run tests and builds (dev)"`} {
		if !strings.Contains(text, present) {
			t.Errorf("the file lacks %q:\n%s", present, text)
		}
	}
	path := filepath.Join(t.TempDir(), PlacementFile)
	if err := p.Write(path); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	back, err := ReadPlacement(path)
	if err != nil {
		t.Fatalf("ReadPlacement() error = %v\n%s", err, text)
	}
	if diff := cmp.Diff(p, back); diff != "" {
		t.Errorf("the placement read back differs (-written +read):\n%s", diff)
	}
	if got := p.ReleaseAppLogin(); got != "imf-example-release[bot]" {
		t.Errorf("ReleaseAppLogin() = %q", got)
	}
	if got := p.Library("impulse"); got == nil || !got.Public || p.Library("absent") != nil {
		t.Errorf("Library() = %+v for impulse, %+v for absent", got, p.Library("absent"))
	}
	with, err := rootPlacement(t).JSON()
	if err != nil {
		t.Fatalf("JSON() error = %v", err)
	}
	if !strings.Contains(string(with), `"applications": null`) {
		t.Errorf("a root organization with a cloud writes its file as before; got:\n%s", with)
	}
}

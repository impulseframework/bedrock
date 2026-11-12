// apps.go declares bedrock's three GitHub Apps: what each is for, and the permissions
// each holds, as GitHub names them. The declaration is the manifest bedrock org bootstrap
// makes an app from, in the browser, with one click (github.Manifest), and the measure an
// app the organization brought instead is held to: a key that signs as the app proves
// which app it is, and the app's own record (GET /app) and installations say whether it
// is the organization's, holds what the manifest declares and no more, and is installed
// on every repository. bedrock org check reports each difference.

package org

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/github"
)

// AppRole names one of the three GitHub Apps bedrock acts as.
type AppRole string

// The three apps, in the order the bootstrap makes them.
const (
	// InfrastructureApp administers the organization's repositories: the root
	// organization's apply creates a hosting organization's repository and pushes its
	// render as it, the runs of 1-org configure the applications' repositories as it,
	// and it bypasses the infrastructure repositories' branch rules, which it writes.
	InfrastructureApp AppRole = "infrastructure"
	// DeployerApp is who the plan trigger's run posts a plan as, and who every
	// application's pipeline talks back on a pull request as.
	DeployerApp AppRole = "deployer"
	// ReleaseApp is who release-please cuts an application's releases as: the only actor
	// that creates, moves or deletes a release tag.
	ReleaseApp AppRole = "release"
)

// AppRoles are the three apps, in the order the bootstrap makes them.
var AppRoles = []AppRole{InfrastructureApp, DeployerApp, ReleaseApp}

// GitHub's names of the permissions the apps hold, as the app's own record and a
// manifest carry them, and the two levels.
const (
	PermissionActions        = "actions"
	PermissionAdministration = "administration"
	PermissionChecks         = "checks"
	PermissionContents       = "contents"
	PermissionDeployments    = "deployments"
	PermissionEnvironments   = "environments"
	PermissionIssues         = "issues"
	PermissionMetadata       = "metadata"
	PermissionPullRequests   = "pull_requests"
	PermissionWorkflows      = "workflows"
	PermissionOrgAdmin       = "organization_administration"
	PermissionOrgVariables   = "organization_actions_variables"
	PermissionOrgRunners     = "organization_self_hosted_runners"
	PermissionOrgSecrets     = "organization_secrets"

	LevelRead  = "read"
	LevelWrite = "write"
)

// maxAppNameLength is GitHub's limit on an app's name.
const maxAppNameLength = 34

// AppManifest is bedrock's declaration of one of its GitHub Apps: the role, the name,
// what it is for in a sentence (the app's description on GitHub) and its permissions, by
// GitHub's name of each to its level.
type AppManifest struct {
	Role        AppRole
	Name        string
	Description string
	Permissions map[string]string
}

// appPermissions are each app's permissions. GitHub gives every app Metadata read, and
// the record carries it, so each declares it.
var appPermissions = map[AppRole]map[string]string{
	InfrastructureApp: {
		// 1-org's GitHub provider reads a repository's environments and their branch
		// policies on Actions and writes them, with the rulesets, on Administration.
		PermissionActions:        LevelRead,
		PermissionAdministration: LevelWrite,
		// The root organization's apply pushes a created organization's render, and
		// opens the hand-off pull request in the root repository from the branch it
		// pushed there (GitHub resolves the pull request's head through Contents).
		PermissionContents:     LevelWrite,
		PermissionMetadata:     LevelRead,
		PermissionPullRequests: LevelWrite,
		// The organization's runner group, hosted runner and CI_LARGE_RUNNER variable,
		// the repositories it creates, and the organization's policy on immutable
		// releases, which the root organization's apply sets to every repository.
		PermissionOrgAdmin:     LevelWrite,
		PermissionOrgVariables: LevelWrite,
		PermissionOrgRunners:   LevelWrite,
	},
	DeployerApp: {
		PermissionChecks:       LevelWrite,
		PermissionDeployments:  LevelWrite,
		PermissionIssues:       LevelWrite,
		PermissionMetadata:     LevelRead,
		PermissionPullRequests: LevelWrite,
	},
	ReleaseApp: {
		PermissionContents:     LevelWrite,
		PermissionIssues:       LevelWrite,
		PermissionMetadata:     LevelRead,
		PermissionPullRequests: LevelWrite,
	},
}

// libraryPermissions are what an app holds beyond appPermissions in a root organization
// that keeps libraries: the infrastructure app seals the release app's key into an
// organization secret and keeps the repositories that read it, and reads the checks a
// library's latest pull request reported; the release app pushes a release branch that
// carries the repository's workflow files as the default branch has them since.
var libraryPermissions = map[AppRole]map[string]string{
	InfrastructureApp: {PermissionOrgSecrets: LevelWrite, PermissionChecks: LevelRead},
	ReleaseApp:        {PermissionWorkflows: LevelWrite},
}

// noCloudPermissions are what the infrastructure app holds beyond those in a root
// organization without a cloud, whose engine runs in the root repository's workflow: it
// pushes that workflow file with the first commit, seals its own key into the secret of
// the root repository's environment, and keeps the one issue the weekly check reports its
// findings in.
var noCloudPermissions = map[string]string{PermissionWorkflows: LevelWrite, PermissionEnvironments: LevelWrite, PermissionIssues: LevelWrite}

// noCloudInfrastructureDescription is the infrastructure app's description in a root
// organization without a cloud, where its key is a GitHub secret.
const noCloudInfrastructureDescription = "bedrock's infrastructure app: creates and configures the organization's repositories from the root organization's repository. Its key is a secret of that repository's bedrock environment, which the default branch's workflow alone reads."

// appDescriptions say what each app is for, on GitHub.
var appDescriptions = map[AppRole]string{
	InfrastructureApp: "bedrock's infrastructure app: creates and configures the organization's repositories for the infrastructure repositories' runs. Its key lives in Secret Manager; it is never a GitHub secret.",
	DeployerApp:       "bedrock's deployer app: posts plans and talks back on pull requests for the applications' pipelines and the infrastructure repositories' runs.",
	ReleaseApp:        "bedrock's release app: cuts the applications' releases as release-please, the only actor that makes a release tag.",
}

// appTitles name each app in a sentence.
var appTitles = map[AppRole]string{
	InfrastructureApp: "the infrastructure app",
	DeployerApp:       "the deployer app",
	ReleaseApp:        "the release app",
}

// Title names the app in a sentence (the infrastructure app).
func (r AppRole) Title() string {
	return appTitles[r]
}

// Key is the name of the secret that holds the app's private key (github-<role>-key).
func (r AppRole) Key() string {
	return "github-" + string(r) + "-key"
}

// AppNamePrefix is the word the apps' names start with (<word>-infrastructure,
// <word>-deployer, <word>-release): githubAppPrefix when set, else the GitHub
// organization's login.
func (p *Placement) AppNamePrefix() string {
	if p.GithubAppPrefix != "" {
		return p.GithubAppPrefix
	}

	return p.GithubOrganization
}

// AppName is the app's name on GitHub.
func (p *Placement) AppName(role AppRole) string {
	return p.AppNamePrefix() + "-" + string(role)
}

// ReleaseAppSlug is the release app's slug, the name in its page's address, as every
// application's placement records the author of its releases: the slug the root
// organization recorded when the app was made, or the app's declared name, which GitHub
// takes as the slug when no other app has it.
func (p *Placement) ReleaseAppSlug() string {
	if p.GithubReleaseAppSlug != "" {
		return p.GithubReleaseAppSlug
	}

	return p.AppName(ReleaseApp)
}

// AppManifest is the declaration of one of the organization's apps, as the placement
// shapes it: what every organization's app holds, what a root organization that keeps
// libraries adds, and what one without a cloud adds to the infrastructure app. The
// permissions are the caller's copy.
func (p *Placement) AppManifest(role AppRole) AppManifest {
	m := AppManifest{Role: role, Name: p.AppName(role), Description: appDescriptions[role], Permissions: maps.Clone(appPermissions[role])}
	if !p.IsRoot() {
		return m
	}
	if len(p.Libraries) > 0 || !p.HasCloud() {
		maps.Copy(m.Permissions, libraryPermissions[role])
	}
	if !p.HasCloud() && role == InfrastructureApp {
		maps.Copy(m.Permissions, noCloudPermissions)
		m.Description = noCloudInfrastructureDescription
	}

	return m
}

// AppManifests are the declarations of the apps the placement calls for (Apps), in the
// order the bootstrap makes them.
func (p *Placement) AppManifests() []AppManifest {
	roles := p.Apps()
	manifests := make([]AppManifest, 0, len(roles))
	for _, role := range roles {
		manifests = append(manifests, p.AppManifest(role))
	}

	return manifests
}

// AppID is the App ID the placement records for the role, or empty.
func (p *Placement) AppID(role AppRole) string {
	switch role {
	case InfrastructureApp:
		return p.GithubInfrastructureAppID
	case DeployerApp:
		return p.GithubDeployerAppID
	default:
		return p.GithubReleaseAppID
	}
}

// SetApp records the app's id, and for the release app its slug, as GitHub gave them.
func (p *Placement) SetApp(role AppRole, app *github.App) {
	id := fmt.Sprint(app.ID)
	switch role {
	case InfrastructureApp:
		p.GithubInfrastructureAppID = id
	case DeployerApp:
		p.GithubDeployerAppID = id
	default:
		p.GithubReleaseAppID, p.GithubReleaseAppSlug = id, app.Slug
	}
}

// GitHub is the manifest as the browser posts it: the app's homepage, where GitHub sends
// the browser with the code once the app is made (redirect), and where it sends it once
// the app is installed (setup); no webhook, installable by the owning organization alone.
func (m AppManifest) GitHub(homepage, redirect, setup string) github.Manifest {
	return github.Manifest{
		Name:               m.Name,
		URL:                homepage,
		HookAttributes:     github.HookAttributes{URL: homepage, Active: false},
		RedirectURL:        redirect,
		SetupURL:           setup,
		Description:        m.Description,
		Public:             false,
		DefaultEvents:      []string{},
		DefaultPermissions: m.Permissions,
	}
}

// Verify holds an app to its declaration: owned by the organization, every declared
// permission held at its level and no other, and installed on the organization for all
// of its repositories. Each difference is one finding, in a sentence; none means the app
// is as declared.
func (m AppManifest) Verify(app *github.App, installations []github.Installation, organization string) []string {
	var findings []string
	if app.Owner.Login != organization {
		findings = append(findings, fmt.Sprintf("%s (App %d, %s) is owned by %s, not the %s organization", m.Role.Title(), app.ID, app.Name, app.Owner.Login, organization))
	}
	for _, name := range sortedKeys(m.Permissions) {
		want, got := m.Permissions[name], app.Permissions[name]
		switch {
		case got == "":
			findings = append(findings, fmt.Sprintf("%s lacks the %s permission (%s): add it on the app's settings page (Permissions & events), then accept the new permission on the organization's installation of the app (the organization's settings, GitHub Apps)", m.Role.Title(), permissionTitle(name), want))
		case level(got) < level(want):
			findings = append(findings, fmt.Sprintf("%s holds the %s permission as %s, not %s: raise it on the app's settings page (Permissions & events), then accept the new permission on the organization's installation of the app (the organization's settings, GitHub Apps)", m.Role.Title(), permissionTitle(name), got, want))
		}
	}
	for _, name := range sortedKeys(app.Permissions) {
		if _, declared := m.Permissions[name]; !declared {
			findings = append(findings, fmt.Sprintf("%s holds the %s permission (%s), which it does not need: take it off on the app's settings page", m.Role.Title(), permissionTitle(name), app.Permissions[name]))
		}
	}
	i := slices.IndexFunc(installations, func(in github.Installation) bool { return in.Account.Login == organization })
	switch {
	case i < 0:
		findings = append(findings, fmt.Sprintf("%s is not installed on the %s organization: install it there, for all repositories", m.Role.Title(), organization))
	case installations[i].RepositorySelection != github.AllRepositories:
		findings = append(findings, fmt.Sprintf("%s is installed on a selection of the %s organization's repositories, not all of them: a repository made later would be outside it", m.Role.Title(), organization))
	}

	return findings
}

// level ranks a permission's level: read below write below admin.
func level(l string) int {
	switch l {
	case LevelRead:
		return 1
	case LevelWrite:
		return 2
	case "admin":
		return 3
	default:
		return 0
	}
}

// permissionTitle names a permission as GitHub's settings page does: Contents,
// Administration (organization).
func permissionTitle(name string) string {
	words := strings.Split(name, "_")
	if words[0] == "organization" && len(words) > 1 {
		return titleWords(words[1:]) + " (organization)"
	}

	return titleWords(words)
}

// titleWords joins the words of a permission's name as a title (pull_requests: Pull
// requests; actions_variables: Actions variables).
func titleWords(words []string) string {
	title := strings.Join(words, " ")

	return strings.ToUpper(title[:1]) + title[1:]
}

// sortedKeys lists a map's keys in order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return keys
}

// validateAppNames refuses a prefix that makes an app's name longer than GitHub allows,
// or that is not a word GitHub takes in a name.
func (p *Placement) validateAppNames() error {
	if p.GithubAppPrefix != "" && !githubLoginRE.MatchString(p.GithubAppPrefix) {
		return errors.Newf("githubAppPrefix %q is not a word the GitHub Apps' names can start with (letters, digits and hyphens)", p.GithubAppPrefix)
	}
	for _, role := range p.Apps() {
		if name := p.AppName(role); len(name) > maxAppNameLength {
			return errors.Newf("the GitHub App name %s is %d characters, and GitHub allows %d: set githubAppPrefix to a shorter word than %s for the apps' names", name, len(name), maxAppNameLength, p.AppNamePrefix())
		}
	}

	return nil
}

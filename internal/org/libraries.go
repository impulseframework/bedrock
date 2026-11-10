// libraries.go is the libraries of a root organization, and the root organization that
// has no cloud. A library is a project that deploys nowhere: a repository the root
// organization's placement lists, which its engine creates and keeps on GitHub by API
// (the repository, whether it is public, its merge settings, the ruleset on its default
// branch with the checks the entry names, the organization's ruleset on version tags, and
// the organization secrets its release workflow reads). It belongs to the root
// organization, which owns the GitHub organization, so a library two hosting
// organizations' applications share has one home. A root organization whose placement
// names no Google Cloud organization has no cloud: it holds libraries alone, nothing of
// Google's exists for it, and its engine runs in GitHub Actions as its infrastructure app
// (the workflow bedrock renders into its repository).

package org

import (
	"bytes"
	"slices"
	"strings"

	"github.com/go-playground/errors/v5"
)

// Library is one library of a root organization: a repository of the GitHub
// organization that deploys nowhere.
type Library struct {
	// Repository is the repository's name in the GitHub organization.
	Repository string `json:"repository"`
	// Public says the repository is public; left out, it is private. The engine brings
	// the repository to it either way.
	Public bool `json:"public,omitempty"`
	// Checks are the checks a pull request into the default branch must pass, each by
	// the name a pull request shows it under: a job's name, or for a job of a reusable
	// workflow the calling job's id, a slash and the called job's name
	// (golang-ci / Run tests and builds). GitHub Actions is the reporter of each. The
	// engine writes no workflow: the names are what the repository's own workflow files
	// report, and a name no pull request reports is a finding, since every pull request
	// would wait for it.
	Checks []string `json:"checks,omitempty"`
	// Adopted says the repository stood before the placement listed it (one transferred
	// into the organization): the engine brings its settings and its ruleset to the
	// placement as it does any library's, reports the rulesets it carries beside the
	// engine's own, and, while the entry names no checks, prints the ones those rulesets
	// require, which become the entry's first list.
	Adopted bool `json:"adopted,omitempty"`
}

// What the engine keeps on GitHub for a root organization's workflows, by the names the
// workflows read them under. The infrastructure app's private key, in a root organization
// without a cloud, is a secret of an environment of the root repository, which admits the
// default branch alone: the root's own workflow mints its token from it, and a workflow on
// any other branch is never handed it. The release app's client ID and private key are
// organization secrets the library repositories alone read, from which a library's release
// workflow mints the token release-please cuts its releases with. The release app's login
// is an organization variable, the author a release workflow accepts a release from.
const (
	RootEnvironment          = "bedrock"
	InfrastructureKeySecret  = "BEDROCK_INFRASTRUCTURE_APP_KEY" //nolint:gosec // a secret's name, never its value
	ReleaseClientIDOrgSecret = "RELEASE_PLEASE_APP_CLIENT_ID"
	ReleaseKeyOrgSecret      = "RELEASE_PLEASE_APP_PEM"
	ReleaseLoginVariable     = "RELEASE_APP_LOGIN"
	// ReleaseKeyVersionVariable names the Secret Manager version the release app's key
	// was sealed into its organization secret from, in a root organization with a cloud:
	// GitHub never gives a secret's value back, so the version is what the apply compares
	// with the placement's pin.
	ReleaseKeyVersionVariable = "RELEASE_PLEASE_APP_PEM_VERSION"
)

// The workflow bedrock renders into the repository of a root organization without a
// cloud, and its jobs by the names a pull request and the ruleset show them under: the
// plan a pull request must pass, and the apply a merge runs.
const (
	WorkflowFile = ".github/workflows/bedrock.yml"
	PlanJobName  = "bedrock plan"
	ApplyJobName = "bedrock apply"
	CheckJobName = "bedrock check"
)

// botSuffix is what GitHub appends to an app's slug for the login its actions carry.
const botSuffix = "[bot]"

// HasCloud reports whether the organization has a Google Cloud side. A hosting
// organization always has one; a root organization has one when its placement names the
// Google Cloud organization (organizationId), and none when it holds libraries alone.
func (p *Placement) HasCloud() bool {
	return !p.IsRoot() || p.OrganizationID != ""
}

// LibraryNames lists the libraries' repositories in the order the placement lists them.
func (p *Placement) LibraryNames() []string {
	names := make([]string, 0, len(p.Libraries))
	for _, l := range p.Libraries {
		names = append(names, l.Repository)
	}

	return names
}

// Library is the library whose repository has the name, nil when the placement lists
// none.
func (p *Placement) Library(repository string) *Library {
	for i := range p.Libraries {
		if p.Libraries[i].Repository == repository {
			return &p.Libraries[i]
		}
	}

	return nil
}

// Apps are the GitHub Apps the placement calls for, in the order the bootstrap makes
// them: the infrastructure app always; the deployer app when the organization has a
// cloud, where the applications' pipelines and the plan trigger talk back as it; the
// release app when it has a cloud or lists libraries, since a release needs an actor
// whose tag pushes start workflows, which a workflow's own token cannot be, and the
// infrastructure app's key never leaves the root organization's repository.
func (p *Placement) Apps() []AppRole {
	if p.HasCloud() {
		return AppRoles
	}
	roles := []AppRole{InfrastructureApp}
	if len(p.Libraries) > 0 {
		roles = append(roles, ReleaseApp)
	}

	return roles
}

// ReleaseAppLogin is the login the release app's actions carry (<slug>[bot]), which a
// library's release workflow accepts a release from; empty while no release app is
// recorded.
func (p *Placement) ReleaseAppLogin() string {
	if p.GithubReleaseAppID == "" {
		return ""
	}

	return p.ReleaseAppSlug() + botSuffix
}

// validateLibraries holds the libraries to the rules: a root organization's alone; each
// a repository name GitHub takes, listed once, and not the organization's own
// repository; each check named once and not blank.
func (p *Placement) validateLibraries() error {
	if len(p.Libraries) > 0 && !p.IsRoot() {
		return errors.New("libraries is set, and a hosting organization holds none: a library is a project of the root organization, which owns the GitHub organization; list it in that organization's placement.json")
	}
	seen := map[string]bool{}
	for i, l := range p.Libraries {
		where := "libraries[" + l.Repository + "]"
		switch {
		case !repositoryRE.MatchString(l.Repository):
			return errors.Newf("libraries[%d]: repository %q is not a repository's name (letters, digits, hyphens, underscores and periods, at most a hundred)", i, l.Repository)
		case strings.EqualFold(l.Repository, p.SourceRepo):
			return errors.Newf("%s is this organization's own repository (sourceRepo), which holds the placement: a library is another repository", where)
		case seen[strings.ToLower(l.Repository)]:
			return errors.Newf("%s is listed twice", where)
		}
		seen[strings.ToLower(l.Repository)] = true
		for j, check := range l.Checks {
			if strings.TrimSpace(check) == "" || check != strings.TrimSpace(check) {
				return errors.Newf("%s: checks[%d] %q is blank or carries a space at an end: a check is named as a pull request shows it (golang-ci / Run tests and builds)", where, j, check)
			}
			if slices.Contains(l.Checks[:j], check) {
				return errors.Newf("%s names the check %q twice", where, check)
			}
		}
	}

	return nil
}

// validateNoCloud holds a root organization without a cloud to having none: every field
// that describes something of Google's, or that exists for the hosting organizations and
// the domains a root organization with a cloud holds, is refused, each by its name.
func (p *Placement) validateNoCloud() error {
	const why = "is set, and this root organization has no cloud (placement.json names no organizationId): it holds libraries alone, kept on GitHub by its workflow"
	for name, value := range map[string]string{
		"organizationDomain": p.OrganizationDomain, "directoryCustomerId": p.DirectoryCustomerID, "billingAccount": p.BillingAccount,
		"breakGlassAccount": p.BreakGlassAccount, "stateBucket": p.StateBucket,
		"githubBotAccount": p.GithubBotAccount, "githubDeployerAppId": p.GithubDeployerAppID,
		"cloudBuildInstallationId": p.CloudBuildInstallationID, "cloudBuildTokenSecretVersion": p.CloudBuildTokenSecretVersion,
	} {
		if value != "" {
			return errors.Newf("%s %s", name, why)
		}
	}
	// The keys are organization secrets on GitHub here, which carry no version to pin.
	for _, name := range Secrets {
		if p.KeyVersion(name) != "" {
			return errors.Newf("%s %s", pinKey(name), why)
		}
	}
	for name, set := range map[string]bool{
		"monthlyBudget": p.MonthlyBudget != 0, "regions": len(p.Regions) > 0, "domains": len(p.Domains) > 0,
		"hostingOrganizations": len(p.HostingOrganizations) > 0, "keyOperators": len(p.KeyOperators) > 0,
		"projects": len(p.Projects) > 0, "projectNumbers": len(p.ProjectNumbers) > 0, "labels": len(p.Labels) > 0, "contactDomains": len(p.ContactDomains) > 0,
	} {
		if set {
			return errors.Newf("%s %s", name, why)
		}
	}

	return nil
}

// withoutEmptyKeys takes the top-level keys whose value is an empty string or null out of
// an indented JSON object, as json.MarshalIndent writes one: each such key is one line,
// two spaces in. The entry left last loses its comma.
func withoutEmptyKeys(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	kept := make([][]byte, 0, len(lines))
	for _, line := range lines {
		value := bytes.TrimSuffix(line, []byte(","))
		if bytes.HasPrefix(line, []byte(`  "`)) && (bytes.HasSuffix(value, []byte(`": ""`)) || bytes.HasSuffix(value, []byte(`": null`))) {
			continue
		}
		kept = append(kept, line)
	}
	if last := len(kept) - 2; last > 0 && bytes.Equal(kept[len(kept)-1], []byte("}")) {
		kept[last] = bytes.TrimSuffix(kept[last], []byte(","))
	}

	return bytes.Join(kept, []byte("\n"))
}

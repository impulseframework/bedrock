// libraries.go is the engine's part for the libraries of a root organization, and the
// engine of a root organization without a cloud. A library is a repository the placement
// lists: the engine creates it, brings its visibility, its merge settings and the ruleset
// on its default branch to the entry, rules the libraries' version tags once for the
// organization, keeps the release app's key where the libraries' release workflows read
// it and nowhere else, and says when the checks an entry requires are not the checks the
// repository's pull requests report. It writes no file in a library: the workflows are
// the repository's own. Without a cloud nothing of Google's is opened: the run reads and
// writes GitHub alone, as the infrastructure app, from the root repository's workflow.

package root

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/create"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// runWithoutCloud runs the engine for a root organization without a cloud: the root
// repository and its environment, the organization's runner, the apps, and the libraries.
func runWithoutCloud(ctx context.Context, clients *Clients, req *Request, out io.Writer) (*Report, error) {
	if clients.GitHub == nil || req.GitHubToken == "" {
		return nil, errors.Newf("no GitHub token: the %s organization has no cloud and is kept on GitHub alone, as its infrastructure app (in its repository's workflow) or as a person who owns the GitHub organization (GITHUB_TOKEN)", req.Placement.Prefix)
	}
	e := &engine{clients: clients, req: req, p: req.Placement, d: NewDesired(req.Placement), out: out, report: &Report{}}
	e.gh = clients.GitHub(req.GitHubToken)
	for _, section := range []func(ctx context.Context) error{e.githubSection, e.rootEnvironment, e.listedApps, e.libraries} {
		if err := section(ctx); err != nil {
			return e.report, err
		}
	}

	return e.report, nil
}

// rootEnvironment keeps the environment of the root repository the workflow's jobs run
// in, and the infrastructure app's key in it: the environment admits the default branch
// alone, so a workflow on any other branch is never handed the key.
func (e *engine) rootEnvironment(ctx context.Context) error {
	owner, repo, name := e.p.GithubOrganization, e.p.SourceRepo, org.RootEnvironment
	if _, err := e.gh.Repository(ctx, owner, repo); err != nil {
		if github.NotFound(err) {
			return nil
		}

		return errors.Wrapf(err, "reading %s/%s", owner, repo)
	}
	branch := e.p.GithubDefaultBranch
	env, err := e.gh.Environment(ctx, owner, repo, name)
	switch {
	case github.NotFound(err):
		env = nil
	case err != nil:
		return errors.Wrapf(err, "reading the environment %s of %s/%s", name, owner, repo)
	}
	// An environment keeps patterns of its own only once it is made so: one a workflow's
	// first run made by naming it, or a person by hand, admits every branch.
	custom := env != nil && env.BranchPolicy != nil && env.BranchPolicy.CustomBranchPolicies
	if !custom {
		what := fmt.Sprintf("makes the environment %s of %s/%s, which the workflow's jobs run in and which admits the branch %s alone", name, owner, repo, branch)
		if env != nil {
			what = fmt.Sprintf("limits the environment %s of %s/%s to the branch %s: it admitted every branch, or every protected one", name, owner, repo, branch)
		}
		if err := e.change(ctx, sectionGitHub, what, func(ctx context.Context) error {
			return e.gh.PutEnvironment(ctx, owner, repo, name)
		}); err != nil {
			return err
		}
		if e.req.Mode != Apply {
			// The sentence above says the branch; nothing stands to list.
			return e.infrastructureKey(ctx, env != nil)
		}
	}
	branches, tags, err := e.gh.EnvironmentPatterns(ctx, owner, repo, name)
	if err != nil {
		return errors.Wrapf(err, "listing what the environment %s of %s/%s admits", name, owner, repo)
	}
	if !slices.Contains(branches, branch) {
		if err := e.change(ctx, sectionGitHub, fmt.Sprintf("admits the branch %s to the environment %s of %s/%s", branch, name, owner, repo), func(ctx context.Context) error {
			return e.gh.AddEnvironmentBranch(ctx, owner, repo, name, branch)
		}); err != nil {
			return err
		}
	}
	for _, pattern := range branches {
		if pattern != branch {
			e.finding(fmt.Sprintf("the environment %s of %s/%s admits %s beside %s: a workflow on a branch that name matches is handed the infrastructure app's key; remove it (the repository's settings, Environments)", name, owner, repo, pattern, branch))
		}
	}
	for _, pattern := range tags {
		e.finding(fmt.Sprintf("the environment %s of %s/%s admits the tags named %s: a workflow on a tag that name matches is handed the infrastructure app's key; remove it (the repository's settings, Environments)", name, owner, repo, pattern))
	}

	return e.infrastructureKey(ctx, env != nil)
}

// infrastructureKey seals the infrastructure app's key into the environment's secret when
// the run holds it, and says so when the secret is missing and the run holds none.
func (e *engine) infrastructureKey(ctx context.Context, stands bool) error {
	owner, repo, name := e.p.GithubOrganization, e.p.SourceRepo, org.RootEnvironment
	if key := e.req.SealKeys[org.InfrastructureApp]; len(key) > 0 {
		return e.change(ctx, sectionGitHub, fmt.Sprintf("seals the infrastructure app's key into the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, name, owner, repo), func(ctx context.Context) error {
			return e.gh.PutEnvironmentSecret(ctx, owner, repo, name, org.InfrastructureKeySecret, key)
		})
	}
	missing := !stands
	if stands {
		_, err := e.gh.EnvironmentSecret(ctx, owner, repo, name, org.InfrastructureKeySecret)
		if err != nil && !github.NotFound(err) {
			return errors.Wrapf(err, "reading the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, name, owner, repo)
		}
		missing = err != nil
	}
	if missing {
		e.finding(fmt.Sprintf("the environment %s of %s/%s holds no secret %s, and the repository's workflow acts as the infrastructure app with the key it reads there: generate a private key on the app's settings page and seal it with bedrock secret add %s --from-file <the file>", name, owner, repo, org.InfrastructureKeySecret, org.InfrastructureKey))
	}

	return nil
}

// listedApps holds each app the placement calls for to its declaration without its key,
// which is a GitHub secret no run reads back: the organization's own list of the apps
// installed on it says which are and on which repositories, and each app's record, by its
// slug, says who owns it and what it holds.
func (e *engine) listedApps(ctx context.Context) error {
	owner := e.p.GithubOrganization
	installed, err := e.gh.OrgInstallations(ctx, owner)
	if err != nil {
		e.note(fmt.Sprintf("the GitHub Apps were not checked: the apps installed on %s could not be listed (%v)", owner, errors.Cause(err)))

		return nil
	}
	for _, m := range e.p.AppManifests() {
		id, err := e.appID(m.Role)
		if err != nil {
			return err
		}
		if id == 0 {
			e.finding(fmt.Sprintf("placement.json records no App ID for %s, which this organization calls for: bedrock org bootstrap makes the app and records it", m.Role.Title()))

			continue
		}
		i := slices.IndexFunc(installed, func(in github.OrgInstallation) bool { return in.AppID == id })
		if i < 0 {
			e.finding(fmt.Sprintf("%s (App %d) is not installed on the %s organization: install it there, for all repositories", m.Role.Title(), id, owner))

			continue
		}
		app, err := e.gh.AppBySlug(ctx, installed[i].AppSlug)
		if err != nil {
			e.note(fmt.Sprintf("%s is not checked: its record (%s) could not be read (%v)", m.Role.Title(), installed[i].AppSlug, errors.Cause(err)))

			continue
		}
		installation := github.Installation{ID: installed[i].ID, AppID: id, Account: github.Account{Login: owner}, RepositorySelection: installed[i].RepositorySelection}
		for _, f := range m.Verify(app, []github.Installation{installation}, owner) {
			e.finding(f)
		}
	}

	return nil
}

// libraries brings each library to its entry, then what the libraries share: the
// organization's rule on their version tags, the release app's key where their release
// workflows read it, and the variable that names the release app's login.
func (e *engine) libraries(ctx context.Context) error {
	if e.gh == nil {
		if len(e.p.Libraries) > 0 {
			e.note("the libraries were not checked: no token (GITHUB_TOKEN, or the infrastructure app's in a trigger's run)")
		}

		return nil
	}
	for i := range e.p.Libraries {
		if err := e.library(ctx, &e.p.Libraries[i]); err != nil {
			return err
		}
	}
	if err := e.tagRuleset(ctx); err != nil {
		return err
	}
	if err := e.releaseSecrets(ctx); err != nil {
		return err
	}

	return e.releaseLogin(ctx)
}

// library brings one library to its entry: the repository made when it is missing, its
// visibility, its merge settings, the ruleset on its default branch, and the comparison
// of the checks the entry requires with the ones its latest pull request reported.
func (e *engine) library(ctx context.Context, l *org.Library) error {
	owner := e.p.GithubOrganization
	full := owner + "/" + l.Repository
	repo, err := e.gh.Repository(ctx, owner, l.Repository)
	switch {
	case github.NotFound(err):
		if l.Adopted {
			e.finding(fmt.Sprintf("%s does not exist, and its entry says it stood before the placement listed it (adopted): transfer the repository into the organization, or take adopted off the entry and the apply creates it", full))

			return nil
		}
		if repo, err = e.createLibrary(ctx, l); err != nil || repo == nil {
			return err
		}
	case err != nil:
		return errors.Wrapf(err, "reading %s", full)
	}
	if e.libraryIDs == nil {
		e.libraryIDs = map[string]int64{}
	}
	e.libraryIDs[l.Repository] = repo.ID
	if repo.Name != "" && repo.Name != l.Repository {
		// GitHub finds a repository whatever the case of the name asked for, and a
		// ruleset's and a secret's lists hold the name as GitHub spells it.
		e.finding(fmt.Sprintf("placement.json lists the library %s, and GitHub spells the repository %s/%s: spell the entry as GitHub does", l.Repository, owner, repo.Name))
	}
	if repo.Archived {
		e.finding(fmt.Sprintf("%s is archived, and placement.json lists it as a library: unarchive it on GitHub, or take it off the list", full))

		return nil
	}
	if repo.Private == l.Public {
		if err := e.change(ctx, sectionLibraries, "makes "+full+" "+visibility(l.Public)+" (it is "+visibility(!repo.Private)+")", func(ctx context.Context) error {
			return e.gh.SetRepositoryPrivate(ctx, owner, l.Repository, !l.Public)
		}); err != nil {
			return err
		}
	}
	if err := e.repositorySettings(ctx, sectionLibraries, owner, l.Repository); err != nil {
		return err
	}
	if err := e.libraryRuleset(ctx, l); err != nil {
		return err
	}

	return e.reportedChecks(ctx, l, repo.DefaultBranch)
}

// visibility names a repository's visibility in a word.
func visibility(public bool) string {
	if public {
		return "public"
	}

	return "private"
}

// createLibrary makes the library's repository with an initial commit on the default
// branch the placement names, as the root organization's own repository is made. In an
// apply it answers the repository; in a plan the rest of what the apply does for it is
// listed and nil is answered, since nothing stands to compare.
func (e *engine) createLibrary(ctx context.Context, l *org.Library) (*github.Repository, error) {
	owner := e.p.GithubOrganization
	full := owner + "/" + l.Repository
	if err := e.change(ctx, sectionLibraries, fmt.Sprintf("creates the %s repository %s, with an initial commit on %s", visibility(l.Public), full, e.p.GithubDefaultBranch), func(ctx context.Context) error {
		if _, err := e.gh.CreateRepository(ctx, owner, github.RepositoryRequest{Name: l.Repository, Private: !l.Public, AutoInit: true}); err != nil {
			return err
		}

		return create.DefaultBranch(ctx, e.gh, owner, l.Repository, e.p.GithubDefaultBranch, e.sleep, e.out)
	}); err != nil {
		return nil, err
	}
	if e.req.Mode != Apply {
		for _, what := range []string{"sets the merge settings of " + full + ": " + mergeSettings, e.rulesLibrary(l)} {
			if err := e.change(ctx, sectionLibraries, what, nil); err != nil {
				return nil, err
			}
		}

		return nil, nil
	}
	repo, err := e.gh.Repository(ctx, owner, l.Repository)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s", full)
	}
	// GitHub's read of the default branch lags the rename just made by a moment; the
	// branch is the one the placement names, since the rename to it succeeded.
	repo.DefaultBranch = e.p.GithubDefaultBranch

	return repo, nil
}

// rulesLibrary is the sentence for the making of a library's ruleset.
func (e *engine) rulesLibrary(l *org.Library) string {
	checks := "no check required (the entry names none)"
	if len(l.Checks) > 0 {
		checks = "the checks " + strings.Join(l.Checks, "; ") + " required"
	}

	return fmt.Sprintf("rules the default branch of %s/%s: pull requests alone with %d approving review(s), squash merges, %s, the infrastructure app bypassing", e.p.GithubOrganization, l.Repository, e.p.ApprovingReviewCount(), checks)
}

// actionsAppID is the id of GitHub Actions' own app, which reports every check a
// library's ruleset requires.
func (e *engine) actionsAppID(ctx context.Context) (int64, error) {
	if e.actionsApp == 0 {
		app, err := e.gh.AppBySlug(ctx, actionsAppSlug)
		if err != nil {
			return 0, errors.Wrapf(err, "reading GitHub Actions' app (%s)", actionsAppSlug)
		}
		e.actionsApp = app.ID
	}

	return e.actionsApp, nil
}

// libraryRuleset makes the ruleset on the library's default branch or brings it to the
// entry, and reports every other ruleset in force on the repository's branches: its rules
// hold too, and for a repository that came with one (adopted) whose entry names no checks
// yet, the checks it requires are printed, which become the entry's first list.
func (e *engine) libraryRuleset(ctx context.Context, l *org.Library) error {
	owner := e.p.GithubOrganization
	full := owner + "/" + l.Repository
	actions, err := e.actionsAppID(ctx)
	if err != nil {
		return err
	}
	infrastructure, err := e.appID(org.InfrastructureApp)
	if err != nil {
		return err
	}
	if err := e.bringRuleset(ctx, sectionLibraries, owner, l.Repository, e.d.LibraryRuleset(l, actions, infrastructure), e.rulesLibrary(l)); err != nil {
		return err
	}
	listed, err := e.gh.Rulesets(ctx, owner, l.Repository)
	if err != nil {
		return errors.Wrapf(err, "listing the rulesets of %s", full)
	}
	for _, other := range listed {
		if other.Name == rulesetName || other.Target != github.RulesetTargetBranch || other.Enforcement != github.RulesetActive {
			continue
		}
		e.finding(fmt.Sprintf("%s carries the ruleset %q beside bedrock's own (%s), and its rules hold too: once the entry's checks cover what it requires, delete it on GitHub (the repository's settings, Rules)", full, other.Name, rulesetName))
		if !l.Adopted || len(l.Checks) > 0 {
			continue
		}
		whole, err := e.gh.Ruleset(ctx, owner, l.Repository, other.ID)
		if err != nil {
			return errors.Wrapf(err, "reading the ruleset %s of %s", other.Name, full)
		}
		if required := requiredChecks(whole); len(required) > 0 {
			e.note(fmt.Sprintf("%s was adopted and its entry names no checks: the ruleset %q it came with requires %s; list them as the entry's checks", full, other.Name, quoted(required)))
		}
	}

	return nil
}

// requiredChecks are the names of the checks a ruleset requires.
func requiredChecks(rs *github.Ruleset) []string {
	var names []string
	for _, rule := range rs.Rules {
		if rule.Type != ruleRequiredChecks {
			continue
		}
		checks, _ := rule.Parameters["required_status_checks"].([]any)
		for _, check := range checks {
			fields, _ := check.(map[string]any)
			if name, ok := fields["context"].(string); ok {
				names = append(names, name)
			}
		}
	}

	return names
}

// quoted spells a list of names in a sentence, each in quotation marks.
func quoted(names []string) string {
	q := make([]string, 0, len(names))
	for _, name := range names {
		q = append(q, fmt.Sprintf("%q", name))
	}

	return joined(q)
}

// reportedChecks compares the checks the entry requires with the ones GitHub Actions
// reported on the head of the latest pull request into the library's default branch. A
// required check that pull request did not report is a finding: no workflow of the
// repository reports under that name, and every pull request waits for it. The checks it
// reported that the entry does not require are a note for the reviewer, who alone can
// tell a helper job from a test nobody listed.
func (e *engine) reportedChecks(ctx context.Context, l *org.Library, branch string) error {
	owner := e.p.GithubOrganization
	full := owner + "/" + l.Repository
	pr, err := e.gh.LatestPullRequest(ctx, owner, l.Repository, branch)
	if err != nil {
		return errors.Wrapf(err, "reading the latest pull request into %s of %s", branch, full)
	}
	if pr == nil {
		if len(l.Checks) > 0 {
			e.note(fmt.Sprintf("%s has no pull request into %s yet: the checks its entry requires are compared with the ones a pull request reports once there is one", full, branch))
		}

		return nil
	}
	runs, err := e.gh.CheckRuns(ctx, owner, l.Repository, pr.Head.SHA)
	if err != nil {
		return errors.Wrapf(err, "reading the checks of pull request %d of %s", pr.Number, full)
	}
	actions, err := e.actionsAppID(ctx)
	if err != nil {
		return err
	}
	var reported []string
	for _, run := range runs {
		if run.App.ID == actions && !slices.Contains(reported, run.Name) {
			reported = append(reported, run.Name)
		}
	}
	for _, check := range l.Checks {
		if !slices.Contains(reported, check) {
			e.finding(fmt.Sprintf("%s requires %q; pull request %d reported no such check; every pull request will wait for it", full, check, pr.Number))
		}
	}
	var extra []string
	for _, name := range reported {
		if !slices.Contains(l.Checks, name) {
			extra = append(extra, name)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		e.note(fmt.Sprintf("%s: pull request %d reported checks its entry does not require: %s", full, pr.Number, quoted(extra)))
	}

	return nil
}

// tagRuleset keeps the organization's rule on the libraries' version tags: made with the
// first library, brought to the libraries listed, removed with the last.
func (e *engine) tagRuleset(ctx context.Context) error {
	owner := e.p.GithubOrganization
	listed, err := e.gh.OrgRulesets(ctx, owner)
	if err != nil {
		return errors.Wrapf(err, "listing the rulesets of the %s organization", owner)
	}
	i := slices.IndexFunc(listed, func(rs github.Ruleset) bool { return rs.Name == tagRulesetName })
	if len(e.p.Libraries) == 0 {
		if i < 0 {
			return nil
		}

		return e.change(ctx, sectionLibraries, fmt.Sprintf("removes the %s organization's ruleset %q: placement.json lists no library", owner, tagRulesetName), func(ctx context.Context) error {
			return e.gh.DeleteOrgRuleset(ctx, owner, listed[i].ID)
		})
	}
	release, err := e.appID(org.ReleaseApp)
	if err != nil {
		return err
	}
	if release == 0 {
		e.note("the libraries' version tags are not ruled yet: placement.json records no release app, the one actor the rule lets make a release's tag")

		return nil
	}
	want := e.d.TagRuleset(release)
	if i < 0 {
		return e.change(ctx, sectionLibraries, fmt.Sprintf("rules the version tags of %s in the %s organization (ruleset %q): the release app alone creates, moves or deletes a tag named v* or */v*", joined(e.p.LibraryNames()), owner, tagRulesetName), func(ctx context.Context) error {
			_, err := e.gh.CreateOrgRuleset(ctx, owner, want)

			return err
		})
	}
	got, err := e.gh.OrgRuleset(ctx, owner, listed[i].ID)
	if err != nil {
		return errors.Wrapf(err, "reading the ruleset %s of the %s organization", tagRulesetName, owner)
	}
	want.ID = got.ID
	if diff := rulesetDiff(got, want, "tags and repositories"); diff != "" {
		return e.change(ctx, sectionLibraries, fmt.Sprintf("brings the %s organization's ruleset %q to the placement: %s", owner, tagRulesetName, diff), func(ctx context.Context) error {
			return e.gh.UpdateOrgRuleset(ctx, owner, got.ID, want)
		})
	}

	return nil
}

// releaseLogin keeps the organization variable that names the release app's login.
func (e *engine) releaseLogin(ctx context.Context) error {
	want := e.d.ReleaseLoginVariable()
	if want == nil {
		return nil
	}
	owner := e.p.GithubOrganization
	got, err := e.gh.OrgVariable(ctx, owner, want.Name)
	switch {
	case github.NotFound(err):
		return e.change(ctx, sectionLibraries, "sets the organization variable "+want.Name+" to "+want.Value+", the author a library's release workflow accepts a release from", func(ctx context.Context) error {
			return e.gh.CreateOrgVariable(ctx, owner, *want)
		})
	case err != nil:
		return errors.Wrapf(err, "reading the %s variable of %s", want.Name, owner)
	case got.Value != want.Value || got.Visibility != want.Visibility:
		return e.change(ctx, sectionLibraries, "sets the organization variable "+want.Name+" to "+want.Value+" (was "+got.Value+")", func(ctx context.Context) error {
			return e.gh.UpdateOrgVariable(ctx, owner, *want)
		})
	}

	return nil
}

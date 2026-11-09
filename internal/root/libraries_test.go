package root_test

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// The App IDs in the tests of a root organization's libraries: GitHub Actions' own app,
// which reports the plan of a root organization without a cloud and every check of a
// library, and the organization's two apps as libraryPlacement records them.
const (
	actionsAppID        = 15368
	infrastructureAppID = 6001
	releaseAppID        = 6002
)

// What the sentences of these tests repeat: the root repository of libraryPlacement by its
// full name, and the merge settings as a change names them.
const (
	libraryRoot   = "imf-example/imf-root-infrastructure"
	mergeSentence = "squash alone, titled and described from the pull request, the branch deleted on merge, the branch update offered"
)

// libraryPlacement is a root organization without a cloud: the GitHub organization, its
// two apps and three libraries (two public ones with the checks they require, a private
// one with none), and nothing of Google's.
func libraryPlacement(t *testing.T) *org.Placement {
	t.Helper()

	p := &org.Placement{
		Kind: org.RootKind, Prefix: "imf", GithubOrganization: "imf-example", GithubDefaultBranch: "master", SourceRepo: "imf-root-infrastructure",
		GithubInfrastructureAppID: strconv.Itoa(infrastructureAppID), GithubReleaseAppID: strconv.Itoa(releaseAppID), GithubReleaseAppSlug: "imf-example-release",
		Libraries: []org.Library{
			{Repository: "workflows", Public: true, Checks: []string{"actionlint", "semantic-titles / Validate PR title"}},
			{Repository: "impulse", Public: true, Checks: []string{"golang-ci / Run tests and builds", "golang-ci / Run tests and builds (dev)"}},
			{Repository: "notes"},
		},
	}
	if err := p.Validate(); err != nil {
		t.Fatalf("the placement of the root organization without a cloud: Validate() error = %v", err)
	}
	if p.HasCloud() {
		t.Fatal("the placement names no organizationId and HasCloud() = true")
	}

	return p
}

// githubWorld is a root organization without a cloud as its bootstrap leaves it before
// the engine's first run, on GitHub's stand-in alone: the root repository with its
// initial commit and nothing else, GitHub Actions' own app, and the apps the placement
// calls for, made and installed on the organization for all repositories. Nothing of
// Google's is wired.
type githubWorld struct {
	p       *org.Placement
	srv     *githubtest.Server
	clients *root.Clients
	// keys are the apps' private keys, which the bootstrap holds from the apps' making
	// and hands the engine to seal.
	keys map[org.AppRole][]byte
}

func newGitHubWorld(t *testing.T, p *org.Placement) *githubWorld {
	t.Helper()

	srv := githubtest.New(t)
	owner := p.GithubOrganization
	srv.AddRepo(owner, p.SourceRepo, &githubtest.Repo{Private: true, Refs: map[string]github.Object{"refs/heads/" + p.GithubDefaultBranch: {Type: "commit", SHA: "initial"}}})
	srv.PublicApps["github-actions"] = github.App{ID: actionsAppID, Slug: "github-actions", Name: "GitHub Actions", Owner: github.Account{Login: "github", Type: "Organization"}}
	keys := map[org.AppRole][]byte{}
	for _, m := range p.AppManifests() {
		id, err := strconv.ParseInt(p.AppID(m.Role), 10, 64)
		if err != nil {
			t.Fatalf("%s is App %q: %v", m.Role.Title(), p.AppID(m.Role), err)
		}
		srv.PublicApps[m.Name] = github.App{
			ID: id, Slug: m.Name, Name: m.Name, ClientID: clientID(m.Role),
			Owner: github.Account{Login: owner, Type: "Organization"}, Permissions: maps.Clone(m.Permissions),
		}
		srv.OrgInstallations[owner] = append(srv.OrgInstallations[owner], github.OrgInstallation{ID: id + 1, AppID: id, AppSlug: m.Name, RepositorySelection: github.AllRepositories})
		keys[m.Role] = []byte("the private key of " + m.Name + ", as GitHub gave it once at the app's making")
	}
	clients := &root.Clients{
		GitHub: func(token string) *github.Client { return github.New(srv.URL, token) },
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}

	return &githubWorld{p: p, srv: srv, clients: clients, keys: keys}
}

// clientID is the client ID the stand-in's record of the app carries.
func clientID(role org.AppRole) string {
	return "Iv23li-" + string(role)
}

// bootstrapped is the organization after the bootstrap's apply, which made everything
// the placement lists and sealed both keys: what a run of the repository's workflow
// starts from.
func bootstrapped(t *testing.T, p *org.Placement) *githubWorld {
	t.Helper()

	w := newGitHubWorld(t, p)
	if report, out := w.run(t, root.Apply, w.keys); len(report.Findings) != 0 || report.Applied != len(report.Changes) {
		t.Fatalf("the bootstrap's apply made %d of %d changes, with the findings %q:\n%s", report.Applied, len(report.Changes), report.Findings, out)
	}

	return w
}

// run runs the engine in the mode with the keys the run holds, none for a run of the
// repository's workflow, and fails the test on an error.
func (w *githubWorld) run(t *testing.T, mode root.Mode, keys map[org.AppRole][]byte) (report *root.Report, printed string) {
	t.Helper()

	var out bytes.Buffer
	report, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Mode: mode, Commit: "abc1234", GitHubToken: "test-token", SealKeys: keys}, &out)
	if err != nil {
		t.Fatalf("Run(%v) error = %v\n%s", mode, err, out.String())
	}

	return report, out.String()
}

// clean fails the test unless a check finds nothing to change and nothing to report.
func (w *githubWorld) clean(t *testing.T) {
	t.Helper()

	if report, out := w.run(t, root.Check, nil); !report.Clean() {
		t.Errorf("the check is not clean:\n%s\nchanges: %q\nfindings: %q", out, whats(report), report.Findings)
	}
}

// repo is a repository of the organization as the stand-in holds it; one it lacks fails
// the test.
func (w *githubWorld) repo(t *testing.T, name string) *githubtest.Repo {
	t.Helper()

	repo, ok := w.srv.Repos[w.p.GithubOrganization+"/"+name]
	if !ok {
		t.Fatalf("the stand-in holds no repository %s/%s", w.p.GithubOrganization, name)
	}

	return repo
}

// libraryIDs are the ids of the repositories named, in order, as an organization
// secret's selection lists the libraries.
func (w *githubWorld) libraryIDs(t *testing.T, names ...string) []int64 {
	t.Helper()

	ids := make([]int64, 0, len(names))
	for _, name := range names {
		ids = append(ids, w.repo(t, name).ID)
	}
	slices.Sort(ids)

	return ids
}

// orgSecret is the organization's secret as the stand-in holds it; one it lacks fails the
// test.
func (w *githubWorld) orgSecret(t *testing.T, name string) *githubtest.OrgSecret {
	t.Helper()

	secret, ok := w.srv.OrgSecrets[w.p.GithubOrganization][name]
	if !ok {
		t.Fatalf("the stand-in holds no organization secret %s", name)
	}

	return secret
}

// state is everything the stand-in holds that a run can write, as JSON, so that what
// stood before a run compares with what stands after it.
func (w *githubWorld) state(t *testing.T) string {
	t.Helper()

	var (
		data []byte
		err  error
	)
	w.srv.Update(func() {
		data, err = json.MarshalIndent(map[string]any{
			"repositories": w.srv.Repos, "created": w.srv.Created, "commits": w.srv.Messages, "variables": w.srv.Variables,
			"runnerGroups": w.srv.RunnerGroups, "hostedRunners": w.srv.HostedRunners, "secrets": w.srv.OrgSecrets, "rulesets": w.srv.OrgRulesets,
		}, "", "  ")
	})
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}

	return string(data)
}

// refusing are the world's clients with a proxy in front of GitHub's stand-in: a call the
// map names, by its method and path, is answered with the status, as GitHub refuses a
// token that lacks the permission, and every other call goes through to the stand-in.
func (w *githubWorld) refusing(t *testing.T, refused map[string]int) *root.Clients {
	t.Helper()

	target, err := url.Parse(w.srv.URL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	forward := httputil.NewSingleHostReverseProxy(target)
	proxy := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		status, ok := refused[r.Method+" "+r.URL.Path]
		if !ok {
			forward.ServeHTTP(rw, r)

			return
		}
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	t.Cleanup(proxy.Close)

	return &root.Clients{GitHub: func(token string) *github.Client { return github.New(proxy.URL, token) }, Sleep: w.clients.Sleep}
}

// wantSettings are the merge settings the engine gives the root repository and every
// library: squash the only merge, titled and described from the pull request, the head
// branch deleted on merge, the update of a branch offered.
func wantSettings() github.RepositorySettings {
	no, yes := false, true
	title, message := "PR_TITLE", "PR_BODY"

	return github.RepositorySettings{
		AllowMergeCommit: &no, AllowRebaseMerge: &no, AllowSquashMerge: &yes,
		SquashMergeCommitTitle: &title, SquashMergeCommitMessage: &message,
		DeleteBranchOnMerge: &yes, AllowUpdateBranch: &yes,
	}
}

// wantBranchRuleset is the ruleset the engine writes on a default branch, as the stand-in
// holds it: pull requests alone with one approving review, squash merges, no force push
// and no deletion, the infrastructure app bypassing, and the checks named required, each
// reported by GitHub Actions' app; no rule of required checks when none is named.
func wantBranchRuleset(checks ...string) *github.Ruleset {
	rs := &github.Ruleset{
		Name: "default branch", Target: "branch", Enforcement: "active",
		BypassActors: []github.BypassActor{{ActorID: infrastructureAppID, ActorType: "Integration", BypassMode: "always"}},
		Conditions:   &github.RulesetConditions{RefName: github.RefNameCondition{Include: []string{"~DEFAULT_BRANCH"}, Exclude: []string{}}},
		Rules: []github.Rule{
			{Type: "deletion"},
			{Type: "non_fast_forward"},
			{Type: "pull_request", Parameters: map[string]any{
				"required_approving_review_count": float64(1), "dismiss_stale_reviews_on_push": true, "require_code_owner_review": false,
				"require_last_push_approval": true, "required_review_thread_resolution": false, "allowed_merge_methods": []any{"squash"},
			}},
		},
	}
	if len(checks) == 0 {
		return rs
	}
	required := make([]any, 0, len(checks))
	for _, check := range checks {
		required = append(required, map[string]any{"context": check, "integration_id": float64(actionsAppID)})
	}
	rs.Rules = append(rs.Rules, github.Rule{Type: "required_status_checks", Parameters: map[string]any{
		"strict_required_status_checks_policy": true, "do_not_enforce_on_create": false, "required_status_checks": required,
	}})

	return rs
}

// wantTagRuleset is the organization's ruleset on the libraries' version tags, as the
// stand-in holds it: the two patterns a release's tag matches, in the repositories named,
// which no one creates, moves or deletes but the release app.
func wantTagRuleset(libraries ...string) *github.Ruleset {
	return &github.Ruleset{
		Name: "version tags", Target: "tag", Enforcement: "active",
		BypassActors: []github.BypassActor{{ActorID: releaseAppID, ActorType: "Integration", BypassMode: "always"}},
		Conditions: &github.RulesetConditions{
			RefName:        github.RefNameCondition{Include: []string{"refs/tags/v*", "refs/tags/*/v*"}, Exclude: []string{}},
			RepositoryName: &github.RepositoryNameCondition{Include: libraries, Exclude: []string{}},
		},
		Rules: []github.Rule{{Type: "creation"}, {Type: "update"}, {Type: "deletion"}, {Type: "non_fast_forward"}},
	}
}

// rulesetNamed is the ruleset of the list with the name, without its id, nil when the
// list holds none.
func rulesetNamed(list []*github.Ruleset, name string) *github.Ruleset {
	for _, rs := range list {
		if rs.Name == name {
			found := *rs
			found.ID = 0

			return &found
		}
	}

	return nil
}

// mentioning are the sentences of the list that carry the words.
func mentioning(list []string, words string) []string {
	var found []string
	for _, sentence := range list {
		if strings.Contains(sentence, words) {
			found = append(found, sentence)
		}
	}

	return found
}

// sameSentences fails the test when the sentences are not the ones wanted, in order; none
// wanted and none got are the same.
func sameSentences(t *testing.T, what string, got, want []string) {
	t.Helper()

	if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("%s (-want +got):\n%s", what, diff)
	}
}

// fromNothing are the changes a run lists against an organization where nothing stands
// beyond the root repository, when it holds both keys: the root repository's settings,
// rule, environment and key, each library with its settings and rule, and what the
// libraries share. An apply admits the default branch to the environment it just made as
// a change of its own, which a plan's sentence for the environment's making covers.
func fromNothing(apply bool) []string {
	changes := []string{
		"GitHub: sets the merge settings of " + libraryRoot + ": " + mergeSentence,
		"GitHub: rules the default branch of " + libraryRoot + ": pull requests alone with 1 approving review(s), squash merges, the workflow's plan (bedrock plan) required, the infrastructure app bypassing",
		"GitHub: makes the environment bedrock of " + libraryRoot + ", which the workflow's jobs run in and which admits the branch master alone",
	}
	if apply {
		changes = append(changes, "GitHub: admits the branch master to the environment bedrock of "+libraryRoot)
	}

	return append(changes,
		"GitHub: seals the infrastructure app's key into the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of "+libraryRoot,
		"libraries: creates the public repository imf-example/workflows, with an initial commit on master",
		"libraries: sets the merge settings of imf-example/workflows: "+mergeSentence,
		"libraries: rules the default branch of imf-example/workflows: pull requests alone with 1 approving review(s), squash merges, the checks actionlint; semantic-titles / Validate PR title required, the infrastructure app bypassing",
		"libraries: creates the public repository imf-example/impulse, with an initial commit on master",
		"libraries: sets the merge settings of imf-example/impulse: "+mergeSentence,
		"libraries: rules the default branch of imf-example/impulse: pull requests alone with 1 approving review(s), squash merges, the checks golang-ci / Run tests and builds; golang-ci / Run tests and builds (dev) required, the infrastructure app bypassing",
		"libraries: creates the private repository imf-example/notes, with an initial commit on master",
		"libraries: sets the merge settings of imf-example/notes: "+mergeSentence,
		"libraries: rules the default branch of imf-example/notes: pull requests alone with 1 approving review(s), squash merges, no check required (the entry names none), the infrastructure app bypassing",
		`libraries: rules the version tags of workflows, impulse, notes in the imf-example organization (ruleset "version tags"): the release app alone creates, moves or deletes a tag named v* or */v*`,
		"libraries: seals the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (workflows, impulse, notes)",
		"libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read",
		"libraries: sets the organization variable RELEASE_APP_LOGIN to imf-example-release[bot], the author a library's release workflow accepts a release from",
	)
}

// TestEngineWithoutCloudNeedsToken: a root organization without a cloud is kept on GitHub
// alone, so a run with no token, or with no GitHub client wired, is refused in a sentence
// that says so, before anything is read.
func TestEngineWithoutCloudNeedsToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		clients func(w *githubWorld) *root.Clients
		token   string
	}{
		{name: "no token", clients: func(w *githubWorld) *root.Clients { return w.clients }},
		{name: "no GitHub client wired", clients: func(*githubWorld) *root.Clients { return &root.Clients{} }, token: "test-token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newGitHubWorld(t, libraryPlacement(t))
			report, err := root.Run(context.Background(), tt.clients(w), &root.Request{Placement: w.p, Mode: root.Check, GitHubToken: tt.token}, &bytes.Buffer{})
			want := "no GitHub token: the imf organization has no cloud and is kept on GitHub alone, as its infrastructure app (in its repository's workflow) or as a person who owns the GitHub organization (GITHUB_TOKEN)"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("Run() error = %v, want it to say %q", err, want)
			}
			if report != nil {
				t.Errorf("Run() answered a report beside the refusal: %+v", report)
			}
			if len(w.srv.Calls) != 0 {
				t.Errorf("the refused run called GitHub: %v", w.srv.Calls)
			}
		})
	}
}

// TestEngineWithoutCloudBootstrapApply: the bootstrap's apply, which holds both apps'
// keys, makes a root organization without a cloud from its root repository's initial
// commit on GitHub alone. Afterwards the root repository carries the merge settings, the
// ruleset that requires the workflow's plan as GitHub Actions reports it with the
// infrastructure app bypassing, and the environment that admits the default branch alone
// with the infrastructure app's key sealed in it; each library stands, public or private
// as its entry says, on the placement's default branch, with the merge settings and a
// ruleset that requires exactly its checks; the organization rules the libraries' version
// tags for the release app alone; and the release app's key and client ID are secrets the
// libraries alone read, beside the variable that names its login. The workflow's check
// after it, which holds no key, finds nothing to change and nothing to report.
func TestEngineWithoutCloudBootstrapApply(t *testing.T) {
	t.Parallel()

	w := newGitHubWorld(t, libraryPlacement(t))
	owner := w.p.GithubOrganization
	report, out := w.run(t, root.Apply, w.keys)
	sameSentences(t, "the changes of the bootstrap's apply", whats(report), fromNothing(true))
	if report.Applied != len(report.Changes) || len(report.Findings) != 0 {
		t.Errorf("the apply made %d of %d changes, with the findings %q:\n%s", report.Applied, len(report.Changes), report.Findings, out)
	}

	// The root repository: its settings, its rule and its environment.
	rootRepo := w.repo(t, w.p.SourceRepo)
	if diff := cmp.Diff(wantSettings(), rootRepo.Settings); diff != "" {
		t.Errorf("the merge settings of the root repository (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantBranchRuleset("bedrock plan"), rulesetNamed(rootRepo.Rulesets, "default branch")); diff != "" || len(rootRepo.Rulesets) != 1 {
		t.Errorf("the root repository carries %d ruleset(s); its ruleset on the default branch (-want +got):\n%s", len(rootRepo.Rulesets), diff)
	}
	env := rootRepo.Environments["bedrock"]
	if env == nil || len(rootRepo.Environments) != 1 {
		t.Fatalf("the root repository's environments = %v, want bedrock alone", slices.Collect(maps.Keys(rootRepo.Environments)))
	}
	if !env.Custom || !slices.Equal(env.Branches, []string{"master"}) {
		t.Errorf("the environment bedrock keeps its own branch patterns: %t, and admits %q; want its own patterns, master alone", env.Custom, env.Branches)
	}
	if secret := env.Secrets["BEDROCK_INFRASTRUCTURE_APP_KEY"]; secret == nil || len(env.Secrets) != 1 || !bytes.Equal(secret.Value, w.keys[org.InfrastructureApp]) {
		t.Errorf("the environment's secrets = %v, want BEDROCK_INFRASTRUCTURE_APP_KEY alone, holding the infrastructure app's key", slices.Collect(maps.Keys(env.Secrets)))
	}

	// Each library: made as its entry says, on the placement's default branch, which the
	// stand-in's organization does not name a new repository's branch after.
	for _, l := range w.p.Libraries {
		repo := w.repo(t, l.Repository)
		if repo.Private == l.Public {
			t.Errorf("%s is private: %t, and its entry says public: %t", l.Repository, repo.Private, l.Public)
		}
		if refs := slices.Sorted(maps.Keys(repo.Refs)); !slices.Equal(refs, []string{"refs/heads/master"}) {
			t.Errorf("%s has the refs %q, want its initial commit on master alone", l.Repository, refs)
		}
		if diff := cmp.Diff(wantSettings(), repo.Settings); diff != "" {
			t.Errorf("the merge settings of %s (-want +got):\n%s", l.Repository, diff)
		}
		// An entry that names no check has no rule of required checks at all.
		if diff := cmp.Diff(wantBranchRuleset(l.Checks...), rulesetNamed(repo.Rulesets, "default branch")); diff != "" || len(repo.Rulesets) != 1 {
			t.Errorf("%s carries %d ruleset(s); its ruleset on the default branch (-want +got):\n%s", l.Repository, len(repo.Rulesets), diff)
		}
		if len(repo.Environments) != 0 {
			t.Errorf("%s has the environments %v; the engine makes none in a library", l.Repository, slices.Collect(maps.Keys(repo.Environments)))
		}
	}
	sameSentences(t, "the repositories made", w.srv.Created, []string{"imf-example/workflows", "imf-example/impulse", "imf-example/notes"})
	if want := "Renamed the branch main of imf-example/notes to master, the default branch placement.json names.\n"; !strings.Contains(out, want) {
		t.Errorf("the apply printed:\n%s\nwant %q", out, want)
	}

	// What the libraries share: the rule on their version tags, the release app's two
	// secrets, and the variable that names its login.
	if diff := cmp.Diff(wantTagRuleset("workflows", "impulse", "notes"), rulesetNamed(w.srv.OrgRulesets[owner], "version tags")); diff != "" || len(w.srv.OrgRulesets[owner]) != 1 {
		t.Errorf("the organization carries %d ruleset(s); its ruleset on version tags (-want +got):\n%s", len(w.srv.OrgRulesets[owner]), diff)
	}
	ids := w.libraryIDs(t, "workflows", "impulse", "notes")
	key := w.orgSecret(t, "RELEASE_PLEASE_APP_PEM")
	if key.Visibility != "selected" || !slices.Equal(key.RepositoryIDs, ids) || !bytes.Equal(key.Value, w.keys[org.ReleaseApp]) {
		t.Errorf("RELEASE_PLEASE_APP_PEM: visibility %q, read by %v, holding the release app's key: %t; want selected, %v, true", key.Visibility, key.RepositoryIDs, bytes.Equal(key.Value, w.keys[org.ReleaseApp]), ids)
	}
	client := w.orgSecret(t, "RELEASE_PLEASE_APP_CLIENT_ID")
	if client.Visibility != "selected" || !slices.Equal(client.RepositoryIDs, ids) || string(client.Value) != clientID(org.ReleaseApp) {
		t.Errorf("RELEASE_PLEASE_APP_CLIENT_ID: visibility %q, read by %v, holding %q; want selected, %v, %q", client.Visibility, client.RepositoryIDs, client.Value, ids, clientID(org.ReleaseApp))
	}
	if len(w.srv.OrgSecrets[owner]) != 2 {
		t.Errorf("the organization's secrets = %v, want the release app's two alone", slices.Sorted(maps.Keys(w.srv.OrgSecrets[owner])))
	}
	wantVariables := map[string]github.Variable{"RELEASE_APP_LOGIN": {Name: "RELEASE_APP_LOGIN", Value: "imf-example-release[bot]", Visibility: "all"}}
	if diff := cmp.Diff(wantVariables, w.srv.Variables[owner]); diff != "" {
		t.Errorf("the organization's variables (-want +got):\n%s", diff)
	}

	// The bootstrap run again holds the keys again: a key handed is sealed whenever one
	// is, since no run can read what a secret holds, and nothing else changes.
	again, _ := w.run(t, root.Apply, w.keys)
	sameSentences(t, "the changes of the bootstrap's apply run again", whats(again), []string{
		"GitHub: seals the infrastructure app's key into the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of " + libraryRoot,
		"libraries: seals the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (workflows, impulse, notes)",
		"libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read",
	})

	// The workflow's check holds no key, and finds the organization as the placement says.
	check, out := w.run(t, root.Check, nil)
	if len(check.Changes) != 0 || len(check.Findings) != 0 || !check.Clean() {
		t.Errorf("the check after the apply is not clean:\n%s\nchanges: %q\nfindings: %q", out, whats(check), check.Findings)
	}
	sameSentences(t, "the notes of the check after the apply", check.Notes, []string{
		"imf-example/workflows has no pull request into master yet: the checks its entry requires are compared with the ones a pull request reports once there is one",
		"imf-example/impulse has no pull request into master yet: the checks its entry requires are compared with the ones a pull request reports once there is one",
	})
}

// TestEngineWithoutCloudPlanWritesNothing: a plan against an organization where nothing
// stands beyond the root repository lists every change the apply makes, in the apply's
// order, and writes nothing: every call it makes is a read, and the stand-in holds after
// it what it held before.
func TestEngineWithoutCloudPlanWritesNothing(t *testing.T) {
	t.Parallel()

	w := newGitHubWorld(t, libraryPlacement(t))
	before := w.state(t)
	report, out := w.run(t, root.Plan, w.keys)
	sameSentences(t, "the changes the plan lists", whats(report), fromNothing(false))
	if report.Applied != 0 || len(report.Findings) != 0 || len(report.Notes) != 0 {
		t.Errorf("the plan applied %d change(s), with the findings %q and the notes %q; want none of each", report.Applied, report.Findings, report.Notes)
	}
	if out != "" {
		t.Errorf("the plan printed as it ran, which an apply alone does:\n%s", out)
	}
	if diff := cmp.Diff(before, w.state(t)); diff != "" {
		t.Errorf("the plan changed what GitHub holds (-before +after):\n%s", diff)
	}
	if writes := slices.DeleteFunc(slices.Clone(w.srv.Calls), func(call string) bool { return strings.HasPrefix(call, "GET ") }); len(writes) != 0 || len(w.srv.Calls) == 0 {
		t.Errorf("of the plan's %d call(s), these are not reads: %q", len(w.srv.Calls), writes)
	}
	var buf bytes.Buffer
	report.Write(&buf, root.Plan)
	if want := "17 change(s) the apply would make; 0 finding(s).\n"; !strings.HasSuffix(buf.String(), want) {
		t.Errorf("Write() = %q, want it to end %q", buf.String(), want)
	}
}

// TestEngineWithoutCloudBeforeTheRootRepository: while the root repository does not
// exist, its rules and its environment wait with a note and nothing is said of the
// infrastructure app's key, which has no environment to be sealed into; the libraries are
// made all the same.
func TestEngineWithoutCloudBeforeTheRootRepository(t *testing.T) {
	t.Parallel()

	w := newGitHubWorld(t, libraryPlacement(t))
	delete(w.srv.Repos, libraryRoot)
	report, out := w.run(t, root.Apply, w.keys)
	sameSentences(t, "the notes about the root repository", mentioning(report.Notes, w.p.SourceRepo), []string{
		"the repository " + libraryRoot + " does not exist yet: the bootstrap makes it, and its rules wait",
	})
	if got := mentioning(whats(report), "GitHub: "); len(got) != 0 || len(report.Findings) != 0 {
		t.Errorf("without the root repository the run changes %q in it and finds %q; want neither:\n%s", got, report.Findings, out)
	}
	// The first five changes of an apply from nothing are the root repository's own.
	sameSentences(t, "the changes without the root repository", whats(report), fromNothing(true)[5:])
	if _, made := w.srv.Repos[libraryRoot]; made {
		t.Error("the engine made the root repository, which is the bootstrap's to make")
	}
}

// TestEngineRootEnvironmentKey: GitHub never gives a secret's value back, so a run that
// holds no key cannot seal the infrastructure app's: an environment without the secret,
// and an environment that is gone, are a finding that names the command a person seals a
// key with. A run handed a key seals it, and the check after it is clean.
func TestEngineRootEnvironmentKey(t *testing.T) {
	t.Parallel()

	finding := "the environment bedrock of " + libraryRoot + " holds no secret BEDROCK_INFRASTRUCTURE_APP_KEY, and the repository's workflow acts as the infrastructure app with the key it reads there: generate a private key on the app's settings page and seal it with bedrock secret add github-infrastructure-key --from-file <the file>"
	tests := []struct {
		name        string
		seed        func(repo *githubtest.Repo)
		wantChanges []string
	}{
		{name: "the secret is gone", seed: func(repo *githubtest.Repo) {
			delete(repo.Environments["bedrock"].Secrets, "BEDROCK_INFRASTRUCTURE_APP_KEY")
		}},
		{
			name: "the environment is gone", seed: func(repo *githubtest.Repo) { delete(repo.Environments, "bedrock") },
			wantChanges: []string{"GitHub: makes the environment bedrock of " + libraryRoot + ", which the workflow's jobs run in and which admits the branch master alone"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			rootRepo := w.repo(t, w.p.SourceRepo)
			tt.seed(rootRepo)
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the findings of a check that holds no key", report.Findings, []string{finding})
			sameSentences(t, "the changes of a check that holds no key", whats(report), tt.wantChanges)

			// The workflow's apply holds no key either: it makes the environment again and
			// still says the key is missing.
			report, _ = w.run(t, root.Apply, nil)
			sameSentences(t, "the findings of an apply that holds no key", report.Findings, []string{finding})
			env := rootRepo.Environments["bedrock"]
			if env == nil || !env.Custom || !slices.Equal(env.Branches, []string{"master"}) || len(env.Secrets) != 0 {
				t.Fatalf("after the apply that holds no key the environment = %+v, want one that admits master alone and holds no secret", env)
			}

			// bedrock secret add hands the run a key generated anew.
			anew := []byte("a private key generated anew on the infrastructure app's settings page")
			report, out := w.run(t, root.Apply, map[org.AppRole][]byte{org.InfrastructureApp: anew})
			sameSentences(t, "the changes of the apply handed a key", whats(report), []string{
				"GitHub: seals the infrastructure app's key into the secret BEDROCK_INFRASTRUCTURE_APP_KEY of the environment bedrock of " + libraryRoot,
			})
			if secret := env.Secrets["BEDROCK_INFRASTRUCTURE_APP_KEY"]; secret == nil || !bytes.Equal(secret.Value, anew) || len(report.Findings) != 0 {
				t.Errorf("after the apply handed a key the secret holds it: %t, with the findings %q:\n%s", secret != nil && bytes.Equal(secret.Value, anew), report.Findings, out)
			}
			w.clean(t)
		})
	}
}

// TestEngineRootEnvironmentBranches: the environment admits the default branch alone, so
// a workflow on any other branch is never handed the infrastructure app's key. A pattern
// beside the default branch, or a pattern of tags, is a finding that names it, which no
// apply removes; an environment with no branch policy of its own (one a workflow's run
// made by naming it, which admits every branch) is limited by the apply in one change
// the plan lists, and one that does not admit the default branch is given it; the key it
// holds stays.
func TestEngineRootEnvironmentBranches(t *testing.T) {
	t.Parallel()

	beside := func(pattern string) string {
		return "the environment bedrock of " + libraryRoot + " admits " + pattern + " beside master: a workflow on a branch that name matches is handed the infrastructure app's key; remove it (the repository's settings, Environments)"
	}
	tags := "the environment bedrock of " + libraryRoot + " admits the tags named v*: a workflow on a tag that name matches is handed the infrastructure app's key; remove it (the repository's settings, Environments)"
	limits := "GitHub: limits the environment bedrock of " + libraryRoot + " to the branch master: it admitted every branch, or every protected one"
	admits := "GitHub: admits the branch master to the environment bedrock of " + libraryRoot
	tests := []struct {
		name         string
		seed         func(env *githubtest.Environment)
		wantChanges  []string
		wantApplied  int
		wantFindings []string
		wantBranches []string
	}{
		{
			name: "another pattern beside the default branch", seed: func(env *githubtest.Environment) { env.Branches = append(env.Branches, "release/*") },
			wantFindings: []string{beside("release/*")}, wantBranches: []string{"master", "release/*"},
		},
		{
			name: "two other patterns", seed: func(env *githubtest.Environment) { env.Branches = []string{"hotfix", "master", "release/*"} },
			wantFindings: []string{beside("hotfix"), beside("release/*")}, wantBranches: []string{"hotfix", "master", "release/*"},
		},
		{
			name: "a pattern of tags", seed: func(env *githubtest.Environment) { env.Tags = []string{"v*"} },
			wantFindings: []string{tags}, wantBranches: []string{"master"},
		},
		{
			// The plan says the limit, which names the branch; the apply makes it and
			// then admits the branch.
			name: "no branch policy of its own", seed: func(env *githubtest.Environment) { env.Custom, env.Branches = false, nil },
			wantChanges: []string{limits}, wantApplied: 2, wantBranches: []string{"master"},
		},
		{
			name: "no branch policy of its own, the pattern kept", seed: func(env *githubtest.Environment) { env.Custom = false },
			wantChanges: []string{limits}, wantApplied: 1, wantBranches: []string{"master"},
		},
		{
			name: "its own policy, which admits no branch", seed: func(env *githubtest.Environment) { env.Branches = nil },
			wantChanges: []string{admits}, wantApplied: 1, wantBranches: []string{"master"},
		},
		{
			name: "its own policy, which admits another branch alone", seed: func(env *githubtest.Environment) { env.Branches = []string{"main"} },
			wantChanges: []string{admits}, wantApplied: 1, wantFindings: []string{beside("main")}, wantBranches: []string{"main", "master"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			env := w.repo(t, w.p.SourceRepo).Environments["bedrock"]
			tt.seed(env)
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the changes of the check", whats(report), tt.wantChanges)
			if env.Custom {
				// An environment with no policy of its own has no pattern to read until
				// the apply limits it; the findings are compared once it has.
				sameSentences(t, "the findings of the check", report.Findings, tt.wantFindings)
			}

			report, out := w.run(t, root.Apply, nil)
			if report.Applied != tt.wantApplied {
				t.Errorf("the apply made %d change(s), want %d:\n%s", report.Applied, tt.wantApplied, out)
			}
			if !env.Custom || !slices.Equal(env.Branches, tt.wantBranches) {
				t.Errorf("after the apply the environment keeps its own patterns: %t, and admits %q; want true and %q", env.Custom, env.Branches, tt.wantBranches)
			}
			if secret := env.Secrets["BEDROCK_INFRASTRUCTURE_APP_KEY"]; secret == nil || !bytes.Equal(secret.Value, w.keys[org.InfrastructureApp]) {
				t.Error("after the apply the environment no longer holds the infrastructure app's key")
			}
			report, _ = w.run(t, root.Check, nil)
			sameSentences(t, "the changes of the check after the apply", whats(report), nil)
			sameSentences(t, "the findings of the check after the apply", report.Findings, tt.wantFindings)
		})
	}
}

// TestEngineListedApps: without a cloud no key is read to sign as an app, so each app the
// placement calls for is held to its declaration by the organization's list of the apps
// installed on it and by the app's record: one not installed, one installed on a
// selection of the repositories, one owned by another account, one whose record lacks a
// declared permission or holds it at a lower level, one that holds a permission the
// declaration does not name, and one the placement records no App ID for are each a
// finding, which no apply clears; an app whose record cannot be read is a note.
func TestEngineListedApps(t *testing.T) {
	t.Parallel()

	const infrastructure, release = "imf-example-infrastructure", "imf-example-release"
	tests := []struct {
		name      string
		seed      func(w *githubWorld)
		want      []string
		wantNotes []string
	}{
		{name: "both as declared", seed: func(*githubWorld) {}},
		{
			name: "not installed on the organization",
			seed: func(w *githubWorld) {
				w.srv.OrgInstallations["imf-example"] = slices.DeleteFunc(w.srv.OrgInstallations["imf-example"], func(in github.OrgInstallation) bool { return in.AppID == releaseAppID })
			},
			want: []string{"the release app (App 6002) is not installed on the imf-example organization: install it there, for all repositories"},
		},
		{
			name: "installed on a selection of the repositories",
			seed: func(w *githubWorld) { w.srv.OrgInstallations["imf-example"][0].RepositorySelection = "selected" },
			want: []string{"the infrastructure app is installed on a selection of the imf-example organization's repositories, not all of them: a repository made later would be outside it"},
		},
		{
			name: "owned by another account",
			seed: func(w *githubWorld) {
				app := w.srv.PublicApps[release]
				app.Owner.Login = "someone-else"
				w.srv.PublicApps[release] = app
			},
			want: []string{"the release app (App 6002, imf-example-release) is owned by someone-else, not the imf-example organization"},
		},
		{
			name: "a declared permission the record lacks",
			seed: func(w *githubWorld) { delete(w.srv.PublicApps[infrastructure].Permissions, org.PermissionOrgSecrets) },
			want: []string{"the infrastructure app lacks the Secrets (organization) permission (write): add it on the app's settings page (Permissions & events), then accept the new permission on the organization's installation of the app (the organization's settings, GitHub Apps)"},
		},
		{
			name: "a declared permission held at a lower level",
			seed: func(w *githubWorld) { w.srv.PublicApps[release].Permissions[org.PermissionContents] = org.LevelRead },
			want: []string{"the release app holds the Contents permission as read, not write: raise it on the app's settings page (Permissions & events), then accept the new permission on the organization's installation of the app (the organization's settings, GitHub Apps)"},
		},
		{
			name: "a permission the declaration does not name",
			seed: func(w *githubWorld) {
				w.srv.PublicApps[release].Permissions[org.PermissionDeployments] = org.LevelWrite
			},
			want: []string{"the release app holds the Deployments permission (write), which it does not need: take it off on the app's settings page"},
		},
		{
			name: "both apps differ",
			seed: func(w *githubWorld) {
				delete(w.srv.PublicApps[infrastructure].Permissions, org.PermissionEnvironments)
				w.srv.PublicApps[release].Permissions[org.PermissionActions] = org.LevelRead
			},
			want: []string{
				"the infrastructure app lacks the Environments permission (write): add it on the app's settings page (Permissions & events), then accept the new permission on the organization's installation of the app (the organization's settings, GitHub Apps)",
				"the release app holds the Actions permission (read), which it does not need: take it off on the app's settings page",
			},
		},
		{
			name:      "a record that cannot be read",
			seed:      func(w *githubWorld) { delete(w.srv.PublicApps, release) },
			wantNotes: []string{"the release app is not checked: its record (imf-example-release) could not be read (GitHub GET /apps/imf-example-release answered 404: Not Found)"},
		},
		{
			name: "no App ID recorded for an app the placement calls for",
			seed: func(w *githubWorld) { w.p.GithubReleaseAppID, w.p.GithubReleaseAppSlug = "", "" },
			want: []string{"placement.json records no App ID for the release app, which this organization calls for: bedrock org bootstrap makes the app and records it"},
			wantNotes: []string{
				"the libraries' version tags are not ruled yet: placement.json records no release app, the one actor the rule lets make a release's tag",
				"the release app's key is not kept for the libraries yet: placement.json records no release app",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			tt.seed(w)
			for _, mode := range []root.Mode{root.Check, root.Apply, root.Check} {
				report, out := w.run(t, mode, nil)
				sameSentences(t, "the findings about the apps", report.Findings, tt.want)
				if len(report.Changes) != 0 {
					t.Errorf("an app that is not as declared is a change: %q\n%s", whats(report), out)
				}
				sameSentences(t, "the notes about the release app", mentioning(report.Notes, "release app"), tt.wantNotes)
			}
		})
	}
}

// TestEngineListedAppsNotListed: a token that may not list the apps installed on the
// organization leaves the apps unchecked with a note that says why, and the rest of the
// run goes on: a check by such a token is not a failure.
func TestEngineListedAppsNotListed(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	w.srv.PublicApps["imf-example-release"].Permissions[org.PermissionDeployments] = org.LevelWrite
	clients := w.refusing(t, map[string]int{"GET /orgs/imf-example/installations": http.StatusForbidden})
	var out bytes.Buffer
	report, err := root.Run(context.Background(), clients, &root.Request{Placement: w.p, Mode: root.Check, GitHubToken: "test-token"}, &out)
	if err != nil {
		t.Fatalf("Run() error = %v\n%s", err, out.String())
	}
	sameSentences(t, "the notes about the apps", mentioning(report.Notes, "GitHub Apps"), []string{
		"the GitHub Apps were not checked: the apps installed on imf-example could not be listed (GitHub GET /orgs/imf-example/installations?per_page=100 answered 403: Resource not accessible by integration)",
	})
	if !report.Clean() {
		t.Errorf("with the apps unchecked the check is not clean: changes %q, findings %q", whats(report), report.Findings)
	}
}

// TestEngineWithoutCloudApplyStopsAndFinishes: a change GitHub refuses stops the apply
// with the error under the change's own sentence, the changes before it made and none
// after it; the run after it reads what stands and finishes, making only what is left.
func TestEngineWithoutCloudApplyStopsAndFinishes(t *testing.T) {
	t.Parallel()

	w := newGitHubWorld(t, libraryPlacement(t))
	clients := w.refusing(t, map[string]int{"POST /orgs/imf-example/rulesets": http.StatusUnprocessableEntity})
	var out bytes.Buffer
	report, err := root.Run(context.Background(), clients, &root.Request{Placement: w.p, Mode: root.Apply, GitHubToken: "test-token", SealKeys: w.keys}, &out)
	// Of the apply's changes in order, the sealing of the infrastructure app's key is the
	// fifth and the rule on version tags the fifteenth.
	const sealsRootKey, rulesTags = 4, 14
	all := fromNothing(true)
	refused := all[rulesTags]
	if !strings.HasPrefix(refused, "libraries: rules the version tags") || !strings.HasPrefix(all[sealsRootKey], "GitHub: seals the infrastructure app's key") {
		t.Fatalf("the changes of an apply from nothing are not in the order this test counts on: %q", all)
	}
	if answer := "GitHub POST /orgs/imf-example/rulesets answered 422: Resource not accessible by integration"; err == nil || !strings.Contains(err.Error(), refused) || !strings.Contains(err.Error(), answer) {
		t.Fatalf("Run() error = %v, want it to name the change %q and GitHub's answer %q\n%s", err, refused, answer, out.String())
	}
	if report == nil {
		t.Fatal("Run() answered no report beside the error, and the changes made before it are lost")
	}
	sameSentences(t, "the changes up to the one refused", whats(report), all[:rulesTags+1])
	if report.Applied != rulesTags {
		t.Errorf("the apply made %d change(s) before the one refused, want %d", report.Applied, rulesTags)
	}
	if len(w.srv.OrgRulesets["imf-example"]) != 0 || len(w.srv.OrgSecrets["imf-example"]) != 0 || len(w.srv.Variables["imf-example"]) != 0 {
		t.Errorf("after the refusal the organization carries %d ruleset(s), %d secret(s) and %d variable(s); want none, the apply having stopped", len(w.srv.OrgRulesets["imf-example"]), len(w.srv.OrgSecrets["imf-example"]), len(w.srv.Variables["imf-example"]))
	}

	// The same apply once GitHub takes the ruleset: what stands is read, the keys the run
	// holds are sealed, and what the first run did not reach is made.
	finished, _ := w.run(t, root.Apply, w.keys)
	sameSentences(t, "the changes of the apply that finishes", whats(finished), slices.Concat(all[sealsRootKey:sealsRootKey+1], all[rulesTags:]))
	w.clean(t)
}

// TestEngineLibraryVisibility: a library whose visibility is not its entry's is a change,
// in a sentence that says both, and the apply makes it.
func TestEngineLibraryVisibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		library string
		private bool
		want    string
	}{
		{name: "private, and its entry says public", library: "workflows", private: true, want: "libraries: makes imf-example/workflows public (it is private)"},
		{name: "public, and its entry says private", library: "notes", private: false, want: "libraries: makes imf-example/notes private (it is public)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			repo := w.repo(t, tt.library)
			repo.Private = tt.private
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the changes of the check", whats(report), []string{tt.want})
			if repo.Private != tt.private || len(report.Findings) != 0 {
				t.Errorf("the check left the repository private: %t, with the findings %q; want %t and none", repo.Private, report.Findings, tt.private)
			}
			w.run(t, root.Apply, nil)
			if repo.Private == tt.private {
				t.Errorf("after the apply %s is private: %t", tt.library, repo.Private)
			}
			w.clean(t)
		})
	}
}

// TestEngineLibraryArchived: an archived library is a finding, and nothing else is done
// to it: its settings and its ruleset are neither compared nor written, whatever they
// are, while the other libraries are kept as before.
func TestEngineLibraryArchived(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	repo := w.repo(t, "impulse")
	repo.Archived, repo.Private, repo.Settings, repo.Rulesets = true, true, github.RepositorySettings{}, nil
	want := []string{"imf-example/impulse is archived, and placement.json lists it as a library: unarchive it on GitHub, or take it off the list"}
	for _, mode := range []root.Mode{root.Check, root.Apply} {
		report, out := w.run(t, mode, nil)
		sameSentences(t, "the findings", report.Findings, want)
		if len(report.Changes) != 0 {
			t.Errorf("an archived library is changed: %q\n%s", whats(report), out)
		}
		if got := mentioning(report.Notes, "imf-example/impulse"); len(got) != 0 {
			t.Errorf("the checks of an archived library were compared: %q", got)
		}
	}
	if !repo.Archived || !repo.Private || len(repo.Rulesets) != 0 || !cmp.Equal(repo.Settings, github.RepositorySettings{}) {
		t.Errorf("the apply wrote to the archived library: archived %t, private %t, %d ruleset(s), settings %+v", repo.Archived, repo.Private, len(repo.Rulesets), repo.Settings)
	}
}

// TestEngineLibraryAdoptedAndMissing: an entry that says its repository stood before the
// placement listed it (adopted), whose repository does not exist, is a finding and
// nothing is created for it; the libraries that stand read the release app's secrets
// without it, and the check after the apply says the same and lists no change, since no
// apply can let a repository that does not exist read a secret.
func TestEngineLibraryAdoptedAndMissing(t *testing.T) {
	t.Parallel()

	p := libraryPlacement(t)
	p.Libraries[2].Adopted = true
	w := newGitHubWorld(t, p)
	want := []string{"imf-example/notes does not exist, and its entry says it stood before the placement listed it (adopted): transfer the repository into the organization, or take adopted off the entry and the apply creates it"}
	report, out := w.run(t, root.Apply, w.keys)
	sameSentences(t, "the findings of the apply", report.Findings, want)
	if got := mentioning(whats(report), "imf-example/notes"); len(got) != 0 {
		t.Errorf("the apply changed the repository that does not exist: %q\n%s", got, out)
	}
	if _, made := w.srv.Repos["imf-example/notes"]; made {
		t.Error("the apply created the repository of an adopted entry")
	}
	sameSentences(t, "the repositories made", w.srv.Created, []string{"imf-example/workflows", "imf-example/impulse"})
	ids := w.libraryIDs(t, "workflows", "impulse")
	for _, name := range []string{"RELEASE_PLEASE_APP_PEM", "RELEASE_PLEASE_APP_CLIENT_ID"} {
		if got := w.orgSecret(t, name).RepositoryIDs; !slices.Equal(got, ids) {
			t.Errorf("%s is read by %v, want the two libraries that stand, %v", name, got, ids)
		}
	}
	// The rule on version tags names the repository all the same, for the day it comes.
	if diff := cmp.Diff(wantTagRuleset("workflows", "impulse", "notes"), rulesetNamed(w.srv.OrgRulesets["imf-example"], "version tags")); diff != "" {
		t.Errorf("the organization's ruleset on version tags (-want +got):\n%s", diff)
	}
	check, _ := w.run(t, root.Check, nil)
	sameSentences(t, "the findings of the check after the apply", check.Findings, want)
	sameSentences(t, "the changes of the check after the apply", whats(check), nil)
}

// TestEngineLibraryAdoptedWithAnotherRuleset: a repository that came with a ruleset of
// its own in force on its branches keeps it beside the engine's, and the engine says so
// in a finding, since its rules hold too. While the adopted entry names no checks, the
// checks that ruleset requires are printed in a note, to become the entry's first list;
// once the entry lists checks, the finding stands alone. A ruleset on tags and one that
// is not in force are passed over, and none is written to.
func TestEngineLibraryAdoptedWithAnotherRuleset(t *testing.T) {
	t.Parallel()

	finding := `imf-example/notes carries the ruleset "main protection" beside bedrock's own (default branch), and its rules hold too: once the entry's checks cover what it requires, delete it on GitHub (the repository's settings, Rules)`
	requires := map[string]any{"required_status_checks": []any{map[string]any{"context": "build"}, map[string]any{"context": "lint"}}}
	tests := []struct {
		name     string
		checks   []string
		rules    []github.Rule
		wantNote []string
	}{
		{
			name: "no checks listed yet", rules: []github.Rule{{Type: "deletion"}, {Type: "required_status_checks", Parameters: requires}},
			wantNote: []string{`imf-example/notes was adopted and its entry names no checks: the ruleset "main protection" it came with requires "build", "lint"; list them as the entry's checks`},
		},
		{name: "checks listed", checks: []string{"build", "lint"}, rules: []github.Rule{{Type: "deletion"}, {Type: "required_status_checks", Parameters: requires}}},
		{name: "no checks listed, and the other ruleset requires none", rules: []github.Rule{{Type: "deletion"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := libraryPlacement(t)
			p.Libraries[2].Adopted, p.Libraries[2].Checks = true, tt.checks
			w := newGitHubWorld(t, p)
			came := []*github.Ruleset{
				{ID: 501, Name: "main protection", Target: "branch", Enforcement: "active", Rules: tt.rules},
				{ID: 502, Name: "release tags", Target: "tag", Enforcement: "active", Rules: []github.Rule{{Type: "deletion"}}},
				{ID: 503, Name: "an experiment", Target: "branch", Enforcement: "disabled", Rules: []github.Rule{{Type: "required_status_checks", Parameters: requires}}},
			}
			w.srv.AddRepo("imf-example", "notes", &githubtest.Repo{Private: true, Refs: map[string]github.Object{"refs/heads/master": {Type: "commit", SHA: "adopted"}}, Rulesets: slices.Clone(came)})
			// The bootstrap's apply, which holds the keys, then the workflow's check.
			report, _ := w.run(t, root.Apply, w.keys)
			sameSentences(t, "the findings of the apply", report.Findings, []string{finding})
			sameSentences(t, "the apply's notes about the adoption", mentioning(report.Notes, "was adopted"), tt.wantNote)
			report, _ = w.run(t, root.Check, nil)
			sameSentences(t, "the findings of the check", report.Findings, []string{finding})
			sameSentences(t, "the check's notes about the adoption", mentioning(report.Notes, "was adopted"), tt.wantNote)
			sameSentences(t, "the changes of the check", whats(report), nil)
			sameSentences(t, "the repositories made", w.srv.Created, []string{"imf-example/workflows", "imf-example/impulse"})
			repo := w.repo(t, "notes")
			// The adopted repository is older than the two the apply made: the secrets'
			// readers are the three by id, in order, whatever the order of the entries.
			ids := w.libraryIDs(t, "workflows", "impulse", "notes")
			if got := w.orgSecret(t, "RELEASE_PLEASE_APP_PEM").RepositoryIDs; ids[0] != repo.ID || !slices.Equal(got, ids) {
				t.Errorf("RELEASE_PLEASE_APP_PEM is read by %v, want the three libraries' ids in order, %v, the adopted one (%d) first", got, ids, repo.ID)
			}
			if diff := cmp.Diff(wantBranchRuleset(tt.checks...), rulesetNamed(repo.Rulesets, "default branch")); diff != "" {
				t.Errorf("the engine's own ruleset on the adopted repository (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(came, repo.Rulesets[:min(len(came), len(repo.Rulesets))]); diff != "" || len(repo.Rulesets) != len(came)+1 {
				t.Errorf("the adopted repository carries %d ruleset(s), want the three it came with and the engine's; the three (-came +stand):\n%s", len(repo.Rulesets), diff)
			}
		})
	}
}

// TestEngineLibraryReportedChecks: the checks an entry requires are compared with the
// ones GitHub Actions reported on the head of the latest pull request into the library's
// default branch. A required name that pull request did not report is a finding, since
// every pull request would wait for it; the names it reported that the entry does not
// require are one note; a run of the same name by another app is not GitHub Actions'
// report; and a library with no pull request yet is a note.
func TestEngineLibraryReportedChecks(t *testing.T) {
	t.Parallel()

	const lint, titles = "actionlint", "semantic-titles / Validate PR title"
	pull := func(number int, base string) github.PullRequest {
		return github.PullRequest{Number: number, State: "closed", Title: "a change", Head: github.PullRequestRef{Ref: "change-" + strconv.Itoa(number), SHA: "head-" + strconv.Itoa(number)}, Base: github.PullRequestRef{Ref: base, SHA: "base"}}
	}
	by := func(app int64, names ...string) []github.CheckRun {
		runs := make([]github.CheckRun, 0, len(names))
		for _, name := range names {
			runs = append(runs, github.CheckRun{Name: name, Status: "completed", Conclusion: "success", App: github.CheckApp{ID: app}})
		}

		return runs
	}
	missing := func(check string, number int) string {
		return "imf-example/workflows requires " + strconv.Quote(check) + "; pull request " + strconv.Itoa(number) + " reported no such check; every pull request will wait for it"
	}
	tests := []struct {
		name         string
		pulls        []github.PullRequest
		runs         map[string][]github.CheckRun
		wantFindings []string
		wantNotes    []string
	}{
		{
			name:      "no pull request yet",
			wantNotes: []string{"imf-example/workflows has no pull request into master yet: the checks its entry requires are compared with the ones a pull request reports once there is one"},
		},
		{
			name:  "every required check reported",
			pulls: []github.PullRequest{pull(7, "master")}, runs: map[string][]github.CheckRun{"head-7": by(actionsAppID, lint, titles)},
		},
		{
			name:  "a required check not reported",
			pulls: []github.PullRequest{pull(7, "master")}, runs: map[string][]github.CheckRun{"head-7": by(actionsAppID, lint)},
			wantFindings: []string{`imf-example/workflows requires "semantic-titles / Validate PR title"; pull request 7 reported no such check; every pull request will wait for it`},
		},
		{
			name:         "no check reported at all",
			pulls:        []github.PullRequest{pull(7, "master")},
			wantFindings: []string{missing(lint, 7), missing(titles, 7)},
		},
		{
			name:  "checks reported that the entry does not require",
			pulls: []github.PullRequest{pull(7, "master")}, runs: map[string][]github.CheckRun{"head-7": by(actionsAppID, "zizmor", lint, "build", titles, "zizmor")},
			wantNotes: []string{`imf-example/workflows: pull request 7 reported checks its entry does not require: "build", "zizmor"`},
		},
		{
			name:  "a required name reported by another app",
			pulls: []github.PullRequest{pull(7, "master")}, runs: map[string][]github.CheckRun{"head-7": slices.Concat(by(actionsAppID, lint), by(999, titles, "its own check"))},
			wantFindings: []string{missing(titles, 7)},
		},
		{
			name:  "the latest pull request into the default branch is the one compared",
			pulls: []github.PullRequest{pull(7, "master"), pull(9, "master"), pull(11, "release")},
			runs: map[string][]github.CheckRun{
				"head-7": by(actionsAppID, lint, titles), "head-9": by(actionsAppID, titles), "head-11": by(actionsAppID, "a check of the release branch"),
			},
			wantFindings: []string{missing(lint, 9)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			repo := w.repo(t, "workflows")
			repo.PullRequests, repo.CheckRuns = tt.pulls, tt.runs
			for _, mode := range []root.Mode{root.Check, root.Apply} {
				report, out := w.run(t, mode, nil)
				sameSentences(t, "the findings", report.Findings, tt.wantFindings)
				sameSentences(t, "the notes about the library", mentioning(report.Notes, "imf-example/workflows"), tt.wantNotes)
				if len(report.Changes) != 0 {
					t.Errorf("the comparison of the checks is a change: %q\n%s", whats(report), out)
				}
			}
		})
	}
}

// TestEngineLibrariesDrift: what a person moved by hand and what the placement gained are
// listed and made true: a library's ruleset that lost its required checks, merge settings
// changed, the organization's ruleset on version tags put out of force with its bypass
// actor taken off, the variable that names the release app's login, a check added to an
// entry, and a library
// added to the list, which is created and joins the tag ruleset and the readers of the
// release app's secrets without either secret being sealed again.
func TestEngineLibrariesDrift(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	workflows := w.repo(t, "workflows")
	workflows.Rulesets[0].Rules = workflows.Rulesets[0].Rules[:3]
	yes := true
	w.repo(t, "impulse").Settings.AllowMergeCommit = &yes
	w.srv.OrgRulesets["imf-example"][0].Enforcement, w.srv.OrgRulesets["imf-example"][0].BypassActors = "disabled", []github.BypassActor{}
	w.srv.Variables["imf-example"]["RELEASE_APP_LOGIN"] = github.Variable{Name: "RELEASE_APP_LOGIN", Value: "someone-else[bot]", Visibility: "all"}
	w.p.Libraries[2].Checks = []string{"markdownlint"}
	w.p.Libraries = append(w.p.Libraries, org.Library{Repository: "docs"})
	if err := w.p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	want := []string{
		"libraries: brings the default branch's ruleset of imf-example/workflows to the placement: rules (required_status_checks)",
		"libraries: sets the merge settings of imf-example/impulse: " + mergeSentence,
		"libraries: brings the default branch's ruleset of imf-example/notes to the placement: rules (required_status_checks)",
		"libraries: creates the private repository imf-example/docs, with an initial commit on master",
		"libraries: sets the merge settings of imf-example/docs: " + mergeSentence,
		"libraries: rules the default branch of imf-example/docs: pull requests alone with 1 approving review(s), squash merges, no check required (the entry names none), the infrastructure app bypassing",
		`libraries: brings the imf-example organization's ruleset "version tags" to the placement: enforcement, bypass actors, tags and repositories`,
		"libraries: brings the repositories that read the organization secret RELEASE_PLEASE_APP_PEM to the libraries: adds docs",
		"libraries: brings the repositories that read the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the libraries: adds docs",
		"libraries: sets the organization variable RELEASE_APP_LOGIN to imf-example-release[bot] (was someone-else[bot])",
	}
	report, _ := w.run(t, root.Check, nil)
	sameSentences(t, "the changes of the check", whats(report), want)
	sameSentences(t, "the findings of the check", report.Findings, nil)

	report, out := w.run(t, root.Apply, nil)
	sameSentences(t, "the changes of the apply", whats(report), want)
	if report.Applied != len(want) {
		t.Errorf("the apply made %d of %d changes:\n%s", report.Applied, len(want), out)
	}
	for _, l := range w.p.Libraries {
		repo := w.repo(t, l.Repository)
		if diff := cmp.Diff(wantBranchRuleset(l.Checks...), rulesetNamed(repo.Rulesets, "default branch")); diff != "" || len(repo.Rulesets) != 1 {
			t.Errorf("%s carries %d ruleset(s); its ruleset on the default branch (-want +got):\n%s", l.Repository, len(repo.Rulesets), diff)
		}
		if diff := cmp.Diff(wantSettings(), repo.Settings); diff != "" {
			t.Errorf("the merge settings of %s (-want +got):\n%s", l.Repository, diff)
		}
	}
	if diff := cmp.Diff(wantTagRuleset("workflows", "impulse", "notes", "docs"), rulesetNamed(w.srv.OrgRulesets["imf-example"], "version tags")); diff != "" || len(w.srv.OrgRulesets["imf-example"]) != 1 {
		t.Errorf("the organization carries %d ruleset(s); its ruleset on version tags (-want +got):\n%s", len(w.srv.OrgRulesets["imf-example"]), diff)
	}
	ids := w.libraryIDs(t, "workflows", "impulse", "notes", "docs")
	if key := w.orgSecret(t, "RELEASE_PLEASE_APP_PEM"); !slices.Equal(key.RepositoryIDs, ids) || !bytes.Equal(key.Value, w.keys[org.ReleaseApp]) {
		t.Errorf("RELEASE_PLEASE_APP_PEM is read by %v and holds the key the bootstrap sealed: %t; want %v and true", key.RepositoryIDs, bytes.Equal(key.Value, w.keys[org.ReleaseApp]), ids)
	}
	if client := w.orgSecret(t, "RELEASE_PLEASE_APP_CLIENT_ID"); !slices.Equal(client.RepositoryIDs, ids) {
		t.Errorf("RELEASE_PLEASE_APP_CLIENT_ID is read by %v, want %v", client.RepositoryIDs, ids)
	}
	if got := w.srv.Variables["imf-example"]["RELEASE_APP_LOGIN"].Value; got != "imf-example-release[bot]" {
		t.Errorf("RELEASE_APP_LOGIN = %q", got)
	}
	w.clean(t)
}

// TestEngineTagRulesetGoesWithTheLastLibrary: once the placement lists no library, the
// organization's ruleset on version tags is a change that removes it, and the
// repositories that read the release app's secrets, libraries no longer, are taken off
// both and named in a finding each; the repositories themselves stand as they were.
func TestEngineTagRulesetGoesWithTheLastLibrary(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	w.p.Libraries = nil
	if err := w.p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	reads := func(name string) string {
		return "imf-example/" + name + " reads the release app's key (the organization secret RELEASE_PLEASE_APP_PEM) and placement.json lists no such library: a library taken off the list is archived by a person, never deleted here, and the apply takes the key from it"
	}
	wantChanges := []string{
		`libraries: removes the imf-example organization's ruleset "version tags": placement.json lists no library`,
		"libraries: brings the repositories that read the organization secret RELEASE_PLEASE_APP_PEM to the libraries: takes impulse, notes, workflows off",
		"libraries: brings the repositories that read the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the libraries: takes impulse, notes, workflows off",
	}
	wantFindings := []string{reads("workflows"), reads("impulse"), reads("notes")}
	report, _ := w.run(t, root.Check, nil)
	sameSentences(t, "the changes of the check", whats(report), wantChanges)
	sameSentences(t, "the findings of the check", report.Findings, wantFindings)
	if len(w.srv.OrgRulesets["imf-example"]) != 1 {
		t.Fatalf("the check removed the ruleset: %d left", len(w.srv.OrgRulesets["imf-example"]))
	}

	report, out := w.run(t, root.Apply, nil)
	sameSentences(t, "the changes of the apply", whats(report), wantChanges)
	if report.Applied != len(wantChanges) {
		t.Errorf("the apply made %d of %d changes:\n%s", report.Applied, len(wantChanges), out)
	}
	if left := w.srv.OrgRulesets["imf-example"]; len(left) != 0 {
		t.Errorf("after the apply the organization carries %d ruleset(s), want none", len(left))
	}
	for _, name := range []string{"RELEASE_PLEASE_APP_PEM", "RELEASE_PLEASE_APP_CLIENT_ID"} {
		if got := w.orgSecret(t, name).RepositoryIDs; len(got) != 0 {
			t.Errorf("after the apply %s is read by %v, want by no repository", name, got)
		}
	}
	for _, name := range []string{"workflows", "impulse", "notes"} {
		if repo := w.repo(t, name); repo.Archived || len(repo.Rulesets) != 1 {
			t.Errorf("%s: archived %t, %d ruleset(s); a library taken off the list stands as it was", name, repo.Archived, len(repo.Rulesets))
		}
	}
	w.clean(t)
}

// TestEngineLibrarySpelledInAnotherCase: GitHub finds a repository whatever the case of
// the name asked for, and holds the name in its own spelling. An entry spelled otherwise
// is a finding that names GitHub's spelling, and the readers of the release app's
// secrets are compared whatever the case, so no change comes back on every run.
func TestEngineLibrarySpelledInAnotherCase(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	// The placement spells in capitals the repository GitHub holds as notes.
	w.p.Libraries[2].Repository = "NOTES"
	report, out := w.run(t, root.Check, nil)
	sameSentences(t, "the findings of the check", report.Findings, []string{
		"placement.json lists the library NOTES, and GitHub spells the repository imf-example/notes: spell the entry as GitHub does",
	})
	if got := mentioning(whats(report), "repositories that read"); len(got) != 0 {
		t.Errorf("the check lists a change of the secrets' readers for a name that differs by case alone: %q\n%s", got, out)
	}
}

// TestEngineRunnerGroupAndPublicLibraries: the runner group the engine makes for the larger
// runner admits public repositories exactly when the placement lists a public library,
// whose jobs a group refuses otherwise; a group that stands the other way is brought to it.
func TestEngineRunnerGroupAndPublicLibraries(t *testing.T) {
	t.Parallel()

	const (
		makes       = "GitHub: makes the runner group applications, open to every repository"
		makesPublic = makes + ", the public ones too"
		opens       = "GitHub: opens the runner group applications to public repositories (placement.json lists a public library, whose jobs the group refuses until then)"
		closes      = "GitHub: closes the runner group applications to public repositories (placement.json lists no public library)"
	)
	tests := []struct {
		name string
		// private makes every library private before the first run.
		private bool
		// stands is the group already on GitHub, nil for none.
		stands     *github.RunnerGroup
		want       string
		wantPublic bool
	}{
		{name: "a public library, no group yet", want: makesPublic, wantPublic: true},
		{name: "private libraries alone, no group yet", private: true, want: makes},
		{name: "a public library, the group closed to public repositories", stands: &github.RunnerGroup{ID: 9001, Name: "applications", Visibility: github.VisibilityAll}, want: opens, wantPublic: true},
		{name: "private libraries alone, the group open to public repositories", private: true, stands: &github.RunnerGroup{ID: 9001, Name: "applications", Visibility: github.VisibilityAll, AllowsPublicRepositories: true}, want: closes},
		{name: "a public library, the group open already", stands: &github.RunnerGroup{ID: 9001, Name: "applications", Visibility: github.VisibilityAll, AllowsPublicRepositories: true}, wantPublic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := libraryPlacement(t)
			p.CiLargeRunner, p.CiLargeRunnerSize, p.CiLargeRunnerMaximum = "imf-8core", "8-core", 4
			if tt.private {
				for i := range p.Libraries {
					p.Libraries[i].Public = false
				}
			}
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			w := newGitHubWorld(t, p)
			if tt.stands != nil {
				w.srv.RunnerGroups[p.GithubOrganization] = []*github.RunnerGroup{tt.stands}
			}

			report, _ := w.run(t, root.Check, w.keys)
			var want []string
			if tt.want != "" {
				want = []string{tt.want}
			}
			sameSentences(t, "what the check says of the runner group", mentioning(whats(report), "the runner group applications"), want)
			containsAll(t, whats(report), "GitHub: sets the organization variable CI_LARGE_RUNNER to imf-8core", "GitHub: makes the larger runner imf-8core (8-core, at most 4 at once) in the group applications")

			if applied, out := w.run(t, root.Apply, w.keys); len(applied.Findings) != 0 || applied.Applied != len(applied.Changes) {
				t.Fatalf("the apply made %d of %d changes, with the findings %q:\n%s", applied.Applied, len(applied.Changes), applied.Findings, out)
			}
			groups := w.srv.RunnerGroups[p.GithubOrganization]
			if len(groups) != 1 || groups[0].AllowsPublicRepositories != tt.wantPublic {
				t.Errorf("the runner groups after the apply = %+v, want one that admits public repositories: %t", groups, tt.wantPublic)
			}
			w.clean(t)
		})
	}
}

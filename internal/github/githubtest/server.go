// Package githubtest is a GitHub API stand-in for tests: in-memory organizations with
// their installed apps, Actions secrets and rulesets, and one or more repositories with
// rulesets, refs, tags, check runs, environments and issues, served over httptest so the
// real client speaks to it unchanged.
package githubtest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/nacl/box"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/migration"
)

// Repo is one repository's state.
type Repo struct {
	// ID is the repository's id, which an organization secret's selection names it by. A
	// repository added (AddRepo) or made through the API with none gets the next of the
	// stand-in's ids, and one a test put in Repos directly gets its own the first time the
	// API serves it, so the API never answers 0.
	ID int64
	// Private says the repository is private: as a test set it, as its creation through
	// the API asked, or as a PATCH with private last set it.
	Private bool
	// Refs by full name (refs/heads/master, refs/tags/v0.1.0) to the object they point at;
	// the branches listing is the refs/heads entries, none for an empty repository.
	Refs map[string]github.Object
	// TagObjects by SHA: the commit each annotated tag object names.
	TagObjects map[string]string
	// Ancestry lists, per commit, the commits reachable from it (itself included), so a
	// comparison can be answered: base...head is identical when equal, ahead when base
	// is reachable from head, behind when the reverse, diverged otherwise.
	Ancestry map[string][]string
	// MergeBase by "a b" (either order) for a diverged pair.
	MergeBase map[string]string
	// Trees by commit SHA, and Files by "<tree>:<path>" to the blob's content; Blobs
	// by SHA. A commit made through the API gets a tree that copies its base tree's
	// files with the entries changed.
	Trees map[string]string
	Files map[string]string
	Blobs map[string]string
	// Comments by issue or pull request number, oldest first; a comment posted through
	// the API is appended.
	Comments map[int][]github.Comment
	// Releases by tag.
	Releases map[string]github.Release
	// Changed lists, per head commit, the files a comparison against it names.
	Changed map[string][]string
	// Archived says the repository was archived through the API (PATCH with archived).
	Archived bool
	// PullStates by pull request number: open or closed.
	PullStates map[int]string
	// PullRequests as the API lists them, oldest first; one opened through the API is
	// appended, numbered after the highest number the repository knows.
	PullRequests []github.PullRequest
	// Deployments made through the API, oldest first, with their statuses.
	Deployments []*Deployment
	// Dispatches is the workflow_dispatch events started, oldest first.
	Dispatches []Dispatch
	// Workflows are the paths of the workflows GitHub lists for the repository
	// (.github/workflows/ci.yml), in listing order; a dispatch of a file the listing lacks
	// is answered 404 as GitHub answers it. Nil models no listing, and every file dispatches.
	Workflows []string
	// Rulesets are the repository's rulesets, made or replaced through the API, with
	// their ids; Settings its merge settings, as read and as set through the API.
	Rulesets []*github.Ruleset
	Settings github.RepositorySettings
	// Environments are the repository's deployment environments, by name; one put
	// through the API is added.
	Environments map[string]*Environment
	// Issues are the repository's issues, oldest first; one opened through the API is
	// appended, numbered after the highest issue or pull request number the repository
	// knows, and its body kept in IssueBodies by that number. The listing of issues
	// carries the pull requests of PullRequests too, each with a pull_request key, as
	// GitHub's does.
	Issues      []*github.Issue
	IssueBodies map[int]string
	// CheckRuns are the check runs by the ref they are asked for with: a commit SHA, or
	// a branch or tag name. A name with no entry of its own is resolved through Refs to
	// its commit, and answered with that commit's runs.
	CheckRuns map[string][]github.CheckRun
}

// Environment is one deployment environment of a repository.
type Environment struct {
	// Custom says the environment takes deployments from the branches its own patterns
	// name alone (the API's custom_branch_policies), as a put through the API makes it.
	// One without it is an environment any branch may deploy to: the API describes it
	// with no branch policy and refuses it a pattern (404), as GitHub does.
	Custom bool
	// Branches are the name patterns of the branches that may deploy, in the order they
	// were added; Tags the tags' patterns, which a test seeds (the API here adds none)
	// and the listing carries after the branches', each marked as a tag's.
	Branches []string
	Tags     []string
	// Secrets are the environment's Actions secrets, by name: the ones a test seeds and
	// the ones put through the API.
	Secrets map[string]*EnvSecret
}

// EnvSecret is one Actions secret of an environment.
type EnvSecret struct {
	// Value is the plaintext: what the stand-in found on opening the sealed value a put
	// carried, with the key pair an organization secret's is opened with (OrgSecret).
	Value []byte
	// UpdatedAt is when the value was last put; every put sets it.
	UpdatedAt time.Time
}

// OrgSecret is one Actions secret of an organization.
type OrgSecret struct {
	// Value is the plaintext: what the stand-in found on opening the sealed value a put
	// carried, so a test can assert what was sealed. GitHub never gives it back, and
	// neither does any answer of the stand-in.
	Value []byte
	// Visibility is the secret's visibility as the put gave it (all, private, selected).
	Visibility string
	// RepositoryIDs are the ids of the repositories selected for the secret, as the
	// last put or the last setting of its selection carried them: an empty list, not
	// nil, for a JSON array of none.
	RepositoryIDs []int64
	// UpdatedAt is when the value was last put; every put sets it.
	UpdatedAt time.Time
}

// Dispatch is one workflow_dispatch event: the workflow file, the ref and the inputs.
type Dispatch struct {
	File   string
	Ref    string
	Inputs map[string]string
}

// Deployment is one deployment made through the API and the statuses added to it.
type Deployment struct {
	ID       int64
	Request  github.DeploymentRequest
	Statuses []github.DeploymentStatus
}

// Server is the stand-in.
type Server struct {
	*httptest.Server
	mu    sync.Mutex
	Repos map[string]*Repo
	// AppID and AppKey are the GitHub App whose JWT the installation endpoints accept;
	// AppInstallation is its installation's id. The installation token it mints is the
	// token every other endpoint accepts.
	AppID           string
	AppKey          *rsa.PublicKey
	AppInstallation int64
	// Apps are the apps the stand-in knows beside that one, by App ID: a JWT issued by
	// one is verified with its key, and GET /app and GET /app/installations answer with
	// its record and installations. Conversions are the apps a manifest's code makes
	// (POST /app-manifests/<code>/conversions), by code; a code is good once.
	Apps        map[string]*App
	Conversions map[string]*github.App
	nextID      int64
	// Calls records every method and path served, in order; Messages the commit
	// messages of commits made through the API.
	Calls    []string
	Messages []string
	// Bodies are the bodies of the pull requests opened through the API, oldest first.
	Bodies []string
	// Created are the repositories made through the API (owner/name), oldest first.
	Created []string
	// Login is who the token belongs to, as /user answers; octocat by default.
	// Memberships are the token's user's roles in organizations, by organization (admin
	// for an owner, member); an organization not listed answers not found.
	Login       string
	Memberships map[string]string
	// DefaultBranch is the organization's setting for a new repository's initial branch
	// (main by default), which GitHub uses whatever the creation asks.
	DefaultBranch string
	// The organizations' Actions settings, by organization: Variables by name, the
	// runner groups and the hosted runners made through the API. PublicApps are the apps
	// read by slug (/apps/<slug>); OrgInstallations the apps installed on an
	// organization, as its listing gives them.
	Variables        map[string]map[string]github.Variable
	RunnerGroups     map[string][]*github.RunnerGroup
	HostedRunners    map[string][]*github.HostedRunner
	PublicApps       map[string]github.App
	OrgInstallations map[string][]github.OrgInstallation
	// OrgSecrets are the organizations' Actions secrets, by organization then name: the
	// ones a test seeds and the ones put through the API. OrgRulesets are the
	// organizations' own rulesets, by organization, made, replaced and removed through
	// the API, with their ids.
	OrgSecrets  map[string]map[string]*OrgSecret
	OrgRulesets map[string][]*github.Ruleset
	// secretsPublic and secretsPrivate are the stand-in's own key pair, the one every
	// organization's and every environment's public key for secrets answers with: a
	// put's value is sealed with the public key and opened here with the private one.
	secretsPublic  *[32]byte
	secretsPrivate *[32]byte
}

// The answers' repeated keys and refusals.
const (
	totalCountKey    = "total_count"
	nameRequired     = "name is required"
	jsonBodyRequired = "a JSON body is required"
	// secretsKeyID is the id of the stand-in's public key for secrets, the same for
	// every organization and environment; a put sealed for another key is refused.
	secretsKeyID = "568250167242549743"
	// defaultPerPage is the page GitHub answers when a listing's request names no size.
	defaultPerPage = 30
	stateOpen      = "open"
	stateAll       = "all"
	branchPolicy   = "branch"
	tagPolicy      = "tag"
	typeKey        = "type"
)

var (
	refRE        = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/ref/(.+)$`)
	renameRE     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/branches/([^/]+)/rename$`)
	refsRE       = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/refs$`)
	refUpdRE     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/refs/(.+)$`)
	orgReposRE   = regexp.MustCompile(`^/orgs/([^/]+)/repos$`)
	tagObjectRE  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/tags/([^/]+)$`)
	compareRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/compare/(.+)\.{3}(.+)$`)
	tagsRE       = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/tags$`)
	branchesRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/branches$`)
	commitRE     = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/commits/([^/]+)$`)
	commitsRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/commits$`)
	blobsRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/blobs$`)
	treesRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/git/trees$`)
	contentsRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/contents/(.+)$`)
	issueRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)$`)
	commentsRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/(\d+)/comments$`)
	commentRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues/comments/(\d+)$`)
	releaseRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/releases/tags/(.+)$`)
	pullRE       = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls/(\d+)$`)
	pullsRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/pulls$`)
	deploysRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/deployments$`)
	statusesRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/deployments/(\d+)/statuses$`)
	repoRE       = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)$`)
	repoInstRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/installation$`)
	tokensRE     = regexp.MustCompile(`^/app/installations/(\d+)/access_tokens$`)
	appRE        = regexp.MustCompile(`^/app$`)
	rulesetsRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/rulesets$`)
	rulesetRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/rulesets/(\d+)$`)
	appSlugRE    = regexp.MustCompile(`^/apps/([^/]+)$`)
	orgInstsRE   = regexp.MustCompile(`^/orgs/([^/]+)/installations$`)
	variablesRE  = regexp.MustCompile(`^/orgs/([^/]+)/actions/variables$`)
	variableRE   = regexp.MustCompile(`^/orgs/([^/]+)/actions/variables/([^/]+)$`)
	groupsRE     = regexp.MustCompile(`^/orgs/([^/]+)/actions/runner-groups$`)
	hostedRE     = regexp.MustCompile(`^/orgs/([^/]+)/actions/hosted-runners$`)
	groupOneRE   = regexp.MustCompile(`^/orgs/([^/]+)/actions/runner-groups/(\d+)$`)
	hostedOneRE  = regexp.MustCompile(`^/orgs/([^/]+)/actions/hosted-runners/(\d+)$`)
	appInstsRE   = regexp.MustCompile(`^/app/installations$`)
	convertRE    = regexp.MustCompile(`^/app-manifests/([^/]+)/conversions$`)
	userRE       = regexp.MustCompile(`^/user$`)
	membershipRE = regexp.MustCompile(`^/user/memberships/orgs/([^/]+)$`)
	dispatchRE   = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/workflows/([^/]+)/dispatches$`)
	workflowsRE  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/actions/workflows$`)
	idMin        = int64(1000)

	issuesRE         = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/issues$`)
	checkRunsRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/commits/(.+)/check-runs$`)
	environmentRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/environments/([^/]+)$`)
	envBranchesRE    = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/environments/([^/]+)/deployment-branch-policies$`)
	envSecretsKeyRE  = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/environments/([^/]+)/secrets/public-key$`)
	envSecretRE      = regexp.MustCompile(`^/repos/([^/]+)/([^/]+)/environments/([^/]+)/secrets/([^/]+)$`)
	orgRulesetsRE    = regexp.MustCompile(`^/orgs/([^/]+)/rulesets$`)
	orgRulesetRE     = regexp.MustCompile(`^/orgs/([^/]+)/rulesets/(\d+)$`)
	secretsKeyRE     = regexp.MustCompile(`^/orgs/([^/]+)/actions/secrets/public-key$`)
	orgSecretRE      = regexp.MustCompile(`^/orgs/([^/]+)/actions/secrets/([^/]+)$`)
	orgSecretReposRE = regexp.MustCompile(`^/orgs/([^/]+)/actions/secrets/([^/]+)/repositories$`)
)

const (
	messageKey = "message"
	idKey      = "id"
	nameKey    = "name"
	notFound   = "Not Found"
	typeCommit = "commit"
)

// New starts the stand-in; it stops when the test ends.
func New(t *testing.T) *Server {
	t.Helper()

	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("box.GenerateKey() = %v", err)
	}
	s := &Server{
		Repos: map[string]*Repo{}, Apps: map[string]*App{}, Conversions: map[string]*github.App{}, nextID: idMin,
		Variables: map[string]map[string]github.Variable{}, RunnerGroups: map[string][]*github.RunnerGroup{}, HostedRunners: map[string][]*github.HostedRunner{},
		PublicApps: map[string]github.App{}, OrgInstallations: map[string][]github.OrgInstallation{}, Memberships: map[string]string{},
		DefaultBranch: "main",
		OrgSecrets:    map[string]map[string]*OrgSecret{}, OrgRulesets: map[string][]*github.Ruleset{},
		secretsPublic: public, secretsPrivate: private,
	}
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(s.Close)

	return s
}

// Client is the real client pointed at the stand-in.
func (s *Server) Client() *github.Client {
	return github.New(s.URL, "test-token")
}

// AddRepo registers a repository with its state.
func (s *Server) AddRepo(owner, name string, repo *Repo) *Repo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.addRepo(owner, name, repo)
}

// Update runs fn with the stand-in's lock held, for a test that changes the stand-in
// while the code under test is calling it: a browser flow's goroutine registering the
// app GitHub would have made, as the serving goroutines read the same maps.
func (s *Server) Update(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	fn()
}

// addRepo is AddRepo with the lock held, for a repository made through the API.
func (s *Server) addRepo(owner, name string, repo *Repo) *Repo {
	if repo.Refs == nil {
		repo.Refs = map[string]github.Object{}
	}
	if repo.TagObjects == nil {
		repo.TagObjects = map[string]string{}
	}
	if repo.Ancestry == nil {
		repo.Ancestry = map[string][]string{}
	}
	if repo.MergeBase == nil {
		repo.MergeBase = map[string]string{}
	}
	if repo.Trees == nil {
		repo.Trees = map[string]string{}
	}
	if repo.Files == nil {
		repo.Files = map[string]string{}
	}
	if repo.Blobs == nil {
		repo.Blobs = map[string]string{}
	}
	if repo.Comments == nil {
		repo.Comments = map[int][]github.Comment{}
	}
	if repo.Releases == nil {
		repo.Releases = map[string]github.Release{}
	}
	if repo.Changed == nil {
		repo.Changed = map[string][]string{}
	}
	if repo.PullStates == nil {
		repo.PullStates = map[int]string{}
	}
	if repo.Environments == nil {
		repo.Environments = map[string]*Environment{}
	}
	if repo.IssueBodies == nil {
		repo.IssueBodies = map[int]string{}
	}
	if repo.CheckRuns == nil {
		repo.CheckRuns = map[string][]github.CheckRun{}
	}
	if repo.ID == 0 {
		s.nextID++
		repo.ID = s.nextID
	}
	s.Repos[owner+"/"+name] = repo

	return repo
}

// App is a GitHub App the stand-in answers for: its key verifies the app's JWT, and the
// record and the installations are what GET /app and GET /app/installations give it.
type App struct {
	Key           *rsa.PublicKey
	Record        github.App
	Installations []github.Installation
}

// route pairs a path pattern with what serves it; the submatches are the pattern's.
type route struct {
	re     *regexp.Regexp
	handle func(s *Server, w http.ResponseWriter, r *http.Request, m []string)
}

// appRoute is a route served to an app's JWT; the issuer is the app's id.
type appRoute struct {
	re     *regexp.Regexp
	handle func(s *Server, w http.ResponseWriter, r *http.Request, m []string, issuer string)
}

// routes are served in order; the first pattern to match wins.
var routes = []route{
	{refsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createRef(w, r, m[1]+"/"+m[2])
	}},
	{refUpdRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.updateRef(w, r, m[1]+"/"+m[2], m[3])
	}},
	{orgReposRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createRepo(w, r, m[1])
	}},
	{refRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.ref(w, m[1]+"/"+m[2], "refs/"+m[3])
	}},
	{tagObjectRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.tagObject(w, m[1]+"/"+m[2], m[3])
	}},
	{compareRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.compare(w, m[1]+"/"+m[2], m[3], m[4])
	}},
	{workflowsRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.workflows(w, m[1]+"/"+m[2])
	}},
	{tagsRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.tags(w, m[1]+"/"+m[2])
	}},
	{branchesRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.branches(w, m[1]+"/"+m[2])
	}},
	{renameRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.renameBranch(w, r, m[1]+"/"+m[2], m[3])
	}},
	{commitRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.commit(w, m[1]+"/"+m[2], m[3])
	}},
	{commitsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createCommit(w, r, m[1]+"/"+m[2])
	}},
	{blobsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createBlob(w, r, m[1]+"/"+m[2])
	}},
	{treesRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createTree(w, r, m[1]+"/"+m[2])
	}},
	{contentsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.contents(w, r, m[1]+"/"+m[2], m[3])
	}},
	{issuesRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.issues(w, r, m[1]+"/"+m[2])
	}},
	{issueRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		number, _ := strconv.Atoi(m[3])
		s.issue(w, r, m[1]+"/"+m[2], number)
	}},
	{commentsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		number, _ := strconv.Atoi(m[3])
		s.comments(w, r, m[1]+"/"+m[2], number)
	}},
	{commentRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[3], 10, 64)
		s.updateComment(w, r, m[1]+"/"+m[2], id)
	}},
	{releaseRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		s.release(w, m[1]+"/"+m[2], m[3])
	}},
	{pullRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		number, _ := strconv.Atoi(m[3])
		s.pull(w, m[1]+"/"+m[2], number)
	}},
	{pullsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.pulls(w, r, m[1]+"/"+m[2], m[1])
	}},
	{deploysRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.deployments(w, r, m[1]+"/"+m[2])
	}},
	{statusesRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[3], 10, 64)
		s.deploymentStatus(w, r, m[1]+"/"+m[2], id)
	}},
	{membershipRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		role, ok := s.Memberships[m[1]]
		if !ok {
			reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

			return
		}
		reply(w, http.StatusOK, map[string]string{"role": role, "state": "active"})
	}},
	{userRE, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		login := s.Login
		if login == "" {
			login = "octocat"
		}
		reply(w, http.StatusOK, map[string]string{"login": login})
	}},
	{repoRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.updateRepo(w, r, m[1]+"/"+m[2])
	}},
	{dispatchRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.dispatch(w, r, m[1]+"/"+m[2], m[3])
	}},
	{rulesetsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.rulesets(w, r, m[1]+"/"+m[2])
	}},
	{rulesetRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[3], 10, 64)
		s.ruleset(w, r, m[1]+"/"+m[2], id)
	}},
	{appSlugRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		if app, ok := s.PublicApps[m[1]]; ok {
			reply(w, http.StatusOK, app)

			return
		}
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
	}},
	{orgInstsRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		installations := s.OrgInstallations[m[1]]
		if installations == nil {
			installations = []github.OrgInstallation{}
		}
		reply(w, http.StatusOK, map[string]any{totalCountKey: len(installations), "installations": installations})
	}},
	{variablesRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.createVariable(w, r, m[1])
	}},
	{variableRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.variable(w, r, m[1], m[2])
	}},
	{groupsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.runnerGroups(w, r, m[1])
	}},
	{groupOneRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[2], 10, 64)
		s.updateRunnerGroup(w, r, m[1], id)
	}},
	{hostedRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.hostedRunners(w, r, m[1])
	}},
	{hostedOneRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[2], 10, 64)
		s.updateHostedRunner(w, r, m[1], id)
	}},
	{checkRunsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.checkRuns(w, r, m[1]+"/"+m[2], m[3])
	}},
	{environmentRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.environment(w, r, m[1]+"/"+m[2], m[3])
	}},
	{envBranchesRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.environmentBranches(w, r, m[1]+"/"+m[2], m[3])
	}},
	{orgRulesetsRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.orgRulesets(w, r, m[1])
	}},
	{orgRulesetRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		id, _ := strconv.ParseInt(m[2], 10, 64)
		s.orgRuleset(w, r, m[1], id)
	}},
	// A public key comes before the secret by name, which its path would match too.
	{envSecretsKeyRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string) {
		if _, ok := s.environmentOf(w, m[1]+"/"+m[2], m[3]); ok {
			s.secretsKey(w)
		}
	}},
	{envSecretRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.environmentSecret(w, r, m[1]+"/"+m[2], m[3], m[4])
	}},
	{secretsKeyRE, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string) {
		s.secretsKey(w)
	}},
	{orgSecretRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.orgSecret(w, r, m[1], m[2])
	}},
	{orgSecretReposRE, func(s *Server, w http.ResponseWriter, r *http.Request, m []string) {
		s.orgSecretRepositories(w, r, m[1], m[2])
	}},
}

// dispatch records a workflow_dispatch event; the workflow file must be one the
// repository could carry (a name) and, when the repository models its listing, one
// GitHub lists, and the ref a branch the repository has.
func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, key, file string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if repo.Workflows != nil && !slices.Contains(repo.Workflows, ".github/workflows/"+file) {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var body struct {
		Ref    string            `json:"ref"`
		Inputs map[string]string `json:"inputs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	if _, ok := repo.Refs["refs/heads/"+body.Ref]; !ok {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "No ref found for: " + body.Ref})

		return
	}
	repo.Dispatches = append(repo.Dispatches, Dispatch{File: file, Ref: body.Ref, Inputs: body.Inputs})
	w.WriteHeader(http.StatusNoContent)
}

// appRoutes are served to the app's JWT, before the token check the others take; the
// issuer is the app the JWT signs as.
var appRoutes = []appRoute{
	{appRE, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string, issuer string) {
		if app, ok := s.Apps[issuer]; ok {
			reply(w, http.StatusOK, app.Record)

			return
		}
		id, _ := strconv.ParseInt(issuer, 10, 64)
		reply(w, http.StatusOK, github.App{ID: id, Slug: "app-" + issuer, Name: "app-" + issuer})
	}},
	{appInstsRE, func(s *Server, w http.ResponseWriter, _ *http.Request, _ []string, issuer string) {
		if app, ok := s.Apps[issuer]; ok {
			reply(w, http.StatusOK, app.Installations)

			return
		}
		reply(w, http.StatusOK, []github.Installation{})
	}},
	{repoInstRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string, issuer string) {
		if _, ok := s.repo(w, m[1]+"/"+m[2]); !ok {
			return
		}
		if app, ok := s.Apps[issuer]; ok {
			// A registered app: its installation on the repository's owner.
			i := slices.IndexFunc(app.Installations, func(in github.Installation) bool { return in.Account.Login == m[1] })
			if i < 0 {
				reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

				return
			}
			reply(w, http.StatusOK, map[string]int64{idKey: app.Installations[i].ID})

			return
		}
		reply(w, http.StatusOK, map[string]int64{idKey: s.AppInstallation})
	}},
	{tokensRE, func(s *Server, w http.ResponseWriter, _ *http.Request, m []string, issuer string) {
		id, _ := strconv.ParseInt(m[1], 10, 64)
		known := id == s.AppInstallation
		if app, ok := s.Apps[issuer]; ok {
			known = slices.ContainsFunc(app.Installations, func(in github.Installation) bool { return in.ID == id })
		}
		if !known {
			reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

			return
		}
		reply(w, http.StatusCreated, map[string]string{"token": "test-token"})
	}},
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := r.URL.Path
	s.Calls = append(s.Calls, r.Method+" "+path)
	for _, route := range appRoutes {
		if m := route.re.FindStringSubmatch(path); m != nil {
			issuer, err := s.verifyAppJWT(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
			if err != nil {
				reply(w, http.StatusUnauthorized, map[string]string{messageKey: err.Error()})

				return
			}
			route.handle(s, w, r, m, issuer)

			return
		}
	}
	if m := convertRE.FindStringSubmatch(path); m != nil && r.Method == http.MethodPost {
		s.convert(w, m[1])

		return
	}
	if r.Header.Get("Authorization") != "Bearer test-token" {
		reply(w, http.StatusUnauthorized, map[string]string{messageKey: "Bad credentials"})

		return
	}
	for _, route := range routes {
		if m := route.re.FindStringSubmatch(path); m != nil {
			route.handle(s, w, r, m)

			return
		}
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
}

// canonical is the key the stand-in holds the repository under (owner/name) for a key
// asked in any case, as GitHub finds a repository whatever the case of its name and
// answers with its own spelling; a key it holds none for is answered as asked.
func (s *Server) canonical(key string) string {
	if _, ok := s.Repos[key]; ok {
		return key
	}
	for held := range s.Repos {
		if strings.EqualFold(held, key) {
			return held
		}
	}

	return key
}

// repo is the repository the key names (owner/name), or the answer 404. A repository a
// test put in Repos directly, with no id, gets the stand-in's next id here.
func (s *Server) repo(w http.ResponseWriter, key string) (*Repo, bool) {
	repo, ok := s.Repos[s.canonical(key)]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return nil, false
	}
	if repo.ID == 0 {
		s.nextID++
		repo.ID = s.nextID
	}

	return repo, true
}

// describe is the repository as the API describes it. Its default branch is master when
// it has that branch, else a branch it has.
func describe(key string, repo *Repo) github.Repository {
	branch := "master"
	if _, ok := repo.Refs["refs/heads/"+branch]; !ok {
		for ref := range repo.Refs {
			if rest, isHead := strings.CutPrefix(ref, "refs/heads/"); isHead {
				branch = rest
			}
		}
	}

	return github.Repository{ID: repo.ID, Name: key[strings.Index(key, "/")+1:], FullName: key, DefaultBranch: branch, Private: repo.Private, Archived: repo.Archived, HTMLURL: "https://github.com/" + key}
}

// window is the part of a listing of n entries that one page holds, from start up to
// end, as the values of the request's per_page (thirty when it names none, as GitHub
// answers) and page ask; an empty part past the listing's end.
func window(perPage, page string, n int) (start, end int) {
	size, _ := strconv.Atoi(perPage)
	if size <= 0 {
		size = defaultPerPage
	}
	number, _ := strconv.Atoi(page)
	if number <= 0 {
		number = 1
	}
	start = min((number-1)*size, n)

	return start, min(start+size, n)
}

func (s *Server) ref(w http.ResponseWriter, key, ref string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	obj, ok := repo.Refs[ref]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	reply(w, http.StatusOK, github.Ref{Ref: ref, Object: obj})
}

// updateRef moves the reference (PATCH, refs/heads/... without the refs/ prefix) to the
// commit given; an absent reference is 422, as GitHub answers it.
func (s *Server) updateRef(w http.ResponseWriter, r *http.Request, key, ref string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method != http.MethodPatch {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var in struct {
		SHA   string `json:"sha"`
		Force bool   `json:"force"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	full := "refs/" + ref
	if _, exists := repo.Refs[full]; !exists {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "Reference does not exist"})

		return
	}
	repo.Refs[full] = github.Object{Type: typeCommit, SHA: in.SHA}
	reply(w, http.StatusOK, github.Ref{Ref: full, Object: repo.Refs[full]})
}

// createRepo makes a repository in the organization (POST /orgs/{org}/repos): private
// when the request says so, with an id, and with the default branch at an initial commit
// when auto_init is set, as GitHub does. Created records the repositories made this way.
func (s *Server) createRepo(w http.ResponseWriter, r *http.Request, owner string) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var in github.RepositoryRequest
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: nameRequired})

		return
	}
	if _, exists := s.Repos[owner+"/"+in.Name]; exists {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "name already exists on this account"})

		return
	}
	branch := s.DefaultBranch
	if branch == "" {
		branch = "main"
	}
	repo := &Repo{Private: in.Private, Refs: map[string]github.Object{}, Ancestry: map[string][]string{}, Trees: map[string]string{}, Files: map[string]string{}, Blobs: map[string]string{}}
	if in.AutoInit {
		sha := s.newSHA(typeCommit)
		tree := s.newSHA("tree")
		repo.Refs["refs/heads/"+branch] = github.Object{Type: typeCommit, SHA: sha}
		repo.Ancestry[sha] = []string{sha}
		repo.Trees[sha] = tree
		repo.Files[tree+":README.md"] = "# " + in.Name + "\n"
	}
	s.addRepo(owner, in.Name, repo)
	s.Created = append(s.Created, owner+"/"+in.Name)
	reply(w, http.StatusCreated, github.Repository{ID: repo.ID, Name: in.Name, FullName: owner + "/" + in.Name, DefaultBranch: branch, Private: in.Private, HTMLURL: "https://github.example/" + owner + "/" + in.Name})
}

func (s *Server) createRef(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var in struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	if _, exists := repo.Refs[in.Ref]; exists {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "Reference already exists"})

		return
	}
	repo.Refs[in.Ref] = github.Object{Type: typeCommit, SHA: in.SHA}
	reply(w, http.StatusCreated, github.Ref{Ref: in.Ref, Object: repo.Refs[in.Ref]})
}

func (s *Server) tagObject(w http.ResponseWriter, key, sha string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	commit, ok := repo.TagObjects[sha]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	reply(w, http.StatusOK, map[string]any{"sha": sha, "object": github.Object{Type: typeCommit, SHA: commit}})
}

// resolve turns a branch or tag name into a commit, or returns the name when it is one.
func (repo *Repo) resolve(name string) string {
	for _, ref := range []string{"refs/heads/" + name, "refs/tags/" + name} {
		if obj, ok := repo.Refs[ref]; ok {
			if obj.Type == tagPolicy {
				return repo.TagObjects[obj.SHA]
			}

			return obj.SHA
		}
	}

	return name
}

func (s *Server) compare(w http.ResponseWriter, key, base, head string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	baseSHA, headSHA := repo.resolve(base), repo.resolve(head)
	var status, mergeBase string
	switch {
	case baseSHA == headSHA:
		status, mergeBase = "identical", baseSHA
	case reachable(repo.Ancestry[headSHA], baseSHA):
		status, mergeBase = "ahead", baseSHA
	case reachable(repo.Ancestry[baseSHA], headSHA):
		status, mergeBase = "behind", headSHA
	default:
		status = "diverged"
		mergeBase = repo.MergeBase[baseSHA+" "+headSHA]
		if mergeBase == "" {
			mergeBase = repo.MergeBase[headSHA+" "+baseSHA]
		}
	}
	files := make([]github.ComparedFile, 0, len(repo.Changed[headSHA]))
	for _, name := range repo.Changed[headSHA] {
		files = append(files, github.ComparedFile{Filename: name})
	}
	reply(w, http.StatusOK, github.Comparison{Status: status, MergeBaseCommit: github.Object{Type: typeCommit, SHA: mergeBase}, Files: files})
}

// tags lists the tags with their commits, annotated ones resolved.
// workflows lists the repository's workflows as the API does: each path with an id, the
// file's name and the active state.
func (s *Server) workflows(w http.ResponseWriter, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	workflows := make([]github.Workflow, 0, len(repo.Workflows))
	for i, path := range repo.Workflows {
		workflows = append(workflows, github.Workflow{ID: idMin + int64(i), Name: strings.TrimSuffix(path[strings.LastIndex(path, "/")+1:], ".yml"), Path: path, State: "active"})
	}
	reply(w, http.StatusOK, map[string]any{totalCountKey: len(workflows), "workflows": workflows})
}

func (s *Server) tags(w http.ResponseWriter, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var tags []github.Tag
	for ref, obj := range repo.Refs {
		if !strings.HasPrefix(ref, "refs/tags/") {
			continue
		}
		sha := obj.SHA
		if obj.Type == tagPolicy {
			sha = repo.TagObjects[sha]
		}
		tags = append(tags, github.Tag{Name: strings.TrimPrefix(ref, "refs/tags/"), Commit: github.Object{Type: typeCommit, SHA: sha}})
	}
	sort.Slice(tags, func(i, j int) bool {
		return tags[i].Name < tags[j].Name
	})
	reply(w, http.StatusOK, tags)
}

// renameBranch moves the branch's ref to the new name (POST), or answers 404 for a branch
// the repository has not.
func (s *Server) renameBranch(w http.ResponseWriter, r *http.Request, key, branch string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var in struct {
		NewName string `json:"new_name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.NewName == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

		return
	}
	object, ok := repo.Refs["refs/heads/"+branch]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: "Branch not found"})

		return
	}
	delete(repo.Refs, "refs/heads/"+branch)
	repo.Refs["refs/heads/"+in.NewName] = object
	reply(w, http.StatusCreated, map[string]string{nameKey: in.NewName})
}

// branches lists the branches with their commits, by name, as the API does: an empty list
// for a repository nothing has been pushed to.
func (s *Server) branches(w http.ResponseWriter, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	branches := make([]github.Branch, 0)
	for ref, obj := range repo.Refs {
		if !strings.HasPrefix(ref, "refs/heads/") {
			continue
		}
		branches = append(branches, github.Branch{Name: strings.TrimPrefix(ref, "refs/heads/"), Commit: github.Object{Type: typeCommit, SHA: obj.SHA}})
	}
	sort.Slice(branches, func(i, j int) bool {
		return branches[i].Name < branches[j].Name
	})
	reply(w, http.StatusOK, branches)
}

func (s *Server) commit(w http.ResponseWriter, key, sha string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	tree, ok := repo.Trees[sha]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	reply(w, http.StatusOK, github.Commit{SHA: sha, Tree: github.Object{Type: "tree", SHA: tree}})
}

func (s *Server) contents(w http.ResponseWriter, r *http.Request, key, path string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	ref := r.URL.Query().Get("ref")
	tree, ok := repo.Trees[repo.resolve(ref)]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	if content, found := repo.Files[tree+":"+path]; found {
		reply(w, http.StatusOK, map[string]string{"content": base64.StdEncoding.EncodeToString([]byte(content)), "encoding": "base64"})

		return
	}
	// A directory: the files directly under it, each with its blob's git SHA.
	var entries []github.DirEntry
	for key, content := range repo.Files {
		rest, under := strings.CutPrefix(key, tree+":"+path+"/")
		if under && !strings.Contains(rest, "/") {
			entries = append(entries, github.DirEntry{Name: rest, SHA: migration.BlobSHA([]byte(content)), Type: "file"})
		}
	}
	if len(entries) == 0 {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	reply(w, http.StatusOK, entries)
}

func (s *Server) createBlob(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var in struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Encoding != "base64" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "content must be base64"})

		return
	}
	data, err := base64.StdEncoding.DecodeString(in.Content)
	if err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	sha := s.newSHA("blob")
	repo.Blobs[sha] = string(data)
	reply(w, http.StatusCreated, github.Object{Type: "blob", SHA: sha})
}

func (s *Server) createTree(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var in struct {
		BaseTree string             `json:"base_tree"`
		Tree     []github.TreeEntry `json:"tree"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	sha := s.newSHA("tree")
	for k, v := range repo.Files {
		if strings.HasPrefix(k, in.BaseTree+":") {
			repo.Files[sha+":"+strings.TrimPrefix(k, in.BaseTree+":")] = v
		}
	}
	for _, entry := range in.Tree {
		content, ok := repo.Blobs[entry.SHA]
		if !ok {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "no blob " + entry.SHA})

			return
		}
		repo.Files[sha+":"+entry.Path] = content
	}
	reply(w, http.StatusCreated, github.Object{Type: "tree", SHA: sha})
}

func (s *Server) createCommit(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var in struct {
		Message string   `json:"message"`
		Tree    string   `json:"tree"`
		Parents []string `json:"parents"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	sha := s.newSHA(typeCommit)
	repo.Trees[sha] = in.Tree
	ancestry := []string{sha}
	for _, parent := range in.Parents {
		ancestry = append(ancestry, repo.Ancestry[parent]...)
	}
	repo.Ancestry[sha] = ancestry
	s.Messages = append(s.Messages, in.Message)
	reply(w, http.StatusCreated, github.Object{Type: typeCommit, SHA: sha})
}

// newSHA is a made-up object name, distinct per call.
// issue answers the issue with its comment count, which the client pages by, whatever
// the number; or, for a PATCH, sets the state of the one of Issues with the number as the
// body says (closed, open) and answers it, or 404 when Issues holds none.
func (s *Server) issue(w http.ResponseWriter, r *http.Request, key string, number int) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method != http.MethodPatch {
		reply(w, http.StatusOK, map[string]any{"number": number, "comments": len(repo.Comments[number])})

		return
	}
	var in struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

		return
	}
	for _, issue := range repo.Issues {
		if issue.Number != number {
			continue
		}
		if in.State != "" {
			issue.State = in.State
		}
		reply(w, http.StatusOK, issue)

		return
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
}

// listedIssue is an issue as the listing of issues carries it; a pull request is listed
// as one too, with the pull_request key GitHub marks it with.
type listedIssue struct {
	github.Issue
	PullRequest *pullRequestLink `json:"pull_request,omitempty"`
}

// pullRequestLink is what the pull_request key of a listed issue holds: the pull
// request's page.
type pullRequestLink struct {
	HTMLURL string `json:"html_url"`
}

// issues lists the repository's issues (GET) or opens one (POST).
func (s *Server) issues(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		listIssues(w, r, repo)
	case http.MethodPost:
		createIssue(w, r, key, repo)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// listIssues answers one page of the repository's issues and, as GitHub lists them, its
// pull requests among them, newest first: the ones in the state the query names (open
// when it names none, every one for all).
func listIssues(w http.ResponseWriter, r *http.Request, repo *Repo) {
	state := r.URL.Query().Get("state")
	if state == "" {
		state = stateOpen
	}
	listed := make([]listedIssue, 0, len(repo.Issues)+len(repo.PullRequests))
	for _, issue := range repo.Issues {
		if state == stateAll || issue.State == state {
			listed = append(listed, listedIssue{Issue: *issue})
		}
	}
	for i := range repo.PullRequests {
		pr := &repo.PullRequests[i]
		if state == stateAll || pr.State == state {
			listed = append(listed, listedIssue{Issue: github.Issue{Number: pr.Number, Title: pr.Title, State: pr.State, HTMLURL: pr.HTMLURL}, PullRequest: &pullRequestLink{HTMLURL: pr.HTMLURL}})
		}
	}
	sort.SliceStable(listed, func(i, j int) bool {
		return listed[i].Number > listed[j].Number
	})
	start, end := window(r.URL.Query().Get("per_page"), r.URL.Query().Get("page"), len(listed))
	reply(w, http.StatusOK, listed[start:end])
}

// createIssue opens an issue with the title and keeps its body, numbered after the
// highest issue or pull request number the repository knows; a title is required.
func createIssue(w http.ResponseWriter, r *http.Request, key string, repo *Repo) {
	var in struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "title is required"})

		return
	}
	number := repo.nextNumber()
	issue := &github.Issue{Number: number, Title: in.Title, State: stateOpen, HTMLURL: "https://github.com/" + key + "/issues/" + strconv.Itoa(number)}
	repo.Issues = append(repo.Issues, issue)
	if repo.IssueBodies == nil {
		repo.IssueBodies = map[int]string{}
	}
	repo.IssueBodies[number] = in.Body
	reply(w, http.StatusCreated, issue)
}

// nextNumber is the number a new issue or pull request takes: one after the highest the
// repository knows, of an issue or of a pull request, which GitHub numbers as one series.
func (repo *Repo) nextNumber() int {
	number := 0
	for n := range repo.PullStates {
		number = max(number, n)
	}
	for i := range repo.PullRequests {
		number = max(number, repo.PullRequests[i].Number)
	}
	for _, issue := range repo.Issues {
		number = max(number, issue.Number)
	}

	return number + 1
}

// comments answers one page of the issue's comments, as per_page and page ask, or
// appends a posted one.
func (s *Server) comments(w http.ResponseWriter, r *http.Request, key string, number int) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		var in struct {
			Body string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "body is required"})

			return
		}
		s.nextID++
		comment := github.Comment{ID: s.nextID, Body: in.Body}
		repo.Comments[number] = append(repo.Comments[number], comment)
		reply(w, http.StatusCreated, comment)

		return
	}
	all := repo.Comments[number]
	size, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if size <= 0 {
		size = 30
	}
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page <= 0 {
		page = 1
	}
	start := min((page-1)*size, len(all))
	end := min(start+size, len(all))
	comments := make([]github.Comment, 0, end-start)
	comments = append(comments, all[start:end]...)
	reply(w, http.StatusOK, comments)
}

// updateRepo describes the repository (GET), with its id and whether it is private and
// archived, or changes its own settings (PATCH): the archive flag, which Archive sets,
// whether it is private, and the merge settings; anything else in the body is ignored. A
// change answers the repository's name, its id and its flags, as the API does, or 404.
func (s *Server) updateRepo(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		reply(w, http.StatusOK, repoRecord{Repository: describe(s.canonical(key), repo), RepositorySettings: repo.Settings})

		return
	}
	if r.Method != http.MethodPatch {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: "PATCH only"})

		return
	}
	var in struct {
		Archived *bool `json:"archived"`
		Private  *bool `json:"private"`
		github.RepositorySettings
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

		return
	}
	if in.Archived != nil {
		repo.Archived = *in.Archived
	}
	if in.Private != nil {
		repo.Private = *in.Private
	}
	settle(&repo.Settings.AllowMergeCommit, in.AllowMergeCommit)
	settle(&repo.Settings.AllowRebaseMerge, in.AllowRebaseMerge)
	settle(&repo.Settings.AllowSquashMerge, in.AllowSquashMerge)
	settle(&repo.Settings.SquashMergeCommitTitle, in.SquashMergeCommitTitle)
	settle(&repo.Settings.SquashMergeCommitMessage, in.SquashMergeCommitMessage)
	settle(&repo.Settings.DeleteBranchOnMerge, in.DeleteBranchOnMerge)
	settle(&repo.Settings.AllowUpdateBranch, in.AllowUpdateBranch)
	reply(w, http.StatusOK, map[string]any{idKey: repo.ID, "full_name": key, "private": repo.Private, "archived": repo.Archived})
}

// updateComment replaces a comment's body by its id (PATCH), or answers 404.
func (s *Server) updateComment(w http.ResponseWriter, r *http.Request, key string, id int64) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method != http.MethodPatch {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: "PATCH only"})

		return
	}
	var in struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Body == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "body is required"})

		return
	}
	for number, comments := range repo.Comments {
		for i, c := range comments {
			if c.ID == id {
				repo.Comments[number][i].Body = in.Body
				reply(w, http.StatusOK, repo.Comments[number][i])

				return
			}
		}
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
}

// release answers the release a tag belongs to, or 404.
func (s *Server) release(w http.ResponseWriter, key, tag string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	release, ok := repo.Releases[tag]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	reply(w, http.StatusOK, release)
}

// pull answers the pull request's state.
func (s *Server) pull(w http.ResponseWriter, key string, number int) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	state, ok := repo.PullStates[number]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	reply(w, http.StatusOK, map[string]any{"number": number, "state": state})
}

// pulls lists the pull requests whose head (owner:branch) and whose base branch the query
// names, either when it names it, newest first and a page at a time, or opens one: its
// head and base branches must exist, and it is numbered after the highest number the
// repository knows, of a pull request or of an issue.
func (s *Server) pulls(w http.ResponseWriter, r *http.Request, key, owner string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		head := strings.TrimPrefix(r.URL.Query().Get("head"), owner+":")
		base := r.URL.Query().Get("base")
		state := r.URL.Query().Get("state")
		prs := []github.PullRequest{}
		for i := len(repo.PullRequests) - 1; i >= 0; i-- {
			pr := repo.PullRequests[i]
			if (head != "" && pr.Head.Ref != head) || (base != "" && pr.Base.Ref != base) || (state != "" && state != stateAll && pr.State != state) {
				continue
			}
			prs = append(prs, pr)
		}
		start, end := window(r.URL.Query().Get("per_page"), r.URL.Query().Get("page"), len(prs))
		reply(w, http.StatusOK, prs[start:end])
	case http.MethodPost:
		var in github.PullRequestRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" || in.Head == "" || in.Base == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "title, head and base are required"})

			return
		}
		head := strings.TrimPrefix(in.Head, owner+":")
		headRef, headOK := repo.Refs["refs/heads/"+head]
		baseRef, baseOK := repo.Refs["refs/heads/"+in.Base]
		if !headOK || !baseOK {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "head or base branch not found"})

			return
		}
		number := repo.nextNumber()
		pr := github.PullRequest{
			Number: number, State: stateOpen, Title: in.Title,
			HTMLURL: "https://github.com/" + key + "/pull/" + strconv.Itoa(number),
			Head:    github.PullRequestRef{Ref: head, SHA: headRef.SHA},
			Base:    github.PullRequestRef{Ref: in.Base, SHA: baseRef.SHA},
		}
		repo.PullRequests = append(repo.PullRequests, pr)
		s.Bodies = append(s.Bodies, in.Body)
		if repo.PullStates == nil {
			repo.PullStates = map[int]string{}
		}
		repo.PullStates[number] = stateOpen
		reply(w, http.StatusCreated, pr)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// deployments lists the environment's deployments, newest first, or creates one.
func (s *Server) deployments(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		var in github.DeploymentRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Ref == "" || in.Environment == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "ref and environment are required"})

			return
		}
		s.nextID++
		repo.Deployments = append(repo.Deployments, &Deployment{ID: s.nextID, Request: in})
		reply(w, http.StatusCreated, map[string]int64{idKey: s.nextID})

		return
	}
	environment := r.URL.Query().Get("environment")
	list := []map[string]int64{}
	for i := len(repo.Deployments) - 1; i >= 0; i-- {
		if d := repo.Deployments[i]; environment == "" || d.Request.Environment == environment {
			list = append(list, map[string]int64{idKey: d.ID})
		}
	}
	reply(w, http.StatusOK, list)
}

// deploymentStatus adds a status to a deployment.
func (s *Server) deploymentStatus(w http.ResponseWriter, r *http.Request, key string, id int64) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	var in github.DeploymentStatus
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.State == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "state is required"})

		return
	}
	for _, d := range repo.Deployments {
		if d.ID == id {
			d.Statuses = append(d.Statuses, in)
			reply(w, http.StatusCreated, in)

			return
		}
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
}

// convert answers a manifest's conversion: the app registered for the code, once; an
// unknown or spent code is not found, as GitHub answers one.
func (s *Server) convert(w http.ResponseWriter, code string) {
	app, ok := s.Conversions[code]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	delete(s.Conversions, code)
	reply(w, http.StatusCreated, app)
}

// verifyAppJWT checks an app JWT the way GitHub does: signed RS256 by the key of the
// app its issuer names, one of Apps or the one AppID and AppKey name; it answers the
// issuer.
func (s *Server) verifyAppJWT(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a JWT")
	}
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	var c struct {
		Issuer string `json:"iss"`
	}
	if err := json.Unmarshal(claims, &c); err != nil {
		return "", fmt.Errorf("claims: %w", err)
	}
	key := s.AppKey
	if app, ok := s.Apps[c.Issuer]; ok {
		key = app.Key
	} else if c.Issuer != s.AppID {
		return "", fmt.Errorf("issuer %q is not app %s", c.Issuer, s.AppID)
	}
	if key == nil {
		return "", fmt.Errorf("no app is registered with the stand-in")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", fmt.Errorf("signature: %w", err)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return "", fmt.Errorf("signature does not verify: %w", err)
	}

	return c.Issuer, nil
}

func (s *Server) newSHA(kind string) string {
	s.nextID++

	return kind + strconv.FormatInt(s.nextID, 10)
}

func reachable(ancestors []string, sha string) bool {
	for _, a := range ancestors {
		if a == sha {
			return true
		}
	}

	return false
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		// The client reads a truncated answer; the test then fails on it.
		_, _ = w.Write([]byte(strings.TrimSpace(err.Error())))
	}
}

// repoRecord is a repository as GET /repos/{owner}/{repo} answers it: its description
// and its merge settings in one object.
type repoRecord struct {
	github.Repository
	github.RepositorySettings
}

// settle sets a setting when the update carries it.
func settle[T any](setting **T, update *T) {
	if update != nil {
		*setting = update
	}
}

// rulesets lists the repository's rulesets (GET) or makes one (POST).
func (s *Server) rulesets(w http.ResponseWriter, r *http.Request, key string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	s.serveRulesets(w, r, &repo.Rulesets)
}

// ruleset reads one of the repository's rulesets whole (GET), replaces it (PUT) or
// removes it (DELETE).
func (s *Server) ruleset(w http.ResponseWriter, r *http.Request, key string, id int64) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	serveRuleset(w, r, &repo.Rulesets, id)
}

// orgRulesets lists the organization's rulesets (GET) or makes one (POST).
func (s *Server) orgRulesets(w http.ResponseWriter, r *http.Request, org string) {
	list := s.OrgRulesets[org]
	s.serveRulesets(w, r, &list)
	if list != nil {
		s.OrgRulesets[org] = list
	}
}

// orgRuleset reads one of the organization's rulesets whole (GET), replaces it (PUT) or
// removes it (DELETE).
func (s *Server) orgRuleset(w http.ResponseWriter, r *http.Request, org string, id int64) {
	list := s.OrgRulesets[org]
	serveRuleset(w, r, &list, id)
	if list != nil {
		s.OrgRulesets[org] = list
	}
}

// serveRulesets lists the rulesets of the list, each with its id, name, target and
// enforcement alone (GET), or adds the one posted to it, with the stand-in's next id
// (POST).
func (s *Server) serveRulesets(w http.ResponseWriter, r *http.Request, list *[]*github.Ruleset) {
	switch r.Method {
	case http.MethodGet:
		listed := make([]github.Ruleset, 0, len(*list))
		for _, rs := range *list {
			listed = append(listed, github.Ruleset{ID: rs.ID, Name: rs.Name, Target: rs.Target, Enforcement: rs.Enforcement})
		}
		reply(w, http.StatusOK, listed)
	case http.MethodPost:
		var in github.Ruleset
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: nameRequired})

			return
		}
		s.nextID++
		in.ID = s.nextID
		*list = append(*list, &in)
		reply(w, http.StatusCreated, in)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// serveRuleset reads the ruleset of the list with the id whole (GET), replaces it (PUT)
// or removes it from the list (DELETE); an id the list does not hold is answered 404.
func serveRuleset(w http.ResponseWriter, r *http.Request, list *[]*github.Ruleset, id int64) {
	i := slices.IndexFunc(*list, func(rs *github.Ruleset) bool {
		return rs.ID == id
	})
	if i < 0 {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, http.StatusOK, (*list)[i])
	case http.MethodPut:
		var in github.Ruleset
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		in.ID = id
		(*list)[i] = &in
		reply(w, http.StatusOK, in)
	case http.MethodDelete:
		*list = slices.Delete(*list, i, i+1)
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// createVariable makes an organization variable (POST).
func (s *Server) createVariable(w http.ResponseWriter, r *http.Request, org string) {
	if r.Method != http.MethodPost {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var in github.Variable
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: nameRequired})

		return
	}
	if s.Variables[org] == nil {
		s.Variables[org] = map[string]github.Variable{}
	}
	if _, exists := s.Variables[org][in.Name]; exists {
		reply(w, http.StatusConflict, map[string]string{messageKey: "Variable already exists"})

		return
	}
	s.Variables[org][in.Name] = in
	w.WriteHeader(http.StatusCreated)
}

// variable reads (GET), changes (PATCH) or removes (DELETE) an organization variable.
func (s *Server) variable(w http.ResponseWriter, r *http.Request, org, name string) {
	v, ok := s.Variables[org][name]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, http.StatusOK, v)
	case http.MethodPatch:
		var in github.Variable
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		v.Value, v.Visibility = in.Value, in.Visibility
		s.Variables[org][name] = v
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		delete(s.Variables[org], name)
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// runnerGroups lists the organization's runner groups (GET) or makes one (POST).
func (s *Server) runnerGroups(w http.ResponseWriter, r *http.Request, org string) {
	switch r.Method {
	case http.MethodGet:
		groups := make([]github.RunnerGroup, 0, len(s.RunnerGroups[org]))
		for _, g := range s.RunnerGroups[org] {
			groups = append(groups, *g)
		}
		reply(w, http.StatusOK, map[string]any{totalCountKey: len(groups), "runner_groups": groups})
	case http.MethodPost:
		var in github.RunnerGroup
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: nameRequired})

			return
		}
		s.nextID++
		in.ID = s.nextID
		s.RunnerGroups[org] = append(s.RunnerGroups[org], &in)
		reply(w, http.StatusCreated, in)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// updateRunnerGroup opens the group to public repositories or closes it to them (PATCH).
func (s *Server) updateRunnerGroup(w http.ResponseWriter, r *http.Request, org string, id int64) {
	if r.Method != http.MethodPatch {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	var in struct {
		AllowsPublicRepositories *bool `json:"allows_public_repositories"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: err.Error()})

		return
	}
	for _, g := range s.RunnerGroups[org] {
		if g.ID != id {
			continue
		}
		if in.AllowsPublicRepositories != nil {
			g.AllowsPublicRepositories = *in.AllowsPublicRepositories
		}
		reply(w, http.StatusOK, *g)

		return
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: "Not Found"})
}

// hostedRunners lists the organization's hosted runners (GET) or makes one (POST).
func (s *Server) hostedRunners(w http.ResponseWriter, r *http.Request, org string) {
	switch r.Method {
	case http.MethodGet:
		runners := make([]github.HostedRunner, 0, len(s.HostedRunners[org]))
		for _, hr := range s.HostedRunners[org] {
			runners = append(runners, *hr)
		}
		reply(w, http.StatusOK, map[string]any{totalCountKey: len(runners), "runners": runners})
	case http.MethodPost:
		var in github.HostedRunnerRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" || in.Size == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "name and size are required"})

			return
		}
		s.nextID++
		runner := &github.HostedRunner{ID: s.nextID, Name: in.Name, RunnerGroupID: in.RunnerGroupID, Image: in.Image, MachineSize: &github.MachineSize{ID: in.Size}, MaximumRunners: in.MaximumRunners, Status: "Ready"}
		s.HostedRunners[org] = append(s.HostedRunners[org], runner)
		reply(w, http.StatusCreated, runner)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// updateHostedRunner moves a hosted runner to a group and sets its ceiling (PATCH).
func (s *Server) updateHostedRunner(w http.ResponseWriter, r *http.Request, org string, id int64) {
	if r.Method != http.MethodPatch {
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})

		return
	}
	for _, runner := range s.HostedRunners[org] {
		if runner.ID != id {
			continue
		}
		var in struct {
			RunnerGroupID  int64 `json:"runner_group_id"`
			MaximumRunners int   `json:"maximum_runners"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		runner.RunnerGroupID, runner.MaximumRunners = in.RunnerGroupID, in.MaximumRunners
		reply(w, http.StatusOK, runner)

		return
	}
	reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})
}

// checkRuns answers one page of the check runs of the ref asked for: the ones CheckRuns
// holds under the ref as it was asked (a commit SHA, a branch or tag name, or GitHub's
// heads/<branch> and tags/<tag>), else under the name without that prefix, else under the
// commit the name resolves to through Refs; none for a ref with no entry.
func (s *Server) checkRuns(w http.ResponseWriter, r *http.Request, key, ref string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	name := strings.TrimPrefix(strings.TrimPrefix(ref, "heads/"), "tags/")
	runs := []github.CheckRun{}
	for _, asked := range []string{ref, name, repo.resolve(name)} {
		if found, ok := repo.CheckRuns[asked]; ok {
			runs = append(runs, found...)

			break
		}
	}
	start, end := window(r.URL.Query().Get("per_page"), r.URL.Query().Get("page"), len(runs))
	reply(w, http.StatusOK, map[string]any{totalCountKey: len(runs), "check_runs": runs[start:end]})
}

// environment describes the repository's environment (GET), or answers 404; or makes it,
// or sets the one there is (PUT): one that takes deployments from its own branch patterns
// alone when the body's deployment_branch_policy says custom_branch_policies, and from any
// branch otherwise (the stand-in models no policy of protected branches). A policy whose
// two flags agree is refused 422, as GitHub documents it.
func (s *Server) environment(w http.ResponseWriter, r *http.Request, key, name string) {
	repo, ok := s.repo(w, key)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		env, ok := repo.Environments[name]
		if !ok {
			reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

			return
		}
		reply(w, http.StatusOK, describeEnvironment(name, env))
	case http.MethodPut:
		var in struct {
			BranchPolicy *github.BranchPolicy `json:"deployment_branch_policy"`
		}
		// GitHub takes a put with no body, for an environment with no rule of its own.
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		if in.BranchPolicy != nil && in.BranchPolicy.ProtectedBranches == in.BranchPolicy.CustomBranchPolicies {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "protected_branches and custom_branch_policies cannot have the same value"})

			return
		}
		env, ok := repo.Environments[name]
		if !ok {
			env = &Environment{}
			if repo.Environments == nil {
				repo.Environments = map[string]*Environment{}
			}
			repo.Environments[name] = env
		}
		env.Custom = in.BranchPolicy != nil && in.BranchPolicy.CustomBranchPolicies
		reply(w, http.StatusOK, describeEnvironment(name, env))
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// describeEnvironment is the environment as the API describes it: with the policy of
// custom branch patterns when it keeps its own, with none when any branch may deploy.
func describeEnvironment(name string, env *Environment) github.Environment {
	out := github.Environment{Name: name}
	if env.Custom {
		out.BranchPolicy = &github.BranchPolicy{CustomBranchPolicies: true}
	}

	return out
}

// environmentOf is the repository's environment with the name, or the answer 404 for a
// repository the stand-in lacks and for an environment the repository lacks.
func (s *Server) environmentOf(w http.ResponseWriter, key, name string) (*Environment, bool) {
	repo, ok := s.repo(w, key)
	if !ok {
		return nil, false
	}
	env, ok := repo.Environments[name]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return nil, false
	}

	return env, true
}

// environmentBranches lists the environment's branch patterns, a page at a time (GET), or
// adds one (POST), a branch's alone: the stand-in keeps no tag pattern and refuses one
// (422). An environment the repository lacks is answered 404; so is a pattern posted to an
// environment that does not keep patterns of its own, and a pattern the environment
// already has is answered 303, as GitHub documents both.
func (s *Server) environmentBranches(w http.ResponseWriter, r *http.Request, key, name string) {
	env, ok := s.environmentOf(w, key, name)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		policies := make([]map[string]any, 0, len(env.Branches)+len(env.Tags))
		for i, pattern := range env.Branches {
			policies = append(policies, map[string]any{idKey: i + 1, nameKey: pattern, typeKey: branchPolicy})
		}
		for i, pattern := range env.Tags {
			policies = append(policies, map[string]any{idKey: len(env.Branches) + i + 1, nameKey: pattern, typeKey: tagPolicy})
		}
		start, end := window(r.URL.Query().Get("per_page"), r.URL.Query().Get("page"), len(policies))
		reply(w, http.StatusOK, map[string]any{totalCountKey: len(policies), "branch_policies": policies[start:end]})
	case http.MethodPost:
		var in struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Name == "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: nameRequired})

			return
		}
		if in.Type != "" && in.Type != branchPolicy {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "the stand-in keeps the patterns of branches alone, not of a " + in.Type})

			return
		}
		if !env.Custom {
			reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

			return
		}
		if i := slices.Index(env.Branches, in.Name); i >= 0 {
			w.Header().Set("Location", r.URL.Path+"/"+strconv.Itoa(i+1))
			w.WriteHeader(http.StatusSeeOther)

			return
		}
		env.Branches = append(env.Branches, in.Name)
		reply(w, http.StatusOK, map[string]any{idKey: len(env.Branches), nameKey: in.Name, typeKey: branchPolicy})
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// orgSecret describes the organization's secret, never with its value (GET), puts it
// (PUT) or removes it (DELETE); a secret the organization lacks is answered 404 to a read
// and to a removal.
func (s *Server) orgSecret(w http.ResponseWriter, r *http.Request, org, name string) {
	if r.Method == http.MethodPut {
		s.putOrgSecret(w, r, org, name)

		return
	}
	secret, ok := s.OrgSecrets[org][name]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, http.StatusOK, github.OrgSecret{Name: name, Visibility: secret.Visibility, UpdatedAt: secret.UpdatedAt})
	case http.MethodDelete:
		delete(s.OrgSecrets[org], name)
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// putOrgSecret makes the organization's secret or replaces it: it opens the sealed value
// with the stand-in's key and keeps the plaintext, with the visibility and the selection
// the put carries and the time of the put. A put sealed for another key than the
// stand-in's, one whose value does not open, and one with no visibility GitHub knows are
// refused 422; a new secret is answered 201 and a replaced one 204, as GitHub answers.
func (s *Server) putOrgSecret(w http.ResponseWriter, r *http.Request, org, name string) {
	var in struct {
		EncryptedValue string  `json:"encrypted_value"`
		KeyID          string  `json:"key_id"`
		Visibility     string  `json:"visibility"`
		RepositoryIDs  []int64 `json:"selected_repository_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

		return
	}
	if !slices.Contains([]string{github.VisibilityAll, "private", github.VisibilitySelected}, in.Visibility) {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: "visibility must be all, private or selected"})

		return
	}
	value, refusal := s.openSealed(in.KeyID, in.EncryptedValue)
	if refusal != "" {
		reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: refusal})

		return
	}
	status := http.StatusNoContent
	if s.OrgSecrets[org] == nil {
		s.OrgSecrets[org] = map[string]*OrgSecret{}
	}
	if _, exists := s.OrgSecrets[org][name]; !exists {
		status = http.StatusCreated
	}
	s.OrgSecrets[org][name] = &OrgSecret{Value: value, Visibility: in.Visibility, RepositoryIDs: in.RepositoryIDs, UpdatedAt: time.Now().UTC()}
	w.WriteHeader(status)
}

// secretsKey answers the stand-in's public key for secrets with its id, as an
// organization's and an environment's public-key endpoints do.
func (s *Server) secretsKey(w http.ResponseWriter) {
	reply(w, http.StatusOK, map[string]string{"key_id": secretsKeyID, "key": base64.StdEncoding.EncodeToString(s.secretsPublic[:])})
}

// openSealed opens the sealed value a put of a secret carries (in base64, beside the id
// of the key it was sealed for) with the stand-in's key, and answers the plaintext; or
// the refusal for a value sealed for another key, one that is not base64 and one that
// does not open.
func (s *Server) openSealed(keyID, encrypted string) (value []byte, refusal string) {
	if keyID != secretsKeyID {
		return nil, "key_id is not the id of the public key"
	}
	sealed, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return nil, "encrypted_value is not base64"
	}
	value, ok := box.OpenAnonymous(nil, sealed, s.secretsPublic, s.secretsPrivate)
	if !ok {
		return nil, "encrypted_value does not open with the public key"
	}

	return value, ""
}

// environmentSecret describes the environment's secret, never with its value (GET), puts
// it (PUT) or removes it (DELETE). An environment the repository lacks is answered 404
// whatever the method, and a secret the environment lacks is answered 404 to a read and
// to a removal. A put opens the sealed value as an organization secret's is opened, with
// the same refusals, and is answered 201 for a new secret and 204 for a replaced one.
func (s *Server) environmentSecret(w http.ResponseWriter, r *http.Request, key, environment, name string) {
	env, ok := s.environmentOf(w, key, environment)
	if !ok {
		return
	}
	if r.Method == http.MethodPut {
		var in struct {
			EncryptedValue string `json:"encrypted_value"`
			KeyID          string `json:"key_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		value, refusal := s.openSealed(in.KeyID, in.EncryptedValue)
		if refusal != "" {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: refusal})

			return
		}
		status := http.StatusNoContent
		if env.Secrets == nil {
			env.Secrets = map[string]*EnvSecret{}
		}
		if _, exists := env.Secrets[name]; !exists {
			status = http.StatusCreated
		}
		env.Secrets[name] = &EnvSecret{Value: value, UpdatedAt: time.Now().UTC()}
		w.WriteHeader(status)

		return
	}
	secret, ok := env.Secrets[name]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	switch r.Method {
	case http.MethodGet:
		reply(w, http.StatusOK, github.EnvironmentSecret{Name: name, UpdatedAt: secret.UpdatedAt})
	case http.MethodDelete:
		delete(env.Secrets, name)
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

// orgSecretRepositories answers one page of the repositories selected for the
// organization's secret (GET): the ones of Repos whose id the selection holds, in the
// selection's order; or replaces the selection with the ids the body carries (PUT). A
// secret the organization lacks is answered 404.
func (s *Server) orgSecretRepositories(w http.ResponseWriter, r *http.Request, org, name string) {
	secret, ok := s.OrgSecrets[org][name]
	if !ok {
		reply(w, http.StatusNotFound, map[string]string{messageKey: notFound})

		return
	}
	switch r.Method {
	case http.MethodGet:
		byID := make(map[int64]github.Repository, len(s.Repos))
		for key, repo := range s.Repos {
			if repo.ID != 0 {
				byID[repo.ID] = describe(key, repo)
			}
		}
		selected := make([]github.Repository, 0, len(secret.RepositoryIDs))
		for _, id := range secret.RepositoryIDs {
			if repo, ok := byID[id]; ok {
				selected = append(selected, repo)
			}
		}
		start, end := window(r.URL.Query().Get("per_page"), r.URL.Query().Get("page"), len(selected))
		reply(w, http.StatusOK, map[string]any{totalCountKey: len(selected), "repositories": selected[start:end]})
	case http.MethodPut:
		var in struct {
			RepositoryIDs []int64 `json:"selected_repository_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			reply(w, http.StatusUnprocessableEntity, map[string]string{messageKey: jsonBodyRequired})

			return
		}
		secret.RepositoryIDs = in.RepositoryIDs
		w.WriteHeader(http.StatusNoContent)
	default:
		reply(w, http.StatusMethodNotAllowed, map[string]string{messageKey: r.Method})
	}
}

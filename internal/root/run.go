// run.go is a trigger's run of the root organization: bedrock org plan on a pull request,
// which says what the merge would change and posts it, and bedrock org apply on a push,
// which makes it so under a lock and leaves a marker. Both read their build through the
// Cloud Build API, as the hosting organizations' runs do, and act on GitHub as the
// infrastructure app.

package root

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// RunRequest is what a trigger's run was started with: the build and the checkout.
type RunRequest struct {
	BuildID  string
	Project  string
	Location string
	Dir      string
}

// The substitutions a run reads off its build.
const (
	stepsSub    = "_BEDROCK_STEPS"
	prNumberSub = "_PR_NUMBER"
	commitSub   = "COMMIT_SHA"
)

// The comment the plan posts, found by its first line on a later push.
const planMarker = "<!-- bedrock org plan -->"

// RunClients are the run's seams beyond the engine's: Cloud Build for the build, Secret
// Manager for the apps' keys.
type RunClients struct {
	Builds  deploy.BuildsFunc
	Secrets deploy.SecretsFunc
}

// trigger is one run's reading of its build.
type trigger struct {
	p      *org.Placement
	subs   map[string]string
	commit string
}

// read reads the placement off the checkout and the build off Cloud Build, and checks
// the steps' shape.
func read(ctx context.Context, rc *RunClients, req *RunRequest, command string, out io.Writer) (*trigger, error) {
	for name, value := range map[string]string{"BUILD_ID": req.BuildID, "PROJECT_ID": req.Project, "LOCATION": req.Location} {
		if value == "" {
			return nil, errors.Newf("%s is not set: bedrock org %s runs from the %s trigger of the root organization, whose step passes the build's id, project and location to it", name, command, command)
		}
	}
	if rc == nil || rc.Builds == nil || rc.Secrets == nil {
		return nil, errors.New("no Cloud Build or Secret Manager client is wired")
	}
	p, err := org.ReadPlacement(filepath.Join(req.Dir, org.PlacementFile))
	if err != nil {
		return nil, err
	}
	if !p.IsRoot() {
		return nil, errors.Newf("the %s organization is a hosting organization: its triggers run the layers", p.Prefix)
	}
	builds, err := rc.Builds(ctx)
	if err != nil {
		return nil, err
	}
	data, err := builds.Get(ctx, req.Project, req.Location, req.BuildID)
	if err != nil {
		return nil, err
	}
	build, err := deploy.ParseBuild(data)
	if err != nil {
		return nil, err
	}
	t := &trigger{p: p, subs: build.Substitutions, commit: build.Substitutions[commitSub]}
	steps, ok := t.subs[stepsSub]
	if !ok {
		return nil, errors.Newf("no %s substitution: bedrock org %s runs from the %s trigger the bootstrap made, whose substitutions carry the steps' shape; this build did not start from it", stepsSub, command, command)
	}
	if shape := org.StepsShape(p); steps != shape {
		fmt.Fprintf(out, "This run's steps are of shape %s, and this bedrock makes shape %s for the placement's pin: the trigger's steps lag one apply behind the repository, and the next run of the trigger is the first on the shape this bedrock makes. This run goes on with what the steps pass.\n", steps, shape)
	}
	if t.commit == "" {
		return nil, errors.Newf("%s is not set: a trigger's run names the commit it builds", commitSub)
	}

	return t, nil
}

// AppToken mints an app's installation token on the repository from its key's alias, as
// the credentials read it (a trigger's runner; a person holding the key's read).
func AppToken(ctx context.Context, rc *RunClients, gh deploy.GitHubFunc, p *org.Placement, role org.AppRole) (string, error) {
	version := p.SecretVersionName(role.Key())
	if p.AppID(role) == "" || version == "" {
		return "", errors.Newf("placement.json records no %s or no pinned key for it: bedrock org bootstrap records both", role.Title())
	}
	secrets, err := rc.Secrets(ctx)
	if err != nil {
		return "", err
	}
	defer secrets.Close()
	pem, err := secrets.Access(ctx, version)
	if err != nil {
		return "", errors.Wrapf(err, "%s's key (%s)", role.Title(), version)
	}

	return deploy.AppInstallationToken(ctx, gh, p.AppID(role), pem, p.GithubOrganization, p.SourceRepo, role.Title())
}

// PlanRun is bedrock org plan: it says what the pull request's merge would change in the
// root organization, in the log and in one comment on the pull request as the deployer
// app, and exits well whatever it found; a change that cannot be read is the error, and
// the plan that stopped at it says what it had gathered in both places (stoppedPlan).
func PlanRun(ctx context.Context, clients *Clients, rc *RunClients, req *RunRequest, out io.Writer) error {
	t, err := read(ctx, rc, req, org.PlanCommand, out)
	if err != nil {
		return err
	}
	number, err := strconv.Atoi(t.subs[prNumberSub])
	if err != nil || number <= 0 {
		return errors.Newf("%s is %q: the plan trigger runs on a pull request, and Cloud Build names it there", prNumberSub, t.subs[prNumberSub])
	}
	infra, err := AppToken(ctx, rc, clients.GitHub, t.p, org.InfrastructureApp)
	if err != nil {
		return err
	}
	report, err := Run(ctx, clients, &Request{Placement: t.p, Dir: req.Dir, Mode: Plan, Commit: t.commit, GitHubToken: infra}, out)
	if err != nil {
		return stoppedPlan(err, t.commit, t.p, out, func(body string) error {
			deployer, terr := AppToken(ctx, rc, clients.GitHub, t.p, org.DeployerApp)
			if terr != nil {
				return terr
			}

			return postPlan(ctx, clients.GitHub(deployer), t.p, number, body, org.DeployerApp.Title(), out)
		})
	}
	report.Write(out, Plan)
	deployer, err := AppToken(ctx, rc, clients.GitHub, t.p, org.DeployerApp)
	if err != nil {
		fmt.Fprintf(out, "The report stays in this log: %v.\n", errors.Cause(err))

		return nil
	}

	return postPlan(ctx, clients.GitHub(deployer), t.p, number, report.Comment(t.commit, t.p), org.DeployerApp.Title(), out)
}

// stoppedPlan ends a plan whose run answered err. What a plan that stopped in a section
// had gathered, which its error carries, goes to the log and, in the same form with the
// error, into the plan's one comment on the pull request, in place of an earlier plan's:
// a person reads the stop where the pull request is, and the pull request never shows a
// finished plan of an earlier push as the answer to this one. A plan refused before its
// first section read nothing: the log has its error alone, and its comment is that of a
// plan that had found no change. Where the comment cannot be posted (no token for it, or
// GitHub refuses the call) the log says so and nothing is said of a comment. The answer
// is err either way: the run fails as it did.
func stoppedPlan(err error, commit string, p *org.Placement, out io.Writer, post func(body string) error) error {
	// Refused before its first section: it read nothing.
	gathered := &Report{}
	var stopped *StoppedError
	if errors.As(err, &stopped) {
		gathered = stopped.Report
		gathered.WriteStopped(out, Plan)
	}
	if perr := post(gathered.StoppedComment(commit, p, err)); perr != nil {
		fmt.Fprintf(out, "The pull request's comment was not posted (%v): the plan's stop is in this log alone.\n", errors.Cause(perr))
	}

	return err
}

// postPlan keeps the plan's one comment on the pull request: the comment an earlier push's
// plan left, found by its marker, is rewritten, and the first plan posts it. as names the
// app the token is of.
func postPlan(ctx context.Context, gh *github.Client, p *org.Placement, number int, body, as string, out io.Writer) error {
	existing, err := gh.IssueComments(ctx, p.GithubOrganization, p.SourceRepo, number)
	if err != nil {
		return err
	}
	for _, c := range existing {
		if strings.HasPrefix(c.Body, planMarker) {
			if err := gh.UpdateComment(ctx, p.GithubOrganization, p.SourceRepo, c.ID, body); err != nil {
				return err
			}
			fmt.Fprintf(out, "Updated the report on pull request #%d as %s.\n", number, as)

			return nil
		}
	}
	if err := gh.CreateComment(ctx, p.GithubOrganization, p.SourceRepo, number, body); err != nil {
		return err
	}
	fmt.Fprintf(out, "Posted the report on pull request #%d as %s.\n", number, as)

	return nil
}

// Comment is the plan's comment: the marker, the heading, what the merge changes and the
// findings, each on a line.
func (r *Report) Comment(commit string, p *org.Placement) string {
	var b strings.Builder
	b.WriteString(commentHead(commit, p))
	pending := r.pendingCreations(p)
	if len(r.Changes) == 0 && len(pending) == 0 {
		b.WriteString("Nothing: the root organization stands as placement.json says.\n")
	}
	r.commentGathered(&b, pending)

	return b.String()
}

// StoppedComment is the comment of a plan that ended on err, in the place and under the
// marker and heading of a finished plan's, so that each takes the other's place: first
// that the plan stopped and that what follows is incomplete, then what it had gathered
// before the stop, as a finished plan lists it and never as the whole of what the merge
// changes, then the error as the run's log ends with it, set as code so that nothing in
// it is read as formatting or as a mention.
func (r *Report) StoppedComment(commit string, p *org.Placement, err error) string {
	var b strings.Builder
	b.WriteString(commentHead(commit, p))
	b.WriteString("**The plan stopped before it finished, at the error below: what it had gathered before the stop follows, and it is incomplete.**\n\n")
	pending := r.pendingCreations(p)
	if len(r.Changes) == 0 && len(pending) == 0 {
		b.WriteString("It had found no change before the stop.\n")
	}
	r.commentGathered(&b, pending)
	b.WriteString("\nThe error the plan stopped at:\n\n")
	// Every line is indented, a carriage return counted as a line's end as GitHub counts
	// it, so no part of the error leaves the code block. An error longer than the bound
	// (an API's answer quoted whole) is cut, so that it cannot take the comment over the
	// size GitHub accepts and leave an earlier plan's standing.
	text := strings.TrimSpace(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(errors.Cause(err).Error()))
	cut := len(text) > commentErrorBytes
	if cut {
		text = strings.ToValidUTF8(text[:commentErrorBytes], "")
	}
	for _, line := range strings.Split(text, "\n") {
		b.WriteString("    " + line + "\n")
	}
	if cut {
		fmt.Fprintf(&b, "\nThe error is cut after %d bytes; the run's log ends with the whole of it.\n", commentErrorBytes)
	}

	return b.String()
}

// commentErrorBytes bounds the error a stopped plan's comment quotes: GitHub refuses a
// comment longer than 65536 characters, and the comment carries the report too.
const commentErrorBytes = 8000

// commentHead opens the plan's comment: the marker a later plan finds it by, the heading
// and the commit.
func commentHead(commit string, p *org.Placement) string {
	return planMarker + "\n### what the merge changes in the " + p.Prefix + " root organization\n\nAt " + short(commit) + ":\n\n"
}

// commentGathered lists what the plan found: the changes, the hosting organizations the
// merge creates, the findings and the notes, each on a line.
func (r *Report) commentGathered(b *strings.Builder, pending []string) {
	for _, c := range r.Changes {
		b.WriteString("- " + c.Section + ": " + c.What + "\n")
	}
	for _, p := range pending {
		b.WriteString("- creations: creates the hosting organization " + p + " as the creating identity (its folder, its boot project, the first applies, its repository, the hand-off pull request)\n")
	}
	if len(r.Findings) > 0 {
		b.WriteString("\nFindings, which the apply does not change:\n\n")
		for _, f := range r.Findings {
			b.WriteString("- " + f + "\n")
		}
	}
	if len(r.Notes) > 0 {
		b.WriteString("\n")
		for _, n := range r.Notes {
			b.WriteString("- note: " + n + "\n")
		}
	}
}

// pendingCreations names the hosting organizations the apply would create.
func (r *Report) pendingCreations(p *org.Placement) []string {
	pending := p.Pending()
	names := make([]string, 0, len(pending))
	for _, h := range pending {
		names = append(names, h.Prefix)
	}

	return names
}

// lock is what the apply leaves in the lock object while it runs.
type lock struct {
	Build  string `json:"build"`
	Commit string `json:"commit"`
	Since  string `json:"since"`
}

// ApplyRun is bedrock org apply: under the lock in the state bucket, it makes the root
// organization as the pushed placement says and creates the hosting organizations
// recorded without a folder, then leaves the marker.
func ApplyRun(ctx context.Context, clients *Clients, rc *RunClients, req *RunRequest, out io.Writer) error {
	t, err := read(ctx, rc, req, org.ApplyCommand, out)
	if err != nil {
		return err
	}
	infra, err := AppToken(ctx, rc, clients.GitHub, t.p, org.InfrastructureApp)
	if err != nil {
		return err
	}
	locks, err := clients.Locks(ctx)
	if err != nil {
		return err
	}
	defer locks.Close()
	now := time.Now
	if clients.Now != nil {
		now = clients.Now
	}
	data, err := json.Marshal(lock{Build: req.BuildID, Commit: t.commit, Since: now().UTC().Format(time.RFC3339)})
	if err != nil {
		return errors.Wrap(err, "json.Marshal()")
	}
	held, err := locks.Lock(ctx, t.p.StateBucket, LockObject, data)
	if errors.Is(err, ErrLocked) {
		var other lock
		_ = json.Unmarshal(held, &other)

		return errors.Newf("another apply holds the lock gs://%s/%s (build %s at %s, since %s): wait for it; a run that died leaves the lock, and deleting the object frees it", t.p.StateBucket, LockObject, other.Build, short(other.Commit), other.Since)
	}
	if err != nil {
		return err
	}
	defer func() { _ = locks.Unlock(ctx, t.p.StateBucket, LockObject) }()
	report, err := runAndReport(ctx, clients, &Request{Placement: t.p, Dir: req.Dir, Mode: Apply, Commit: t.commit, GitHubToken: infra}, out)
	if err != nil {
		// The hosting organizations an apply created before it stopped stand, and their
		// hand-off pull requests wait: they are named as a finished apply names them. A
		// stopped apply leaves no marker: the next push applies again.
		var stopped *StoppedError
		if errors.As(err, &stopped) {
			stopped.Report.writeCreated(out)
		}

		return err
	}
	report.writeCreated(out)

	return WriteMarker(ctx, clients, t.p, t.commit, report.Applied)
}

// runAndReport runs the engine and prints its report: a finished run's whole (Write), and
// before the error of a run that stopped in a section what it had gathered, which the
// error carries (WriteStopped). It answers what Run answers.
func runAndReport(ctx context.Context, clients *Clients, req *Request, out io.Writer) (*Report, error) {
	report, err := Run(ctx, clients, req, out)
	if err != nil {
		var stopped *StoppedError
		if errors.As(err, &stopped) {
			stopped.Report.WriteStopped(out, req.Mode)
		}

		return nil, err
	}
	report.Write(out, req.Mode)

	return report, nil
}

// writeCreated names the hosting organizations the apply created, or found created, each
// with its hand-off pull request.
func (r *Report) writeCreated(w io.Writer) {
	for _, c := range r.Created {
		if c.Already {
			fmt.Fprintf(w, "%s was created by an earlier run; its hand-off pull request: %s\n", c.Prefix, c.PullRequest)
		} else {
			fmt.Fprintf(w, "Created %s; its hand-off pull request: %s\n", c.Prefix, c.PullRequest)
		}
	}
}

// GitHubClient is the GitHub client over a token, the engine's default.
func GitHubClient(token string) *github.Client {
	return github.New(github.DefaultBase, token)
}

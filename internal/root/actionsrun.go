// actionsrun.go is a run of the workflow of a root organization without a cloud: bedrock
// org plan on a pull request, which says what the merge would change and posts it;
// bedrock org apply on a merge, which makes it so; and the weekly check, which keeps one
// issue in the repository with what it found. Each acts on GitHub as the infrastructure
// app, with a token minted from the key the job read from the repository's environment.
// The workflow gives a run what a Cloud Build trigger's build gives the other kind: the
// commit, and for a plan the pull request, in the environment its steps set. One apply
// runs at a time by the workflow's concurrency group, where the other kind locks a bucket.

package root

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/org"
)

// The variables the workflow's steps set beside the key (org.InfrastructureKeySecret):
// the commit the run is of, and for a plan the pull request's number.
const (
	CommitEnv      = "BEDROCK_COMMIT"
	PullRequestEnv = "BEDROCK_PULL_REQUEST"
)

// checkIssueTitle is the title of the one issue the weekly check keeps.
const checkIssueTitle = "bedrock: the organization is not as placement.json says"

// ActionsRequest is what a job of the workflow hands a run: the checkout, the
// infrastructure app's private key, the commit and, for a plan, the pull request.
type ActionsRequest struct {
	Dir         string
	Key         []byte
	Commit      string
	PullRequest string
}

// KeyToken mints the infrastructure app's token on the organization from the key in
// hand: the one a job of the workflow read from the repository's environment.
func KeyToken(ctx context.Context, gh deploy.GitHubFunc, p *org.Placement, key []byte) (string, error) {
	if p.GithubInfrastructureAppID == "" {
		return "", errors.New("placement.json records no infrastructure app (githubInfrastructureAppId): bedrock org bootstrap makes it and records it")
	}
	token, _, err := deploy.AppOrgInstallationToken(ctx, gh, p.GithubInfrastructureAppID, key, p.GithubOrganization, org.InfrastructureApp.Title())

	return token, err
}

// actions reads the placement off the checkout and mints the run's token.
func actions(ctx context.Context, clients *Clients, req *ActionsRequest, command string) (*org.Placement, string, error) {
	p, err := org.ReadPlacement(filepath.Join(req.Dir, org.PlacementFile))
	if err != nil {
		return nil, "", err
	}
	if !p.IsRoot() || p.HasCloud() {
		return nil, "", errors.Newf("the %s organization is not a root organization without a cloud: its runs are its Cloud Build triggers'", p.Prefix)
	}
	if len(req.Key) == 0 {
		return nil, "", errors.Newf("%s is not set: bedrock org %s in the %s root organization runs from its repository's workflow (%s), whose jobs read the infrastructure app's key from the environment %s", org.InfrastructureKeySecret, command, p.Prefix, org.WorkflowFile, org.RootEnvironment)
	}
	token, err := KeyToken(ctx, clients.GitHub, p, req.Key)
	if err != nil {
		return nil, "", err
	}

	return p, token, nil
}

// ActionsPlan is bedrock org plan in the workflow: it says what the pull request's merge
// would change, in the log and in one comment on the pull request as the infrastructure
// app, and exits well whatever it found; a change that cannot be read is the error.
func ActionsPlan(ctx context.Context, clients *Clients, req *ActionsRequest, out io.Writer) error {
	p, token, err := actions(ctx, clients, req, org.PlanCommand)
	if err != nil {
		return err
	}
	number, err := strconv.Atoi(req.PullRequest)
	if err != nil || number <= 0 {
		return errors.Newf("%s is %q: the workflow's plan runs on a pull request, and its step names it there", PullRequestEnv, req.PullRequest)
	}
	report, err := Run(ctx, clients, &Request{Placement: p, Dir: req.Dir, Mode: Plan, Commit: req.Commit, GitHubToken: token}, out)
	if err != nil {
		return err
	}
	report.Write(out, Plan)

	return postPlan(ctx, clients.GitHub(token), p, number, report.Comment(req.Commit, p), org.InfrastructureApp.Title(), out)
}

// ActionsApply is bedrock org apply in the workflow: it makes the root organization as
// the merged placement says.
func ActionsApply(ctx context.Context, clients *Clients, req *ActionsRequest, out io.Writer) error {
	p, token, err := actions(ctx, clients, req, org.ApplyCommand)
	if err != nil {
		return err
	}
	report, err := Run(ctx, clients, &Request{Placement: p, Dir: req.Dir, Mode: Apply, Commit: req.Commit, GitHubToken: token}, out)
	if err != nil {
		return err
	}
	report.Write(out, Apply)

	return nil
}

// ActionsCheck is the weekly check: it compares the organization with the placement and
// keeps one issue in the repository with what it found, opened with the first finding or
// difference, given a comment by each later check that still finds one, and closed by the
// first that finds none.
func ActionsCheck(ctx context.Context, clients *Clients, req *ActionsRequest, out io.Writer) error {
	p, token, err := actions(ctx, clients, req, "check")
	if err != nil {
		return err
	}
	report, err := Run(ctx, clients, &Request{Placement: p, Dir: req.Dir, Mode: Check, Commit: req.Commit, GitHubToken: token}, out)
	if err != nil {
		return err
	}
	report.Write(out, Check)
	gh := clients.GitHub(token)
	owner, repo := p.GithubOrganization, p.SourceRepo
	open, err := gh.OpenIssue(ctx, owner, repo, checkIssueTitle)
	if err != nil {
		return errors.Wrapf(err, "looking for the check's issue in %s/%s", owner, repo)
	}
	switch {
	case report.Clean() && open == nil:
		return nil
	case report.Clean():
		if err := gh.CreateComment(ctx, owner, repo, open.Number, "The check at "+short(req.Commit)+" found the organization as placement.json says."); err != nil {
			return err
		}
		if err := gh.CloseIssue(ctx, owner, repo, open.Number); err != nil {
			return err
		}
		fmt.Fprintf(out, "Closed issue #%d: the organization stands as placement.json says.\n", open.Number)
	case open == nil:
		issue, err := gh.CreateIssue(ctx, owner, repo, checkIssueTitle, report.Issue(req.Commit, p))
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Opened issue #%d with what the check found: %s\n", issue.Number, issue.HTMLURL)
	default:
		if err := gh.CreateComment(ctx, owner, repo, open.Number, report.Issue(req.Commit, p)); err != nil {
			return err
		}
		fmt.Fprintf(out, "Added what the check found to issue #%d.\n", open.Number)
	}

	return nil
}

// Issue is what the weekly check found, as the issue says it: what an apply would
// change, which is a difference someone made by hand since the last merge, and the
// findings, which no apply changes.
func (r *Report) Issue(commit string, p *org.Placement) string {
	var b strings.Builder
	b.WriteString("The check of the " + p.Prefix + " root organization at " + short(commit) + " found:\n")
	if len(r.Changes) > 0 {
		b.WriteString("\nDifferences from placement.json, which the next apply removes (a merge into " + p.GithubDefaultBranch + ", or the workflow's apply run again):\n\n")
		for _, c := range r.Changes {
			b.WriteString("- " + c.Section + ": the apply " + c.What + "\n")
		}
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

	return b.String()
}

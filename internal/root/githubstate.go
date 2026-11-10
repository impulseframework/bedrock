// githubstate.go is the root organization's repository and its GitHub organization's
// settings as the engine keeps them: the repository's merge settings and the ruleset on
// its default branch, the organization's larger runner, its group and the variable that
// names it. Read and written with the infrastructure app's token, or a person's.

package root

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/go-playground/errors/v5"
	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// githubSection brings the repository and the organization's Actions settings to the
// placement, or notes that GitHub was not read.
func (e *engine) githubSection(ctx context.Context) error {
	if e.gh == nil {
		e.note("GitHub was not checked: no token (GITHUB_TOKEN, or the infrastructure app's in a trigger's run)")

		return nil
	}
	owner, repo := e.p.GithubOrganization, e.p.SourceRepo
	if _, err := e.gh.Repository(ctx, owner, repo); err != nil {
		if github.NotFound(err) {
			e.note(fmt.Sprintf("the repository %s/%s does not exist yet: the bootstrap makes it, and its rules wait", owner, repo))

			return nil
		}

		return errors.Wrapf(err, "reading %s/%s", owner, repo)
	}
	if err := e.repositorySettings(ctx, sectionGitHub, owner, repo); err != nil {
		return err
	}
	if err := e.ruleset(ctx, owner, repo); err != nil {
		return err
	}

	return e.runner(ctx)
}

// mergeSettings says what the merge settings are, in a sentence's end.
const mergeSettings = "squash alone, titled and described from the pull request, the branch deleted on merge, the branch update offered"

// repositorySettings brings the merge settings to squash alone, titled and bodied from
// the pull request, the head branch deleted, the branch update offered.
func (e *engine) repositorySettings(ctx context.Context, section, owner, repo string) error {
	got, err := e.gh.RepositorySettings(ctx, owner, repo)
	if err != nil {
		return errors.Wrapf(err, "reading the settings of %s/%s", owner, repo)
	}
	want := e.d.RepositorySettings()
	if cmp.Equal(got, want) {
		return nil
	}

	return e.change(ctx, section, "sets the merge settings of "+owner+"/"+repo+": "+mergeSettings, func(ctx context.Context) error {
		return e.gh.UpdateRepositorySettings(ctx, owner, repo, want)
	})
}

// ruleset makes the default branch's ruleset or brings it to the placement.
func (e *engine) ruleset(ctx context.Context, owner, repo string) error {
	reporter, err := e.gh.AppBySlug(ctx, e.d.PlanReporterSlug())
	if err != nil {
		return errors.Wrapf(err, "reading the app that reports the plan (%s)", e.d.PlanReporterSlug())
	}
	infrastructure, err := e.appID(org.InfrastructureApp)
	if err != nil {
		return err
	}
	plan := "the plan trigger's build"
	if !e.p.HasCloud() {
		plan = "the workflow's plan (" + org.PlanJobName + ")"
	}
	makes := fmt.Sprintf("rules the default branch of %s/%s: pull requests alone with %d approving review(s), squash merges, %s required, the infrastructure app bypassing", owner, repo, e.p.ApprovingReviewCount(), plan)

	return e.bringRuleset(ctx, sectionGitHub, owner, repo, e.d.Ruleset(reporter.ID, infrastructure), makes)
}

// appID is the App ID the placement records for the role as a number, zero while it
// records none.
func (e *engine) appID(role org.AppRole) (int64, error) {
	id := e.p.AppID(role)
	if id == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return 0, errors.Newf("the App ID placement.json records for %s, %q, is not a number", role.Title(), id)
	}

	return n, nil
}

// bringRuleset makes the repository's ruleset on its default branch, in the sentence
// makes, or brings the one that stands to the one wanted.
func (e *engine) bringRuleset(ctx context.Context, section, owner, repo string, want *github.Ruleset, makes string) error {
	listed, err := e.gh.Rulesets(ctx, owner, repo)
	if err != nil {
		return errors.Wrapf(err, "listing the rulesets of %s/%s", owner, repo)
	}
	i := slices.IndexFunc(listed, func(rs github.Ruleset) bool { return rs.Name == rulesetName })
	if i < 0 {
		return e.change(ctx, section, makes, func(ctx context.Context) error {
			_, err := e.gh.CreateRuleset(ctx, owner, repo, want)

			return err
		})
	}
	got, err := e.gh.Ruleset(ctx, owner, repo, listed[i].ID)
	if err != nil {
		return errors.Wrapf(err, "reading the ruleset %s of %s/%s", rulesetName, owner, repo)
	}
	want.ID = got.ID
	if diff := rulesetDiff(got, want, "branches"); diff != "" {
		return e.change(ctx, section, "brings the default branch's ruleset of "+owner+"/"+repo+" to the placement: "+diff, func(ctx context.Context) error {
			return e.gh.UpdateRuleset(ctx, owner, repo, got.ID, want)
		})
	}

	return nil
}

// rulesetDiff says what differs between the ruleset that stands and the one wanted:
// the bypass actors, the conditions (named as conditions names them: branches, or tags
// and repositories) or the rules, empty for nothing.
func rulesetDiff(got, want *github.Ruleset, conditions string) string {
	var parts []string
	if got.Enforcement != want.Enforcement || got.Target != want.Target {
		parts = append(parts, "enforcement")
	}
	if !cmp.Equal(got.BypassActors, want.BypassActors, cmp.Transformer("empty", emptyActors)) {
		parts = append(parts, "bypass actors")
	}
	if !cmp.Equal(got.Conditions, want.Conditions, cmp.Transformer("empty", emptySlice)) {
		parts = append(parts, conditions)
	}
	if kinds := rulesDiffer(got.Rules, want.Rules); len(kinds) > 0 {
		parts = append(parts, "rules ("+strings.Join(kinds, ", ")+")")
	}

	return joined(parts)
}

// rulesDiffer compares the rules that stand with the ones wanted, by type, so that order
// does not count, and names the kinds that differ, sorted: a rule wanted and missing, a
// rule standing that is not wanted, or a parameter declared whose value differs. A
// parameter GitHub adds beyond the declaration (required_reviewers, dismissal_restriction
// and the like, which it fills in with defaults on every ruleset it stores; the lab,
// 2026-11-06) does not count.
func rulesDiffer(got, want []github.Rule) []string {
	g, w := ruleMap(got), ruleMap(want)
	var kinds []string
	for kind := range g {
		if _, ok := w[kind]; !ok {
			kinds = append(kinds, kind)
		}
	}
	for kind, wanted := range w {
		standing, ok := g[kind]
		if !ok {
			kinds = append(kinds, kind)

			continue
		}
		for key, value := range wanted {
			if !cmp.Equal(standing[key], value) {
				kinds = append(kinds, kind)

				break
			}
		}
	}
	slices.Sort(kinds)

	return kinds
}

func emptyActors(a []github.BypassActor) []github.BypassActor {
	if a == nil {
		return []github.BypassActor{}
	}

	return a
}

// ruleMap keys the rules by type with their parameters, for rulesDiffer.
func ruleMap(rules []github.Rule) map[string]map[string]any {
	m := map[string]map[string]any{}
	for _, r := range rules {
		params := map[string]any{}
		for k, v := range r.Parameters {
			params[k] = v
		}
		m[r.Type] = params
	}

	return m
}

// runnerVariable brings the organization variable that names the larger runner to the
// placement: set to the name, or removed when the placement names none.
func (e *engine) runnerVariable(ctx context.Context) error {
	owner := e.p.GithubOrganization
	want := e.d.RunnerVariable()
	got, err := e.gh.OrgVariable(ctx, owner, runnerVariable)
	if err != nil && !github.NotFound(err) {
		return errors.Wrapf(err, "reading the %s variable of %s", runnerVariable, owner)
	}
	switch {
	case want == nil && got != nil:
		if err := e.change(ctx, sectionGitHub, "removes the organization variable "+runnerVariable+" (the workflows' jobs run on GitHub's standard runner)", func(ctx context.Context) error {
			return e.gh.DeleteOrgVariable(ctx, owner, runnerVariable)
		}); err != nil {
			return err
		}
	case want != nil && got == nil:
		if err := e.change(ctx, sectionGitHub, "sets the organization variable "+runnerVariable+" to "+want.Value, func(ctx context.Context) error {
			return e.gh.CreateOrgVariable(ctx, owner, *want)
		}); err != nil {
			return err
		}
	case want != nil && (got.Value != want.Value || got.Visibility != want.Visibility):
		if err := e.change(ctx, sectionGitHub, "sets the organization variable "+runnerVariable+" to "+want.Value+" (was "+got.Value+")", func(ctx context.Context) error {
			return e.gh.UpdateOrgVariable(ctx, owner, *want)
		}); err != nil {
			return err
		}
	}

	return nil
}

// runnerGroup is the id of the runner group the larger runner belongs to, made when it is
// missing and opened to public repositories, or closed to them, as the placement's
// libraries call for; 0 in a plan that would make it.
func (e *engine) runnerGroup(ctx context.Context) (int64, error) {
	owner := e.p.GithubOrganization
	groups, err := e.gh.RunnerGroups(ctx, owner)
	if err != nil {
		return 0, errors.Wrapf(err, "listing the runner groups of %s", owner)
	}
	var groupID int64
	public := e.d.RunnerGroupPublic()
	if i := slices.IndexFunc(groups, func(g github.RunnerGroup) bool { return g.Name == runnerGroupName }); i >= 0 {
		groupID = groups[i].ID
		if groups[i].AllowsPublicRepositories != public {
			what := "closes the runner group " + runnerGroupName + " to public repositories (placement.json lists no public library)"
			if public {
				what = "opens the runner group " + runnerGroupName + " to public repositories (placement.json lists a public library, whose jobs the group refuses until then)"
			}
			if err := e.change(ctx, sectionGitHub, what, func(ctx context.Context) error {
				return e.gh.OpenRunnerGroup(ctx, owner, groupID, public)
			}); err != nil {
				return 0, err
			}
		}
	} else {
		what := "makes the runner group " + runnerGroupName + ", open to every repository"
		if public {
			what += ", the public ones too"
		}
		if err := e.change(ctx, sectionGitHub, what, func(ctx context.Context) error {
			g, err := e.gh.CreateRunnerGroup(ctx, owner, github.RunnerGroup{Name: runnerGroupName, Visibility: github.VisibilityAll, AllowsPublicRepositories: public})
			if err == nil {
				groupID = g.ID
			}

			return err
		}); err != nil {
			return 0, err
		}
	}

	return groupID, nil
}

// runner brings the organization's larger runner to the placement: the variable that
// names it, and when the placement sizes it, the group and the runner.
func (e *engine) runner(ctx context.Context) error {
	if err := e.runnerVariable(ctx); err != nil {
		return err
	}
	hosted := e.d.HostedRunner()
	if hosted == nil {
		return nil
	}
	owner := e.p.GithubOrganization
	groupID, err := e.runnerGroup(ctx)
	if err != nil {
		return err
	}
	hosted.RunnerGroupID = groupID
	runners, err := e.gh.HostedRunners(ctx, owner)
	if err != nil {
		return errors.Wrapf(err, "listing the hosted runners of %s", owner)
	}
	i := slices.IndexFunc(runners, func(r github.HostedRunner) bool { return r.Name == hosted.Name })
	if i < 0 {
		return e.change(ctx, sectionGitHub, fmt.Sprintf("makes the larger runner %s (%s, at most %d at once) in the group %s", hosted.Name, hosted.Size, hosted.MaximumRunners, runnerGroupName), func(ctx context.Context) error {
			_, err := e.gh.CreateHostedRunner(ctx, owner, *hosted)

			return err
		})
	}
	got2 := runners[i]
	if got2.MachineSize != nil && got2.MachineSize.ID != hosted.Size {
		e.finding(fmt.Sprintf("the larger runner %s is a %s, and placement.json sizes it %s (ciLargeRunnerSize): a runner's size cannot change in place; delete the runner on GitHub and let the apply make it again", hosted.Name, got2.MachineSize.ID, hosted.Size))
	}
	if groupID != 0 && (got2.RunnerGroupID != groupID || got2.MaximumRunners != hosted.MaximumRunners) {
		return e.change(ctx, sectionGitHub, fmt.Sprintf("moves the larger runner %s into the group %s with at most %d at once", hosted.Name, runnerGroupName, hosted.MaximumRunners), func(ctx context.Context) error {
			return e.gh.UpdateHostedRunner(ctx, owner, got2.ID, groupID, hosted.MaximumRunners)
		})
	}

	return nil
}

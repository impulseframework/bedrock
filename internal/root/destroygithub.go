// destroygithub.go is bedrock org destroy for a root organization without a cloud. There
// is no project to delete: the destroy takes off GitHub what bedrock keeps there for the
// organization as a whole, and leaves what is somebody's work. Gone are the release app's
// credentials (the two organization secrets and the variable that names its login), the
// organization's ruleset on version tags, the infrastructure app's key in the root
// repository's environment, and the plan the root repository's ruleset requires, since
// nothing can run it once the key is gone. The repositories stay, the root's and every
// library's, with their code, pull requests, settings and rulesets, and the apps stay
// installed, for a person to delete. The organization's policy on immutable releases
// stays as it is too: bedrock turns it on and never off.

package root

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// destroyWithoutCloud takes the root organization's own keeping off GitHub. It looks
// before each removal, so a destroy run again finds nothing to do.
func destroyWithoutCloud(ctx context.Context, clients *Clients, req *DestroyRequest, out io.Writer) error {
	p := req.Placement
	owner, repo := p.GithubOrganization, p.SourceRepo
	if req.GithubToken == nil || clients.GitHub == nil {
		return errors.Newf("the %s root organization is kept on GitHub alone, and its destroy needs a token there: an owner's of %s (GITHUB_TOKEN, or gh's), or the infrastructure app's, minted from a key of it (--infrastructure-key)", p.Prefix, owner)
	}
	token, err := req.GithubToken(ctx)
	if err != nil || token == "" {
		return errors.Newf("the %s root organization is kept on GitHub alone, and its destroy needs a token there: an owner's of %s (GITHUB_TOKEN, or gh's), or the infrastructure app's, minted from a key of it (--infrastructure-key)", p.Prefix, owner)
	}
	gh := clients.GitHub(token)
	for _, name := range []string{org.ReleaseKeyOrgSecret, org.ReleaseClientIDOrgSecret} {
		switch _, err := gh.OrgSecret(ctx, owner, name); {
		case github.NotFound(err):
			fmt.Fprintf(out, "The organization secret %s is gone already.\n", name)
		case err != nil:
			return errors.Wrapf(err, "reading the organization secret %s of %s", name, owner)
		default:
			if err := gh.DeleteOrgSecret(ctx, owner, name); err != nil {
				return errors.Wrapf(err, "deleting the organization secret %s of %s", name, owner)
			}
			fmt.Fprintf(out, "Deleted the organization secret %s.\n", name)
		}
	}
	switch _, err := gh.OrgVariable(ctx, owner, org.ReleaseLoginVariable); {
	case github.NotFound(err):
	case err != nil:
		return errors.Wrapf(err, "reading the %s variable of %s", org.ReleaseLoginVariable, owner)
	default:
		if err := gh.DeleteOrgVariable(ctx, owner, org.ReleaseLoginVariable); err != nil {
			return errors.Wrapf(err, "deleting the %s variable of %s", org.ReleaseLoginVariable, owner)
		}
		fmt.Fprintf(out, "Deleted the organization variable %s.\n", org.ReleaseLoginVariable)
	}
	listed, err := gh.OrgRulesets(ctx, owner)
	if err != nil {
		return errors.Wrapf(err, "listing the rulesets of the %s organization", owner)
	}
	if i := slices.IndexFunc(listed, func(rs github.Ruleset) bool { return rs.Name == tagRulesetName }); i >= 0 {
		if err := gh.DeleteOrgRuleset(ctx, owner, listed[i].ID); err != nil {
			return errors.Wrapf(err, "deleting the ruleset %s of the %s organization", tagRulesetName, owner)
		}
		fmt.Fprintf(out, "Deleted the organization's ruleset %q: the libraries' version tags are ruled by nothing now.\n", tagRulesetName)
	}
	switch _, err := gh.EnvironmentSecret(ctx, owner, repo, org.RootEnvironment, org.InfrastructureKeySecret); {
	case github.NotFound(err):
		fmt.Fprintf(out, "The environment %s of %s/%s holds no key already.\n", org.RootEnvironment, owner, repo)
	case err != nil:
		return errors.Wrapf(err, "reading the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, org.RootEnvironment, owner, repo)
	default:
		if err := gh.DeleteEnvironmentSecret(ctx, owner, repo, org.RootEnvironment, org.InfrastructureKeySecret); err != nil {
			return errors.Wrapf(err, "deleting the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, org.RootEnvironment, owner, repo)
		}
		fmt.Fprintf(out, "Deleted the infrastructure app's key from the environment %s of %s/%s: the repository's workflow acts as nobody now.\n", org.RootEnvironment, owner, repo)
	}

	return dropPlanCheck(ctx, clients, req, out)
}

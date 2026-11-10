// bootstrapgithub.go is bedrock org bootstrap for a root organization without a cloud:
// one command run by an owner of the GitHub organization, with no Google credentials and
// no token. It makes the GitHub Apps the placement calls for in the browser, and does
// everything else as the infrastructure app, with a token minted from the key GitHub
// answered the app's making with: the repository, then the engine's apply, which seals
// each key into its GitHub secret, then the first commit, and the check. A key is held
// in memory by the run that made its app and by no other, since GitHub never gives a
// sealed key back: a run again acts with a key generated anew, brought with
// --infrastructure-app and --infrastructure-key.

package root

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// withoutCloud is the bootstrap of a root organization without a cloud.
func (b *bootstrap) withoutCloud(ctx context.Context) (*BootstrapResult, error) {
	if err := b.checkWithoutCloud(); err != nil {
		return nil, err
	}
	if err := b.save(); err != nil {
		return nil, err
	}
	keys, err := b.githubApps(ctx)
	if err != nil {
		return nil, err
	}
	// The apps' ids are recorded before anything else is made: a run that stops after
	// this finds them in the directory and makes no second app.
	if err := b.save(); err != nil {
		return nil, err
	}
	token, err := b.infrastructureToken(ctx, keys)
	if err != nil {
		return nil, err
	}
	gh := b.clients.GitHub(token)
	fmt.Fprintf(b.out, "=== the repository %s/%s ===\n", b.p.GithubOrganization, b.p.SourceRepo)
	if err := b.repositoryStands(ctx, gh); err != nil {
		return nil, err
	}
	// The apply comes before the first commit: it seals the keys this run holds, makes
	// the environment the workflow's jobs run in and rules the branch, so the workflow
	// the first commit carries starts in an organization that is ready for it.
	fmt.Fprintf(b.out, "=== making the root organization as placement.json says ===\n")
	applied, err := Run(ctx, b.clients, &Request{Placement: b.p, Dir: b.req.Dir, Mode: Apply, GitHubToken: token, Bootstrap: true, SealKeys: keys}, b.out)
	if err != nil {
		return nil, err
	}
	applied.Write(b.out, Apply)
	commit, err := b.foundation(ctx, gh, "chore: the "+b.p.Prefix+" root organization, bootstrapped by bedrock org bootstrap\n\nRendered from placement.json. The workflow in "+org.WorkflowFile+" plans every pull request into "+b.p.GithubDefaultBranch+" and makes the organization as the placement says on every merge.")
	if err != nil {
		return nil, err
	}
	cloned := b.clone(ctx, commit, token)
	fmt.Fprintf(b.out, "=== checking ===\n")
	report, err := Run(ctx, b.clients, &Request{Placement: b.p, Dir: b.req.Dir, Mode: Check, Commit: commit, GitHubToken: token}, b.out)
	if err != nil {
		return nil, err
	}
	report.Write(b.out, Check)
	fmt.Fprintf(b.out, "Bootstrapped the %s root organization on GitHub alone: repository %s/%s at %s; the apps are %s.\n", b.p.Prefix, b.p.GithubOrganization, b.p.SourceRepo, short(commit), b.appIDs())

	return &BootstrapResult{Placement: b.p, Commit: commit, Cloned: cloned, Report: report}, nil
}

// checkWithoutCloud refuses a draft the bootstrap cannot start from, and one that names
// an app the organization does not call for.
func (b *bootstrap) checkWithoutCloud() error {
	if b.clients.GitHub == nil {
		return errors.New("the bootstrap's GitHub client is not wired")
	}
	if b.p.Prefix == "" {
		return errors.New("the bootstrap's placement lacks prefix")
	}
	if err := b.p.Validate(); err != nil {
		return errors.Wrap(err, "the bootstrap's placement")
	}
	called := b.p.Apps()
	for role := range b.req.Brought {
		if !slices.Contains(called, role) {
			return errors.Newf("--%s-app names %s, which the %s root organization does not call for: without a cloud it acts as its infrastructure app, and as its release app once placement.json lists a library", role, role.Title(), b.p.Prefix)
		}
	}

	return nil
}

// githubApps brings each app the placement calls for, and answers the keys this run
// holds: a brought app is held to its manifest and recorded with its key; an app the
// placement records already is left as it is, and this run holds no key of it; the rest
// are made in the browser, each with the key GitHub answers its making with.
func (b *bootstrap) githubApps(ctx context.Context) (map[org.AppRole][]byte, error) {
	fmt.Fprintf(b.out, "=== the GitHub Apps ===\n")
	keys := map[org.AppRole][]byte{}
	var toMake []org.AppManifest
	for _, m := range b.p.AppManifests() {
		brought, ok := b.req.Brought[m.Role]
		recorded := b.p.AppID(m.Role)
		switch {
		case ok && recorded != "" && recorded != brought.ID:
			return nil, errors.Newf("placement.json records %s as App %s, and --%s-app names App %s: bring the app the placement records, with a key generated on its settings page", m.Role.Title(), recorded, m.Role, brought.ID)
		case ok:
			app, err := b.broughtApp(ctx, m, brought)
			if err != nil {
				return nil, err
			}
			b.p.SetApp(m.Role, app)
			keys[m.Role] = brought.Key
			fmt.Fprintf(b.out, "%s: App %s (%s), brought with its key, holds what bedrock declares and is installed on %s.\n", m.Role.Title(), brought.ID, app.Slug, b.p.GithubOrganization)
		case recorded != "":
			fmt.Fprintf(b.out, "%s is App %s (recorded); this run holds no key of it.\n", m.Role.Title(), recorded)
		default:
			toMake = append(toMake, m)
		}
	}
	made, err := b.makeApps(ctx, toMake, "The keys go from GitHub into their GitHub secrets and never touch this computer's disk.")
	if err != nil {
		return nil, err
	}
	for _, m := range toMake {
		app := made[m.Role]
		b.p.SetApp(m.Role, app)
		keys[m.Role] = []byte(app.PEM)
		fmt.Fprintf(b.out, "%s: made App %d (%s) and installed it on %s.\n", m.Role.Title(), app.ID, app.Slug, b.p.GithubOrganization)
	}

	return keys, nil
}

// broughtApp holds an app the organization has already to its manifest, by the key
// brought with it, and answers its record.
func (b *bootstrap) broughtApp(ctx context.Context, m org.AppManifest, brought BroughtApp) (*github.App, error) {
	findings, err := VerifyApp(ctx, b.clients.GitHub, m, brought.ID, brought.Key, b.p.GithubOrganization)
	if err != nil {
		return nil, errors.Wrapf(err, "%s, brought as App %s", m.Role.Title(), brought.ID)
	}
	if len(findings) > 0 {
		return nil, errors.Newf("%s, brought as App %s, is not as bedrock declares it: %s", m.Role.Title(), brought.ID, strings.Join(findings, "; "))
	}

	return deploy.AppIdentity(ctx, b.clients.GitHub, brought.ID, brought.Key)
}

// infrastructureToken mints the infrastructure app's token on the organization from the
// key this run holds, and says how to bring one when it holds none.
func (b *bootstrap) infrastructureToken(ctx context.Context, keys map[org.AppRole][]byte) (string, error) {
	key := keys[org.InfrastructureApp]
	if len(key) == 0 {
		return "", errors.Newf("placement.json records the infrastructure app (App %s), and this run holds no key of it: GitHub never gives a sealed key back, and the bootstrap acts as the app. On the app's settings page (https://github.com/organizations/%s/settings/apps), generate a private key, and run again with --infrastructure-app %s --infrastructure-key <the downloaded file>; then delete the file", b.p.GithubInfrastructureAppID, b.p.GithubOrganization, b.p.GithubInfrastructureAppID)
	}
	token, _, err := deploy.AppOrgInstallationToken(ctx, b.clients.GitHub, b.p.GithubInfrastructureAppID, key, b.p.GithubOrganization, org.InfrastructureApp.Title())

	return token, err
}

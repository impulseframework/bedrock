// sealkey.go is what a person does with a GitHub App's key in a root organization without
// a cloud, where the keys are GitHub secrets: seal a key generated anew into its secret
// (the rotation, and the first sealing after a destroy), and read where each key is and
// when it was last sealed. GitHub never gives a secret's value back, so nothing here
// reads a key: a new key is proven by signing as the app before it is sealed, and the
// report names the secret, its readers and its date.

package root

import (
	"context"
	"fmt"
	"io"
	"slices"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// SealRequest is one key to seal: the placement, the app, its private key in PEM, and
// the token the secret is written with; without one the infrastructure app's own key
// mints it, since the key that is being sealed signs as the app.
type SealRequest struct {
	Placement *org.Placement
	Role      org.AppRole
	Key       []byte
	Token     string
}

// SealKey proves the key signs as the app the placement records for the role, and seals
// it into the app's secret: the infrastructure app's into the secret of the root
// repository's environment, the release app's into the organization secret the libraries
// alone read, with the app's client ID beside it.
func SealKey(ctx context.Context, clients *Clients, req *SealRequest, out io.Writer) error {
	p, role := req.Placement, req.Role
	if !p.IsRoot() || p.HasCloud() {
		return errors.Newf("the %s organization's keys are not GitHub secrets: a key is sealed into one in a root organization without a cloud alone", p.Prefix)
	}
	if !slices.Contains(p.Apps(), role) {
		return errors.Newf("the %s root organization does not call for %s: without a cloud it acts as its infrastructure app, and as its release app once placement.json lists a library", p.Prefix, role.Title())
	}
	appID := p.AppID(role)
	if appID == "" {
		return errors.Newf("placement.json records no App ID for %s: bedrock org bootstrap makes the app and records it", role.Title())
	}
	app, err := deploy.AppIdentity(ctx, clients.GitHub, appID, req.Key)
	if err != nil {
		return errors.Wrapf(err, "the key does not sign as %s (App %s), so it is not sealed", role.Title(), appID)
	}
	owner, repo := p.GithubOrganization, p.SourceRepo
	token := req.Token
	if token == "" && role == org.InfrastructureApp {
		if token, err = KeyToken(ctx, clients.GitHub, p, req.Key); err != nil {
			return err
		}
	}
	if token == "" {
		return errors.Newf("sealing %s's key needs a token that writes the organization's secrets: an owner's of %s (GITHUB_TOKEN, or gh's), or the infrastructure app's, minted from a key of it (--%s)", role.Title(), owner, "infrastructure-key")
	}
	gh := clients.GitHub(token)
	settings := "https://github.com/organizations/" + owner + "/settings/apps/" + app.Slug
	if role == org.InfrastructureApp {
		if err := gh.PutEnvironmentSecret(ctx, owner, repo, org.RootEnvironment, org.InfrastructureKeySecret, req.Key); err != nil {
			if github.NotFound(err) {
				return errors.Newf("%s/%s has no environment %s to seal the key into: bedrock org bootstrap, or the workflow's apply, makes it", owner, repo, org.RootEnvironment)
			}

			return errors.Wrapf(err, "sealing the key into the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, org.RootEnvironment, owner, repo)
		}
		fmt.Fprintf(out, "Sealed the key of %s (App %s, %s) into the secret %s of the environment %s of %s/%s: the repository's workflow acts with it from its next run.\nDelete the file, then delete the app's old key on %s.\n", role.Title(), appID, app.Slug, org.InfrastructureKeySecret, org.RootEnvironment, owner, repo, settings)

		return nil
	}
	var ids []int64
	for _, name := range p.LibraryNames() {
		library, err := gh.Repository(ctx, owner, name)
		switch {
		case github.NotFound(err):
			continue
		case err != nil:
			return errors.Wrapf(err, "reading %s/%s", owner, name)
		}
		ids = append(ids, library.ID)
	}
	slices.Sort(ids)
	if err := gh.PutOrgSecret(ctx, owner, org.ReleaseKeyOrgSecret, req.Key, ids); err != nil {
		return errors.Wrapf(err, "sealing the key into the organization secret %s of %s", org.ReleaseKeyOrgSecret, owner)
	}
	if err := gh.PutOrgSecret(ctx, owner, org.ReleaseClientIDOrgSecret, []byte(app.ClientID), ids); err != nil {
		return errors.Wrapf(err, "setting the organization secret %s of %s", org.ReleaseClientIDOrgSecret, owner)
	}
	fmt.Fprintf(out, "Sealed the key of %s (App %s, %s) into the organization secret %s, and its client ID into %s, both read by the libraries alone (%s): a library's release workflow acts with it from its next run.\nDelete the file, then delete the app's old key on %s.\n", role.Title(), appID, app.Slug, org.ReleaseKeyOrgSecret, org.ReleaseClientIDOrgSecret, joined(p.LibraryNames()), settings)

	return nil
}

// sealedAt is how the report writes the time a secret was last sealed.
const sealedAt = "2006-01-02 15:04 UTC"

// KeysReport prints where the key of each app the placement calls for is, who reads it
// and when it was last sealed, and answers whether every one is in its place.
func KeysReport(ctx context.Context, gh *github.Client, p *org.Placement, out io.Writer) (bool, error) {
	owner, repo := p.GithubOrganization, p.SourceRepo
	complete := true
	for _, role := range p.Apps() {
		app := role.Title()
		if id := p.AppID(role); id != "" {
			app += " (App " + id + ")"
		}
		if role == org.InfrastructureApp {
			secret, err := gh.EnvironmentSecret(ctx, owner, repo, org.RootEnvironment, org.InfrastructureKeySecret)
			switch {
			case github.NotFound(err):
				complete = false
				fmt.Fprintf(out, "%s: no key is sealed. Its place is the secret %s of the environment %s of %s/%s; bedrock secret add %s --from-file <the file> seals one.\n", app, org.InfrastructureKeySecret, org.RootEnvironment, owner, repo, org.InfrastructureKey)
			case err != nil:
				return false, errors.Wrapf(err, "reading the secret %s of the environment %s of %s/%s", org.InfrastructureKeySecret, org.RootEnvironment, owner, repo)
			default:
				fmt.Fprintf(out, "%s: its key is the secret %s of the environment %s of %s/%s, last sealed %s; the workflow's jobs on %s alone read it.\n", app, org.InfrastructureKeySecret, org.RootEnvironment, owner, repo, secret.UpdatedAt.UTC().Format(sealedAt), p.GithubDefaultBranch)
			}

			continue
		}
		secret, err := gh.OrgSecret(ctx, owner, org.ReleaseKeyOrgSecret)
		switch {
		case github.NotFound(err):
			complete = false
			fmt.Fprintf(out, "%s: no key is sealed. Its place is the organization secret %s of %s; bedrock secret add %s --from-file <the file> seals one.\n", app, org.ReleaseKeyOrgSecret, owner, org.ReleaseKey)
		case err != nil:
			return false, errors.Wrapf(err, "reading the organization secret %s of %s", org.ReleaseKeyOrgSecret, owner)
		case secret.Visibility != github.VisibilitySelected:
			complete = false
			fmt.Fprintf(out, "%s: its key is the organization secret %s of %s, last sealed %s, and %s repositories read it, not the libraries alone: seal it again (bedrock secret add %s --from-file <the file>), which limits it to them.\n", app, org.ReleaseKeyOrgSecret, owner, secret.UpdatedAt.UTC().Format(sealedAt), secret.Visibility, org.ReleaseKey)
		default:
			readers, err := gh.OrgSecretRepositories(ctx, owner, org.ReleaseKeyOrgSecret)
			if err != nil {
				return false, errors.Wrapf(err, "listing the repositories that read the organization secret %s of %s", org.ReleaseKeyOrgSecret, owner)
			}
			names := make([]string, 0, len(readers))
			for _, reader := range readers {
				names = append(names, reader.Name)
			}
			slices.Sort(names)
			fmt.Fprintf(out, "%s: its key is the organization secret %s of %s, last sealed %s; read by %s.\n", app, org.ReleaseKeyOrgSecret, owner, secret.UpdatedAt.UTC().Format(sealedAt), joined(names))
		}
	}

	return complete, nil
}

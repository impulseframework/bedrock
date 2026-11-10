// orgsecrets.go is bedrock org secrets, the root organization's commands on the secrets it
// holds for every organization, the GitHub Apps' keys and the Bot Account's token: check,
// which proves a version with GitHub before bedrock secret pin and the pull request's apply
// move the alias to it (README.md, "The GitHub Apps' keys").

package cli

import (
	"context"
	"strings"

	"github.com/go-playground/errors/v5"
	"github.com/spf13/cobra"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
	rootorg "github.com/impulseframework/bedrock/internal/root"
)

func newOrgSecrets(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "The GitHub Apps' keys and the Bot Account's token a root organization holds for every organization",
		Long: `secrets is the root organization's view of the four secrets it holds for every
organization in the Google Cloud organization: the infrastructure, deployer and release GitHub
Apps' private keys, one secret per app in its root project, each read through its alias
current by every pipeline in every organization, and the GitHub Bot Account's token
(github-token), read through its alias current by every Cloud Build connection. A new value is
added as a version with bedrock secret add, proven with secrets check, and pinned with bedrock
secret pin, whose pull request's apply moves the alias.`,
	}
	cmd.AddCommand(newOrgSecretsCheck(d))

	return cmd
}

func newOrgSecretsCheck(d deps) *cobra.Command {
	var dir, key string

	cmd := &cobra.Command{
		Use:   "check <secret> <version>",
		Short: "Prove a version of a GitHub App's key, or of the Bot Account's token, with GitHub before the alias moves to it",
		Long: `check proves a version of a GitHub App's key before anything reads it: the version must be
enabled in Secret Manager, and a token signed with it, issued by the App ID placement.json
records for the app (githubInfrastructureAppId, githubDeployerAppId, githubReleaseAppId),
must be accepted by GitHub as that app, which GitHub does for the app's own key alone. It
reads the version with the run's Google credentials (gcloud auth application-default login,
as a key operator placement.json names) and prints the app GitHub answered with, the version
the alias current is pinned to, and the pin to make. Nothing moves: the alias stays where
placement.json pins it until bedrock secret pin and the pull request's apply. A version that
is not the app's key, or an App ID that is another app's, is refused with GitHub's answer.

For github-token, the Bot Account's token, the version must be enabled, a personal access
token (one a GitHub App issued is refused by its prefix), and GitHub must say it is the Bot
Account's (githubBotAccount) and the account an owner of the GitHub organization; it prints
that, the version the alias is pinned to, and the pin to make.

It runs in the root organization's infrastructure repository; a hosting organization's names
the root organization instead.

In the repository of a root organization without a cloud the keys are GitHub secrets, which
carry no version and are never read back, and check takes no argument: it says where each
app's key is (the infrastructure app's in the repository's bedrock environment, the release
app's in the organization secret the libraries read), who reads it and when it was last
sealed, and exits 1 when one is missing or read more widely than it should be. It reads
GitHub with GITHUB_TOKEN or gh's token, or as the infrastructure app (--infrastructure-key,
or the key the workflow's job hands it). A new key is proven when it is sealed: bedrock secret
add refuses one that does not sign as the app.`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, p, err := d.orgRepository(dir)
			if err != nil {
				return err
			}
			if p == nil {
				return errors.New("not in an organization's infrastructure repository (no placement.json naming an organization at its root): run secrets check at the root organization's root, or pass --dir")
			}
			if err := p.KeysElsewhere(); err != nil {
				return err
			}
			if !p.HasCloud() {
				return d.keysReport(cmd, args, p, key)
			}
			if len(args) != 2 || key != "" {
				return errors.Newf("secrets check takes a secret and a version here (bedrock org secrets check <secret> <version>), and no --%s: this organization's keys are versions in Secret Manager", infrastructureKeyFlag)
			}
			project, container, err := orgSecretPlace(p, &pinFlags{}, args[0])
			if err != nil {
				return err
			}
			req := &org.KeyCheckRequest{Secret: args[0], Version: args[1], Project: project, Container: container}
			if args[0] == org.Token {
				r, err := org.CheckToken(cmd.Context(), d.secrets, d.keyAccess, d.tokenIdentity, p, req)
				if err != nil {
					return err
				}
				r.Write(cmd.OutOrStdout())

				return nil
			}
			r, err := org.CheckKey(cmd.Context(), d.secrets, d.keyAccess, d.appIdentity, p, req)
			if err != nil {
				return err
			}
			r.Write(cmd.OutOrStdout())

			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "the repository root (default: found from the working directory)")
	cmd.Flags().StringVar(&key, infrastructureKeyFlag, "", "a root organization without a cloud: the file holding a private key (PEM) of the infrastructure app, to read GitHub as the app in place of an owner's token")
	_ = cmd.MarkFlagFilename(infrastructureKeyFlag)

	return cmd
}

// keysReport is secrets check in the repository of a root organization without a cloud:
// where each app's key is, who reads it and when it was last sealed.
func (d deps) keysReport(cmd *cobra.Command, args []string, p *org.Placement, key string) error {
	if len(args) > 0 {
		return errors.Newf("secrets check takes no argument in the %s root organization: its keys are GitHub secrets, which carry no version; it reports each key's place (given %s)", p.Prefix, strings.Join(args, " "))
	}
	clients := d.rootOrg()
	if clients == nil {
		return errors.New("no root organization clients are wired")
	}
	var (
		token, why string
		err        error
	)
	if key != "" {
		if token, err = d.keyToken(p, key)(cmd.Context()); err != nil {
			return err
		}
	} else if token, why = d.rootGitHubToken(cmd.Context(), p); token == "" {
		return errors.Newf("no GitHub token to read the keys' places with (%s): set GITHUB_TOKEN or sign in with gh as an owner of %s, or pass --%s", why, p.GithubOrganization, infrastructureKeyFlag)
	}
	complete, err := rootorg.KeysReport(cmd.Context(), clients.GitHub(token), p, cmd.OutOrStdout())
	if err != nil {
		return err
	}
	if !complete {
		return exitError{code: 1}
	}

	return nil
}

// keyAccess reads a key's version through the deploy clients' Secret Manager.
func (d deps) keyAccess(ctx context.Context, version string) ([]byte, error) {
	if d.deploy == nil || d.deploy.Secrets == nil {
		return nil, errors.New("no Secret Manager client is wired for the read")
	}
	secrets, err := d.deploy.Secrets(ctx)
	if err != nil {
		return nil, err
	}
	defer secrets.Close()

	return secrets.Access(ctx, version)
}

// tokenIdentity asks GitHub whose a token is and that the account owns the organization,
// through the deploy clients' GitHub.
func (d deps) tokenIdentity(ctx context.Context, token, organization string) (string, error) {
	if d.deploy == nil || d.deploy.GitHub == nil {
		return "", errors.New("no GitHub client is wired for the check")
	}

	return github.BotAccount(ctx, d.deploy.GitHub, token, organization)
}

// appIdentity asks GitHub which app a key signs as, through the deploy clients' GitHub.
func (d deps) appIdentity(ctx context.Context, appID string, pemKey []byte) (*org.AppIdentity, error) {
	if d.deploy == nil || d.deploy.GitHub == nil {
		return nil, errors.New("no GitHub client is wired for the check")
	}
	app, err := deploy.AppIdentity(ctx, d.deploy.GitHub, appID, pemKey)
	if err != nil {
		return nil, err
	}

	return &org.AppIdentity{ID: app.ID, Slug: app.Slug, Name: app.Name}, nil
}

// librarysecrets.go keeps the release app's credentials where a library's release
// workflow reads them: two organization secrets, the app's client ID and its private key,
// which the libraries' repositories alone read. GitHub never gives a secret's value back,
// so the engine compares what it can: that each secret exists, that its readers are the
// libraries and no other repository, and, in a root organization with a cloud, that the
// key was sealed from the version of it the placement pins.

package root

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// releaseSecrets keeps the two organization secrets: sealed when they are missing and the
// run can reach their values, their readers brought to the libraries listed. A repository
// that reads the key and is no longer listed is a library taken off the list: a finding,
// and the apply takes the key from it; the repository itself is never deleted here. The
// readers are narrowed whether or not the placement still records the release app; a
// sealing needs the app.
func (e *engine) releaseSecrets(ctx context.Context) error {
	key, err := e.orgSecret(ctx, org.ReleaseKeyOrgSecret)
	if err != nil {
		return err
	}
	clientID, err := e.orgSecret(ctx, org.ReleaseClientIDOrgSecret)
	if err != nil {
		return err
	}
	if len(e.p.Libraries) == 0 && key == nil && clientID == nil {
		return nil
	}
	var (
		seal          *keySource
		reportMissing bool
	)
	recorded := e.p.GithubReleaseAppID != ""
	if recorded {
		if seal, reportMissing, err = e.releaseKeySource(ctx, key); err != nil {
			return err
		}
	} else if len(e.p.Libraries) > 0 {
		e.note("the release app's key is not kept for the libraries yet: placement.json records no release app")
	}
	switch {
	case seal != nil:
		if err := e.change(ctx, sectionLibraries, fmt.Sprintf("seals %s into the organization secret %s, which the libraries alone read (%s)", seal.what, org.ReleaseKeyOrgSecret, joined(e.readerNames())), func(ctx context.Context) error {
			pem, err := seal.read(ctx)
			if err != nil {
				return err
			}
			if err := e.gh.PutOrgSecret(ctx, e.p.GithubOrganization, org.ReleaseKeyOrgSecret, pem, e.readerIDs()); err != nil {
				return err
			}

			return seal.sealed(ctx)
		}); err != nil {
			return err
		}
	case key == nil:
		if reportMissing {
			e.finding(fmt.Sprintf("the organization secret %s does not exist, and a library's release workflow mints the release app's token from it: generate a private key on the app's settings page and seal it with bedrock secret add %s --from-file <the file>", org.ReleaseKeyOrgSecret, org.ReleaseKey))
		}
	default:
		if err := e.secretReaders(ctx, key, true); err != nil {
			return err
		}
	}
	switch {
	case recorded && (clientID == nil || clientID.Visibility != github.VisibilitySelected || seal != nil):
		return e.change(ctx, sectionLibraries, fmt.Sprintf("sets the organization secret %s to the release app's client ID, which the libraries alone read", org.ReleaseClientIDOrgSecret), func(ctx context.Context) error {
			id, err := e.releaseClientID(ctx, seal)
			if err != nil {
				return err
			}

			return e.gh.PutOrgSecret(ctx, e.p.GithubOrganization, org.ReleaseClientIDOrgSecret, []byte(id), e.readerIDs())
		})
	case clientID != nil && clientID.Visibility == github.VisibilitySelected:
		return e.secretReaders(ctx, clientID, false)
	}

	return nil
}

// releaseClientID is the release app's client ID: the app's record, read by its slug.
// GitHub gives a private app's record to some tokens and not to others, so a run that
// seals the key, and therefore holds it, asks as the app itself when the slug's read is
// refused: the key signs as the app, and GitHub answers the app with its own record.
func (e *engine) releaseClientID(ctx context.Context, seal *keySource) (string, error) {
	app, err := e.gh.AppBySlug(ctx, e.p.ReleaseAppSlug())
	if err == nil {
		return app.ClientID, nil
	}
	if seal != nil {
		pem, rerr := seal.read(ctx)
		if rerr != nil {
			return "", rerr
		}
		if own, kerr := deploy.AppIdentity(ctx, e.clients.GitHub, e.p.GithubReleaseAppID, pem); kerr == nil {
			return own.ClientID, nil
		}
	}

	return "", errors.Wrapf(err, "reading the release app's record (%s) for its client ID; bedrock secret add %s --from-file <the file> seals the app's key and sets the client ID with it", e.p.ReleaseAppSlug(), org.ReleaseKey)
}

// orgSecret reads the organization's secret, nil when there is none.
func (e *engine) orgSecret(ctx context.Context, name string) (*github.OrgSecret, error) {
	secret, err := e.gh.OrgSecret(ctx, e.p.GithubOrganization, name)
	switch {
	case github.NotFound(err):
		return nil, nil
	case err != nil:
		return nil, errors.Wrapf(err, "reading the organization secret %s of %s", name, e.p.GithubOrganization)
	}

	return secret, nil
}

// keySource is where a run takes the release app's key from to seal it: what it is, in
// words, the read, and what records the sealing once it is done.
type keySource struct {
	what   string
	read   func(ctx context.Context) ([]byte, error)
	sealed func(ctx context.Context) error
}

// releaseKeySource says whether this run seals the release app's key, and from where;
// stands is the secret as it is, nil when there is none. Without a cloud the key is the
// one the run was handed (the bootstrap's, or a key generated anew), sealed whenever one
// is; without one, a missing secret is for a person to seal (reportMissing). With a cloud
// the key is the version the placement pins in Secret Manager, sealed when the secret is
// missing, when it was sealed from another version, which an organization variable
// records since the secret itself says nothing, or when more than the libraries read it,
// which a sealing alone narrows.
func (e *engine) releaseKeySource(ctx context.Context, stands *github.OrgSecret) (source *keySource, reportMissing bool, err error) {
	owner := e.p.GithubOrganization
	if !e.p.HasCloud() {
		pem := e.req.SealKeys[org.ReleaseApp]
		if len(pem) == 0 {
			return nil, true, nil
		}

		return &keySource{
			what:   "the release app's key",
			read:   func(context.Context) ([]byte, error) { return pem, nil },
			sealed: func(context.Context) error { return nil },
		}, false, nil
	}
	pinned := e.p.KeyVersion(org.ReleaseKey)
	if pinned == "" {
		e.note(fmt.Sprintf("the release app's key is not sealed for the libraries: placement.json pins no version of it (%s)", org.ReleaseKey))

		return nil, false, nil
	}
	recorded, err := e.gh.OrgVariable(ctx, owner, org.ReleaseKeyVersionVariable)
	if err != nil && !github.NotFound(err) {
		return nil, false, errors.Wrapf(err, "reading the %s variable of %s", org.ReleaseKeyVersionVariable, owner)
	}
	if stands != nil && stands.Visibility == github.VisibilitySelected && err == nil && recorded.Value == pinned {
		return nil, false, nil
	}
	want := github.Variable{Name: org.ReleaseKeyVersionVariable, Value: pinned, Visibility: github.VisibilityPrivate}

	return &keySource{
		what: "version " + pinned + " of the release app's key",
		read: func(ctx context.Context) ([]byte, error) {
			return e.secrets.AccessSecretVersion(ctx, "projects/"+e.d.Project()+"/secrets/"+e.p.SecretContainer(org.ReleaseKey)+"/versions/"+pinned)
		},
		sealed: func(ctx context.Context) error {
			if recorded == nil {
				return e.gh.CreateOrgVariable(ctx, owner, want)
			}

			return e.gh.UpdateOrgVariable(ctx, owner, want)
		},
	}, false, nil
}

// readerNames are the libraries that read the release app's secrets: the ones listed,
// less an adopted one that does not stand, which no apply creates.
func (e *engine) readerNames() []string {
	names := make([]string, 0, len(e.p.Libraries))
	for _, l := range e.p.Libraries {
		if _, stands := e.libraryIDs[l.Repository]; l.Adopted && !stands {
			continue
		}
		names = append(names, l.Repository)
	}

	return names
}

// readerIDs are the ids of the libraries' repositories that stand, in order: the
// repositories an organization secret is read by.
func (e *engine) readerIDs() []int64 {
	ids := make([]int64, 0, len(e.libraryIDs))
	for _, name := range e.p.LibraryNames() {
		if id, stands := e.libraryIDs[name]; stands {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	return ids
}

// secretReaders brings the repositories that read the secret to the libraries. A secret
// every repository reads, or every private one, cannot be narrowed in place: GitHub takes
// a secret's visibility with its value alone, so it is sealed again. report says whether
// a reader that is no library is reported, which is done once, for the key.
func (e *engine) secretReaders(ctx context.Context, secret *github.OrgSecret, report bool) error {
	owner := e.p.GithubOrganization
	if secret.Visibility != github.VisibilitySelected {
		e.finding(fmt.Sprintf("the organization secret %s is read by %s repositories of %s, not by the libraries alone: seal it again, which limits it to them (bedrock secret add %s --from-file <the file>)", secret.Name, secret.Visibility, owner, org.ReleaseKey))

		return nil
	}
	readers, err := e.gh.OrgSecretRepositories(ctx, owner, secret.Name)
	if err != nil {
		return errors.Wrapf(err, "listing the repositories that read the organization secret %s of %s", secret.Name, owner)
	}
	// A name is compared whatever its case, as GitHub finds a repository: a library the
	// placement spells in another case than GitHub is a finding of its own (library).
	listed := e.readerNames()
	var adds, removes []string
	for _, name := range listed {
		if !slices.ContainsFunc(readers, func(r github.Repository) bool { return strings.EqualFold(r.Name, name) }) {
			adds = append(adds, name)
		}
	}
	for _, reader := range readers {
		if slices.ContainsFunc(e.p.LibraryNames(), func(name string) bool { return strings.EqualFold(name, reader.Name) }) {
			continue
		}
		removes = append(removes, reader.Name)
		if report {
			e.finding(fmt.Sprintf("%s/%s reads the release app's key (the organization secret %s) and placement.json lists no such library: a library taken off the list is archived by a person, never deleted here, and the apply takes the key from it", owner, reader.Name, secret.Name))
		}
	}
	if len(adds) == 0 && len(removes) == 0 {
		return nil
	}
	sort.Strings(removes)
	what := "brings the repositories that read the organization secret " + secret.Name + " to the libraries:"
	if len(adds) > 0 {
		what += " adds " + joined(adds)
	}
	if len(removes) > 0 {
		if len(adds) > 0 {
			what += ";"
		}
		what += " takes " + joined(removes) + " off"
	}

	return e.change(ctx, sectionLibraries, what, func(ctx context.Context) error {
		return e.gh.SetOrgSecretRepositories(ctx, owner, secret.Name, e.readerIDs())
	})
}

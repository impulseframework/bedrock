package github_test

import (
	"slices"
	"testing"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestOrgImmutableReleases: an organization that never set a policy holds none of its
// repositories to immutable releases; enforcing sets the policy to every repository, and
// again changes nothing; a policy an owner narrowed to selected repositories reads as
// selected. An organization the token may not administer answers not found to the read
// and to the set, and the policy stays as it was.
func TestOrgImmutableReleases(t *testing.T) {
	t.Parallel()

	srv := githubtest.New(t)
	c := srv.Client()
	ctx := t.Context()
	if got, err := c.OrgImmutableReleases(ctx, "acme"); err != nil || got != github.ImmutableReleasesNone {
		t.Fatalf("OrgImmutableReleases() of an organization with no policy = %q, %v, want %q", got, err, github.ImmutableReleasesNone)
	}
	for range 2 {
		if err := c.EnforceImmutableReleases(ctx, "acme"); err != nil {
			t.Fatalf("EnforceImmutableReleases() error = %v", err)
		}
		if got, err := c.OrgImmutableReleases(ctx, "acme"); err != nil || got != github.ImmutableReleasesAll {
			t.Fatalf("OrgImmutableReleases() after enforcing = %q, %v, want %q", got, err, github.ImmutableReleasesAll)
		}
	}
	if got, err := c.OrgImmutableReleases(ctx, "another"); err != nil || got != github.ImmutableReleasesNone {
		t.Errorf("OrgImmutableReleases() of another organization = %q, %v, want %q", got, err, github.ImmutableReleasesNone)
	}
	srv.Update(func() { srv.ImmutableReleases["acme"] = github.ImmutableReleasesSelected })
	if got, err := c.OrgImmutableReleases(ctx, "acme"); err != nil || got != github.ImmutableReleasesSelected {
		t.Errorf("OrgImmutableReleases() of a narrowed policy = %q, %v, want %q", got, err, github.ImmutableReleasesSelected)
	}

	srv.Update(func() { srv.ImmutableReleasesHidden["acme"] = true })
	if got, err := c.OrgImmutableReleases(ctx, "acme"); !github.NotFound(err) {
		t.Errorf("OrgImmutableReleases() without the organization's administration = %q, %v, want not found", got, err)
	}
	if err := c.EnforceImmutableReleases(ctx, "acme"); !github.NotFound(err) {
		t.Errorf("EnforceImmutableReleases() without the organization's administration = %v, want not found", err)
	}
	srv.Update(func() {
		if got := srv.ImmutableReleases["acme"]; got != github.ImmutableReleasesSelected {
			t.Errorf("the policy after a refused set = %q, want it as it was", got)
		}
		if want := "PUT /orgs/acme/settings/immutable-releases"; !slices.Contains(srv.Calls, want) {
			t.Errorf("no %s among the calls: %v", want, srv.Calls)
		}
	})
}

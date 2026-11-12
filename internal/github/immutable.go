// immutable.go is the organization's policy on immutable releases: which of its
// repositories GitHub holds to them. In a repository held to them, a release that is
// published keeps its tag at the commit it names and its assets as they were uploaded;
// neither is changed or deleted after, by anyone. The policy binds the releases published
// after it is set and none published before.

package github

import (
	"context"
	"net/http"
)

// What an organization's policy on immutable releases holds to them: every repository of
// the organization, none of them (each repository's own setting decides), or the ones an
// owner selected.
const (
	ImmutableReleasesAll      = "all"
	ImmutableReleasesNone     = "none"
	ImmutableReleasesSelected = "selected"
)

// OrgImmutableReleases reads which repositories the organization's policy holds to
// immutable releases: all, none or selected (GET /orgs/{org}/settings/immutable-releases,
// which takes read access to the organization's Administration: GitHub names the
// permission in its answer, X-Accepted-Github-Permissions:
// organization_administration=read). A token with that access is answered the policy and
// a token without it not found (read 2026-11-12: an owner is answered
// {"enforced_repositories":"none"}, a member who is no owner 404), so NotFound here says
// the token cannot read the policy, never that the policy is off.
func (c *Client) OrgImmutableReleases(ctx context.Context, org string) (string, error) {
	var out struct {
		Enforced string `json:"enforced_repositories"`
	}
	if err := c.do(ctx, http.MethodGet, "/orgs/"+org+"/settings/immutable-releases", nil, &out); err != nil {
		return "", err
	}

	return out.Enforced, nil
}

// EnforceImmutableReleases sets the organization's policy to every repository (PUT
// /orgs/{org}/settings/immutable-releases, which takes write access to the organization's
// Administration): the repositories it has and the ones made after. There is no call
// here that narrows the policy or turns it off: bedrock never does.
func (c *Client) EnforceImmutableReleases(ctx context.Context, org string) error {
	in := struct {
		Enforced string `json:"enforced_repositories"`
	}{Enforced: ImmutableReleasesAll}

	return c.do(ctx, http.MethodPut, "/orgs/"+org+"/settings/immutable-releases", in, nil)
}

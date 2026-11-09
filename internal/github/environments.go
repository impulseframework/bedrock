// environments.go is a repository's deployment environments: the names a workflow's job
// deploys to, each with the branches GitHub lets deploy to it and the Actions secrets only
// a job deploying to it reads. An environment made here takes deployments from the
// branches its patterns name and from no other.

package github

import (
	"context"
	"net/http"
	"net/url"
	"time"
)

// Environment is a deployment environment of a repository: its name and which branches
// may deploy to it. BranchPolicy is nil for an environment any branch may deploy to.
type Environment struct {
	Name         string        `json:"name"`
	BranchPolicy *BranchPolicy `json:"deployment_branch_policy"`
}

// BranchPolicy is how an environment limits the branches that deploy to it: to the
// repository's protected branches, or to the branches its own patterns name
// (EnvironmentBranches). GitHub takes one of the two, never both.
type BranchPolicy struct {
	ProtectedBranches    bool `json:"protected_branches"`
	CustomBranchPolicies bool `json:"custom_branch_policies"`
}

// branchPolicyType is the type of a branch pattern, as against a tag's.
const branchPolicyType = "branch"

// Environment reads the repository's environment; an absent one is NotFound.
func (c *Client) Environment(ctx context.Context, owner, repo, name string) (*Environment, error) {
	out := &Environment{}
	if err := c.do(ctx, http.MethodGet, environmentPath(owner, repo, name), nil, out); err != nil {
		return nil, err
	}

	return out, nil
}

// PutEnvironment makes the repository's environment, or sets the one there is, as an
// environment only the branches its patterns name may deploy to: none until
// AddEnvironmentBranch names one.
func (c *Client) PutEnvironment(ctx context.Context, owner, repo, name string) error {
	in := struct {
		BranchPolicy BranchPolicy `json:"deployment_branch_policy"`
	}{BranchPolicy: BranchPolicy{ProtectedBranches: false, CustomBranchPolicies: true}}

	return c.do(ctx, http.MethodPut, environmentPath(owner, repo, name), in, nil)
}

// EnvironmentBranches lists the name patterns of the branches that may deploy to the
// environment (master, release/*), up to a hundred; tags' patterns are left out
// (EnvironmentPatterns answers both). An absent environment is NotFound.
func (c *Client) EnvironmentBranches(ctx context.Context, owner, repo, name string) ([]string, error) {
	branches, _, err := c.EnvironmentPatterns(ctx, owner, repo, name)

	return branches, err
}

// EnvironmentPatterns lists the name patterns of what may deploy to the environment, up
// to a hundred: the branches' (master, release/*) and the tags' (v*). A workflow that runs
// on a ref either list matches is handed the environment's secrets, so a caller that
// keeps a secret there reads both. An absent environment is NotFound.
func (c *Client) EnvironmentPatterns(ctx context.Context, owner, repo, name string) (branches, tags []string, err error) {
	var out struct {
		Policies []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"branch_policies"`
	}
	if err := c.do(ctx, http.MethodGet, environmentPath(owner, repo, name)+"/deployment-branch-policies?per_page=100", nil, &out); err != nil {
		return nil, nil, err
	}
	branches = make([]string, 0, len(out.Policies))
	for _, policy := range out.Policies {
		if policy.Type == "" || policy.Type == branchPolicyType {
			branches = append(branches, policy.Name)
		} else {
			tags = append(tags, policy.Name)
		}
	}

	return branches, tags, nil
}

// AddEnvironmentBranch lets the branches the pattern names deploy to the environment.
// GitHub documents two refusals worth knowing: 404 for an environment that is absent or
// does not keep patterns of its own (PutEnvironment makes it one that does), and 303 for a
// pattern the environment already has, which the client, following no redirect, returns
// as an error Moved reports; so a caller adds only what EnvironmentBranches lacks.
func (c *Client) AddEnvironmentBranch(ctx context.Context, owner, repo, name, pattern string) error {
	in := struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}{Name: pattern, Type: branchPolicyType}

	return c.do(ctx, http.MethodPost, environmentPath(owner, repo, name)+"/deployment-branch-policies", in, nil)
}

// EnvironmentSecret is an Actions secret of a deployment environment as the API describes
// it: its name and when its value was last written. The value itself is never read back.
type EnvironmentSecret struct {
	Name      string    `json:"name"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EnvironmentSecret reads the environment's secret, without its value; an absent one is
// NotFound, and so is any secret of an environment the repository lacks.
func (c *Client) EnvironmentSecret(ctx context.Context, owner, repo, environment, name string) (*EnvironmentSecret, error) {
	out := &EnvironmentSecret{}
	if err := c.do(ctx, http.MethodGet, environmentPath(owner, repo, environment)+"/secrets/"+name, nil, out); err != nil {
		return nil, err
	}

	return out, nil
}

// PutEnvironmentSecret makes the environment's secret, or replaces its value. It reads
// the environment's public key and sends the value sealed with it, as PutOrgSecret does
// an organization's, so the value crosses the wire readable by GitHub alone. The value is
// in no error this returns.
func (c *Client) PutEnvironmentSecret(ctx context.Context, owner, repo, environment, name string, value []byte) error {
	secrets := environmentPath(owner, repo, environment) + "/secrets/"
	sealed, err := c.seal(ctx, secrets+"public-key", value)
	if err != nil {
		return err
	}

	return c.do(ctx, http.MethodPut, secrets+name, sealed, nil)
}

// DeleteEnvironmentSecret removes the environment's secret.
func (c *Client) DeleteEnvironmentSecret(ctx context.Context, owner, repo, environment, name string) error {
	return c.do(ctx, http.MethodDelete, environmentPath(owner, repo, environment)+"/secrets/"+name, nil, nil)
}

// environmentPath is the environment's path; GitHub wants the name URL encoded.
func environmentPath(owner, repo, name string) string {
	return "/repos/" + owner + "/" + repo + "/environments/" + url.PathEscape(name)
}

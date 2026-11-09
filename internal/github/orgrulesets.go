// orgrulesets.go is the GitHub organization's own rulesets: rules that hold on the
// branches or the tags of every repository the ruleset's conditions name, whatever each
// repository's own rulesets say. They are the Ruleset of rulesets.go, with the
// repositories named in its conditions (RepositoryName).

package github

import (
	"context"
	"fmt"
	"net/http"
)

// OrgRulesets lists the organization's rulesets, each with its id, name, target and
// enforcement, up to a hundred; the rules are read one ruleset at a time (OrgRuleset).
func (c *Client) OrgRulesets(ctx context.Context, org string) ([]Ruleset, error) {
	var out []Ruleset
	if err := c.do(ctx, http.MethodGet, "/orgs/"+org+"/rulesets?per_page=100", nil, &out); err != nil {
		return nil, err
	}

	return out, nil
}

// OrgRuleset reads one of the organization's rulesets whole: its actors, conditions and
// rules. An id the organization has no ruleset under is NotFound.
func (c *Client) OrgRuleset(ctx context.Context, org string, id int64) (*Ruleset, error) {
	out := &Ruleset{}
	if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/orgs/%s/rulesets/%d", org, id), nil, out); err != nil {
		return nil, err
	}

	return out, nil
}

// CreateOrgRuleset makes the ruleset on the organization and answers it with its id.
// GitHub wants the repositories named beside the branches or tags in the conditions of a
// ruleset on either (Conditions.RepositoryName).
func (c *Client) CreateOrgRuleset(ctx context.Context, org string, in *Ruleset) (*Ruleset, error) {
	out := &Ruleset{}
	if err := c.do(ctx, http.MethodPost, "/orgs/"+org+"/rulesets", in, out); err != nil {
		return nil, err
	}

	return out, nil
}

// UpdateOrgRuleset replaces the organization's ruleset with the id by what in says.
func (c *Client) UpdateOrgRuleset(ctx context.Context, org string, id int64, in *Ruleset) error {
	return c.do(ctx, http.MethodPut, fmt.Sprintf("/orgs/%s/rulesets/%d", org, id), in, nil)
}

// DeleteOrgRuleset removes the organization's ruleset with the id; its rules stop holding
// on every repository it named.
func (c *Client) DeleteOrgRuleset(ctx context.Context, org string, id int64) error {
	return c.do(ctx, http.MethodDelete, fmt.Sprintf("/orgs/%s/rulesets/%d", org, id), nil, nil)
}

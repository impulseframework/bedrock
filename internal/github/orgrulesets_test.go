package github_test

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestOrgRulesets: an organization's ruleset is made whole, with the repositories it
// names, listed by its summary, read back whole, replaced and removed; an id the
// organization has no ruleset under is not found, whatever is asked of it.
func TestOrgRulesets(t *testing.T) {
	t.Parallel()

	srv := githubtest.New(t)
	c := srv.Client()
	ctx := t.Context()
	if listed, err := c.OrgRulesets(ctx, "acme"); err != nil || len(listed) != 0 {
		t.Fatalf("OrgRulesets() of an organization with none = %+v, %v", listed, err)
	}
	want := &github.Ruleset{
		Name:         "release tags",
		Target:       github.RulesetTargetTag,
		Enforcement:  github.RulesetActive,
		BypassActors: []github.BypassActor{{ActorID: 5177065, ActorType: github.BypassIntegration, BypassMode: github.BypassAlways}},
		Conditions: &github.RulesetConditions{
			RefName:        github.RefNameCondition{Include: []string{"refs/tags/v*"}, Exclude: []string{}},
			RepositoryName: &github.RepositoryNameCondition{Include: []string{"acme-*"}, Exclude: []string{"acme-sandbox"}},
		},
		Rules: []github.Rule{{Type: "deletion"}, {Type: "non_fast_forward"}, {Type: "update"}},
	}
	made, err := c.CreateOrgRuleset(ctx, "acme", want)
	if err != nil {
		t.Fatalf("CreateOrgRuleset() error = %v", err)
	}
	if made.ID == 0 {
		t.Fatal("CreateOrgRuleset() gave no id")
	}
	other, err := c.CreateOrgRuleset(ctx, "acme", &github.Ruleset{Name: "default branches", Target: github.RulesetTargetBranch, Enforcement: github.RulesetActive})
	if err != nil || other.ID == 0 || other.ID == made.ID {
		t.Fatalf("CreateOrgRuleset() of a second = %+v, %v, want an id of its own", other, err)
	}
	listed, err := c.OrgRulesets(ctx, "acme")
	if err != nil {
		t.Fatalf("OrgRulesets() error = %v", err)
	}
	wantListed := []github.Ruleset{
		{ID: made.ID, Name: "release tags", Target: github.RulesetTargetTag, Enforcement: github.RulesetActive},
		{ID: other.ID, Name: "default branches", Target: github.RulesetTargetBranch, Enforcement: github.RulesetActive},
	}
	if diff := cmp.Diff(wantListed, listed); diff != "" {
		t.Errorf("OrgRulesets() mismatch, want the summaries alone (-want +got):\n%s", diff)
	}
	if others, err := c.OrgRulesets(ctx, "another"); err != nil || len(others) != 0 {
		t.Errorf("OrgRulesets() of another organization = %+v, %v, want none", others, err)
	}
	whole, err := c.OrgRuleset(ctx, "acme", made.ID)
	if err != nil {
		t.Fatalf("OrgRuleset() error = %v", err)
	}
	want.ID = made.ID
	if diff := cmp.Diff(want, whole); diff != "" {
		t.Errorf("OrgRuleset() mismatch (-want +got):\n%s", diff)
	}

	want.Rules = want.Rules[:2]
	want.Conditions.RepositoryName.Include = []string{"~ALL"}
	if err := c.UpdateOrgRuleset(ctx, "acme", made.ID, want); err != nil {
		t.Fatalf("UpdateOrgRuleset() error = %v", err)
	}
	whole, err = c.OrgRuleset(ctx, "acme", made.ID)
	if err != nil {
		t.Fatalf("OrgRuleset() after the update error = %v", err)
	}
	if diff := cmp.Diff(want, whole); diff != "" {
		t.Errorf("OrgRuleset() after the update mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(want, srv.OrgRulesets["acme"][0]); diff != "" {
		t.Errorf("the stand-in's ruleset mismatch (-want +got):\n%s", diff)
	}

	if err := c.DeleteOrgRuleset(ctx, "acme", made.ID); err != nil {
		t.Fatalf("DeleteOrgRuleset() error = %v", err)
	}
	if len(srv.OrgRulesets["acme"]) != 1 || srv.OrgRulesets["acme"][0].ID != other.ID {
		t.Errorf("after the removal the stand-in holds %+v, want the second ruleset alone", srv.OrgRulesets["acme"])
	}
	if _, err := c.OrgRuleset(ctx, "acme", made.ID); !github.NotFound(err) {
		t.Errorf("OrgRuleset() of a removed ruleset = %v, want not found", err)
	}
	if err := c.UpdateOrgRuleset(ctx, "acme", made.ID, want); !github.NotFound(err) {
		t.Errorf("UpdateOrgRuleset() of an unknown id = %v, want not found", err)
	}
	if err := c.DeleteOrgRuleset(ctx, "acme", made.ID); !github.NotFound(err) {
		t.Errorf("DeleteOrgRuleset() of an unknown id = %v, want not found", err)
	}
}

// TestRulesetJSON: a ruleset is sent as GitHub reads it. A repository's ruleset, which
// names no repository, is sent as it was before the conditions could name one: with no
// repository_name key.
func TestRulesetJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   any
		want string
	}{
		{
			name: "the conditions of a branch ruleset carry the branches alone",
			in:   github.RulesetConditions{RefName: github.RefNameCondition{Include: []string{github.DefaultBranchPattern}, Exclude: []string{}}},
			want: `{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}}`,
		},
		{
			name: "a repository's branch ruleset is unchanged",
			in: &github.Ruleset{
				Name:         "default branch",
				Target:       github.RulesetTargetBranch,
				Enforcement:  github.RulesetActive,
				BypassActors: []github.BypassActor{{ActorID: 5177065, ActorType: github.BypassIntegration, BypassMode: github.BypassAlways}},
				Conditions:   &github.RulesetConditions{RefName: github.RefNameCondition{Include: []string{github.DefaultBranchPattern}, Exclude: []string{}}},
				Rules:        []github.Rule{{Type: "deletion"}, {Type: "pull_request", Parameters: map[string]any{"required_approving_review_count": 1}}},
			},
			want: `{"name":"default branch","target":"branch","enforcement":"active","bypass_actors":[{"actor_id":5177065,"actor_type":"Integration","bypass_mode":"always"}],"conditions":{"ref_name":{"include":["~DEFAULT_BRANCH"],"exclude":[]}},"rules":[{"type":"deletion"},{"type":"pull_request","parameters":{"required_approving_review_count":1}}]}`,
		},
		{
			name: "an organization's tag ruleset names its repositories beside the tags",
			in: &github.Ruleset{
				Name:         "release tags",
				Target:       github.RulesetTargetTag,
				Enforcement:  github.RulesetActive,
				BypassActors: []github.BypassActor{},
				Conditions: &github.RulesetConditions{
					RefName:        github.RefNameCondition{Include: []string{"refs/tags/v*"}, Exclude: []string{}},
					RepositoryName: &github.RepositoryNameCondition{Include: []string{"~ALL"}, Exclude: []string{}},
				},
				Rules: []github.Rule{{Type: "deletion"}},
			},
			want: `{"name":"release tags","target":"tag","enforcement":"active","bypass_actors":[],"conditions":{"ref_name":{"include":["refs/tags/v*"],"exclude":[]},"repository_name":{"include":["~ALL"],"exclude":[]}},"rules":[{"type":"deletion"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.Marshal(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("JSON = %s\nwant   %s", got, tt.want)
			}
		})
	}
}

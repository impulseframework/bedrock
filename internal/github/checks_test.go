package github_test

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestCheckRuns: the check runs of a commit are listed by its SHA or by a branch or tag
// that names it, every page of them, each with its conclusion and the app that reported
// it.
func TestCheckRuns(t *testing.T) {
	t.Parallel()

	build := github.CheckRun{Name: "build", Status: "completed", Conclusion: "success", App: github.CheckApp{ID: 15368, Slug: "github-actions"}}
	plan := github.CheckRun{Name: "plan", Status: "in_progress", App: github.CheckApp{ID: 4242, Slug: "acme-deployer"}}
	many := make([]github.CheckRun, 0, 230)
	for i := range 230 {
		many = append(many, github.CheckRun{Name: fmt.Sprintf("check %03d", i), Status: "completed", Conclusion: "success", App: github.CheckApp{ID: 15368, Slug: "github-actions"}})
	}
	tests := []struct {
		name string
		repo string
		ref  string
		want []github.CheckRun
		// wantPages is how many pages the listing reads.
		wantPages int
		notFound  bool
	}{
		{name: "by the commit's SHA", repo: "quill", ref: "c2", want: []github.CheckRun{build, plan}, wantPages: 1},
		{name: "by a branch, resolved to its commit", repo: "quill", ref: "master", want: []github.CheckRun{build, plan}, wantPages: 1},
		{name: "by a branch as GitHub's documentation writes it", repo: "quill", ref: "heads/master", want: []github.CheckRun{build, plan}, wantPages: 1},
		{name: "by a tag, resolved to its commit", repo: "quill", ref: "v0.1.0", want: []github.CheckRun{build}, wantPages: 1},
		{name: "by a branch the runs are kept under by name", repo: "quill", ref: "next", want: []github.CheckRun{plan}, wantPages: 1},
		{name: "a commit nothing ran on has none", repo: "quill", ref: "c9", wantPages: 1},
		{name: "every page of a commit with many", repo: "quill", ref: "c3", want: many, wantPages: 3},
		{name: "a repository GitHub does not know is not found", repo: "nowhere", ref: "c2", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			srv.AddRepo("acme", "quill", &githubtest.Repo{
				Refs: map[string]github.Object{
					"refs/heads/master": {Type: "commit", SHA: "c2"},
					"refs/heads/next":   {Type: "commit", SHA: "c2"},
					"refs/tags/v0.1.0":  {Type: "commit", SHA: "c1"},
				},
				CheckRuns: map[string][]github.CheckRun{"c1": {build}, "c2": {build, plan}, "c3": many, "next": {plan}},
			})
			got, err := srv.Client().CheckRuns(t.Context(), "acme", tt.repo, tt.ref)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("CheckRuns() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("CheckRuns() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("CheckRuns() mismatch (-want +got):\n%s", diff)
			}
			if len(srv.Calls) != tt.wantPages {
				t.Errorf("the listing read %d pages, want %d: %v", len(srv.Calls), tt.wantPages, srv.Calls)
			}
			if want := "GET /repos/acme/quill/commits/" + tt.ref + "/check-runs"; srv.Calls[0] != want {
				t.Errorf("Calls[0] = %q, want %q", srv.Calls[0], want)
			}
		})
	}
}

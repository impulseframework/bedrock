package github_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestLatestPullRequest: the pull request opened last into a branch is answered whatever
// its state, with its head's commit; a branch nothing was opened into answers none.
func TestLatestPullRequest(t *testing.T) {
	t.Parallel()

	first := github.PullRequest{Number: 3, State: "closed", MergedAt: "2026-11-01T10:00:00Z", Title: "the first", HTMLURL: "https://github.com/acme/quill/pull/3", Head: github.PullRequestRef{Ref: "one", SHA: "c1"}, Base: github.PullRequestRef{Ref: "master", SHA: "c0"}}
	release := github.PullRequest{Number: 5, State: "open", Title: "a release", HTMLURL: "https://github.com/acme/quill/pull/5", Head: github.PullRequestRef{Ref: "two", SHA: "c2"}, Base: github.PullRequestRef{Ref: "release", SHA: "c0"}}
	closed := github.PullRequest{Number: 8, State: "closed", Title: "the abandoned one", HTMLURL: "https://github.com/acme/quill/pull/8", Head: github.PullRequestRef{Ref: "three", SHA: "c3"}, Base: github.PullRequestRef{Ref: "master", SHA: "c1"}}
	open := github.PullRequest{Number: 9, State: "open", Title: "the open one", HTMLURL: "https://github.com/acme/quill/pull/9", Head: github.PullRequestRef{Ref: "four", SHA: "c4"}, Base: github.PullRequestRef{Ref: "master", SHA: "c1"}}
	tests := []struct {
		name string
		repo string
		// pulls are the repository's pull requests, oldest first.
		pulls    []github.PullRequest
		base     string
		want     *github.PullRequest
		notFound bool
	}{
		{name: "a repository without a pull request has none", repo: "quill", base: "master"},
		{name: "a branch nothing was opened into has none", repo: "quill", pulls: []github.PullRequest{first, release}, base: "next"},
		{name: "the one opened last into the branch, open", repo: "quill", pulls: []github.PullRequest{first, release, closed, open}, base: "master", want: &open},
		{name: "the one opened last into the branch, closed unmerged", repo: "quill", pulls: []github.PullRequest{first, release, closed}, base: "master", want: &closed},
		{name: "the one opened last into the branch, merged", repo: "quill", pulls: []github.PullRequest{first, release}, base: "master", want: &first},
		{name: "another branch's latest is its own", repo: "quill", pulls: []github.PullRequest{first, release, closed, open}, base: "release", want: &release},
		{name: "a repository GitHub does not know is not found", repo: "nowhere", base: "master", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			srv.AddRepo("acme", "quill", &githubtest.Repo{PullRequests: tt.pulls})
			got, err := srv.Client().LatestPullRequest(t.Context(), "acme", tt.repo, tt.base)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("LatestPullRequest() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("LatestPullRequest() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("LatestPullRequest() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

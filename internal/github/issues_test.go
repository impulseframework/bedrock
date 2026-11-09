package github_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// TestOpenIssue: the open issue with exactly the title is found, on whatever page of the
// open issues it is; a closed issue, an issue whose title only begins the same and a pull
// request with the title are not it.
func TestOpenIssue(t *testing.T) {
	t.Parallel()

	const title = "The root organization has drifted"
	issue := func(number int, title, state string) *github.Issue {
		return &github.Issue{Number: number, Title: title, State: state, HTMLURL: fmt.Sprintf("https://github.com/acme/quill/issues/%d", number)}
	}
	// filler is n open issues with other titles, numbered from first.
	filler := func(first, n int) []*github.Issue {
		issues := make([]*github.Issue, 0, n)
		for i := range n {
			issues = append(issues, issue(first+i, fmt.Sprintf("Another matter %d", i), "open"))
		}

		return issues
	}
	pull := github.PullRequest{Number: 2, State: "open", Title: title, HTMLURL: "https://github.com/acme/quill/pull/2"}
	tests := []struct {
		name   string
		repo   string
		issues []*github.Issue
		pulls  []github.PullRequest
		want   *github.Issue
		// wantPages is how many pages the search reads.
		wantPages int
		notFound  bool
	}{
		{name: "a repository without an issue has none", repo: "quill", wantPages: 1},
		{name: "the open issue with the title", repo: "quill", issues: []*github.Issue{issue(1, "Another matter", "open"), issue(3, title, "open")}, want: issue(3, title, "open"), wantPages: 1},
		{name: "a closed issue with the title is not it", repo: "quill", issues: []*github.Issue{issue(1, title, "closed")}, wantPages: 1},
		{name: "a title that only begins the same is not it", repo: "quill", issues: []*github.Issue{issue(1, title+" again", "open"), issue(3, "the root organization has drifted", "open")}, wantPages: 1},
		{name: "a pull request with the title is not it", repo: "quill", pulls: []github.PullRequest{pull}, wantPages: 1},
		{name: "the issue behind a pull request with the title", repo: "quill", issues: []*github.Issue{issue(1, title, "open")}, pulls: []github.PullRequest{pull}, want: issue(1, title, "open"), wantPages: 1},
		{name: "the newest of several open issues with the title", repo: "quill", issues: []*github.Issue{issue(1, title, "open"), issue(3, title, "closed"), issue(4, title, "open"), issue(5, "Another matter", "open")}, want: issue(4, title, "open"), wantPages: 1},
		{name: "an issue on a later page", repo: "quill", issues: append([]*github.Issue{issue(1, title, "open")}, filler(10, 230)...), want: issue(1, title, "open"), wantPages: 3},
		{name: "every page is read before there is none", repo: "quill", issues: filler(10, 100), wantPages: 2},
		{name: "a repository GitHub does not know is not found", repo: "nowhere", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv := githubtest.New(t)
			srv.AddRepo("acme", "quill", &githubtest.Repo{Issues: tt.issues, PullRequests: tt.pulls})
			got, err := srv.Client().OpenIssue(t.Context(), "acme", tt.repo, title)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("OpenIssue() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("OpenIssue() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("OpenIssue() mismatch (-want +got):\n%s", diff)
			}
			if len(srv.Calls) != tt.wantPages {
				t.Errorf("the search read %d pages, want %d: %v", len(srv.Calls), tt.wantPages, srv.Calls)
			}
		})
	}
}

// TestCreateIssue: an issue is opened with its title and its body, numbered after the
// highest issue or pull request the repository knows, and answered with its page.
func TestCreateIssue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		repo   string
		issues []*github.Issue
		pulls  []github.PullRequest
		title  string
		want   *github.Issue
		// wantErr is part of the refusal expected; notFound says it is the API's 404.
		wantErr  string
		notFound bool
	}{
		{name: "the first issue of a repository", repo: "quill", title: "The root organization has drifted", want: &github.Issue{Number: 1, Title: "The root organization has drifted", State: "open", HTMLURL: "https://github.com/acme/quill/issues/1"}},
		{name: "an issue after the issues there are", repo: "quill", title: "The root organization has drifted", issues: []*github.Issue{{Number: 4, Title: "Another matter", State: "closed"}}, want: &github.Issue{Number: 5, Title: "The root organization has drifted", State: "open", HTMLURL: "https://github.com/acme/quill/issues/5"}},
		{name: "an issue after a pull request numbered higher", repo: "quill", title: "The root organization has drifted", issues: []*github.Issue{{Number: 4, Title: "Another matter", State: "open"}}, pulls: []github.PullRequest{{Number: 7, State: "open", Title: "A change"}}, want: &github.Issue{Number: 8, Title: "The root organization has drifted", State: "open", HTMLURL: "https://github.com/acme/quill/issues/8"}},
		{name: "an issue without a title is refused", repo: "quill", wantErr: "answered 422: title is required"},
		{name: "a repository GitHub does not know is not found", repo: "nowhere", title: "The root organization has drifted", wantErr: "answered 404", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const body = "The plan of the root organization shows changes nobody applied.\n\nSee the run."
			srv := githubtest.New(t)
			repo := srv.AddRepo("acme", "quill", &githubtest.Repo{Issues: tt.issues, PullRequests: tt.pulls})
			c := srv.Client()
			got, err := c.CreateIssue(t.Context(), "acme", tt.repo, tt.title, body)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || github.NotFound(err) != tt.notFound {
					t.Fatalf("CreateIssue() error = %v, wantErr %q (not found: %t)", err, tt.wantErr, tt.notFound)
				}
				if len(repo.Issues) != len(tt.issues) {
					t.Errorf("the refused issue was opened: %d issues", len(repo.Issues))
				}

				return
			}
			if err != nil {
				t.Fatalf("CreateIssue() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("CreateIssue() mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.want, repo.Issues[len(repo.Issues)-1]); diff != "" {
				t.Errorf("the stand-in's newest issue mismatch (-want +got):\n%s", diff)
			}
			if repo.IssueBodies[got.Number] != body {
				t.Errorf("the stand-in holds the body %q, want %q", repo.IssueBodies[got.Number], body)
			}
			found, err := c.OpenIssue(t.Context(), "acme", "quill", tt.title)
			if err != nil {
				t.Fatalf("OpenIssue() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, found); diff != "" {
				t.Errorf("OpenIssue() after the creation mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestCloseIssue: a closed issue is no longer the open issue under its title, closing it
// again changes nothing, and a number the repository has no issue under is not found.
func TestCloseIssue(t *testing.T) {
	t.Parallel()

	const title = "The root organization has drifted"
	srv := githubtest.New(t)
	repo := srv.AddRepo("acme", "quill", &githubtest.Repo{Issues: []*github.Issue{{Number: 6, Title: "Another matter", State: "open"}}})
	c := srv.Client()
	ctx := t.Context()
	issue, err := c.CreateIssue(ctx, "acme", "quill", title, "The plan shows changes.")
	if err != nil {
		t.Fatalf("CreateIssue() error = %v", err)
	}
	if err := c.CreateComment(ctx, "acme", "quill", issue.Number, "Still drifted."); err != nil {
		t.Fatalf("CreateComment() error = %v", err)
	}
	for range 2 {
		if err := c.CloseIssue(ctx, "acme", "quill", issue.Number); err != nil {
			t.Fatalf("CloseIssue() error = %v", err)
		}
		if got := repo.Issues[1]; got.Number != issue.Number || got.State != "closed" || got.Title != title {
			t.Errorf("the stand-in's issue = %+v, want it closed with its title", got)
		}
	}
	if repo.Issues[0].State != "open" {
		t.Errorf("the other issue is %s, want it left open", repo.Issues[0].State)
	}
	if found, err := c.OpenIssue(ctx, "acme", "quill", title); err != nil || found != nil {
		t.Errorf("OpenIssue() after the close = %+v, %v, want none", found, err)
	}
	if comments, err := c.IssueComments(ctx, "acme", "quill", issue.Number); err != nil || len(comments) != 1 {
		t.Errorf("IssueComments() of the closed issue = %+v, %v, want its one comment", comments, err)
	}
	if err := c.CloseIssue(ctx, "acme", "quill", 99); !github.NotFound(err) {
		t.Errorf("CloseIssue() of a number without an issue = %v, want not found", err)
	}
	if err := c.CloseIssue(ctx, "acme", "nowhere", issue.Number); !github.NotFound(err) {
		t.Errorf("CloseIssue() in a repository GitHub does not know = %v, want not found", err)
	}
}

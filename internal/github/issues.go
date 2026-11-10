// issues.go is a repository's issues: the one a command keeps open under a title to say
// something needs a person, found again by that title, and closed once it no longer does.
// A comment on an issue is CreateComment's (pulls.go).

package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Issue is an issue of a repository: its number, its title, its state (open or closed)
// and its page.
type Issue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

// issueClosed is the state of a closed issue.
const issueClosed = "closed"

// OpenIssue finds the repository's open issue whose title is exactly title, the newest
// when several carry it, and answers nil when none does. It reads the open issues, every
// page of them until the title is found; GitHub lists the open pull requests among them,
// each marked with a pull_request key, and those are passed over.
func (c *Client) OpenIssue(ctx context.Context, owner, repo, title string) (*Issue, error) {
	for page := 1; ; page++ {
		var listed []struct {
			Issue
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=%d&page=%d", owner, repo, perPage, page), nil, &listed); err != nil {
			return nil, err
		}
		for i := range listed {
			if listed[i].PullRequest == nil && listed[i].Title == title {
				return &listed[i].Issue, nil
			}
		}
		if len(listed) < perPage {
			return nil, nil
		}
	}
}

// CreateIssue opens an issue with the title and the body as the token's account, and
// answers it with its number.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string) (*Issue, error) {
	in := struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}{Title: title, Body: body}
	out := &Issue{}
	if err := c.do(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/issues", in, out); err != nil {
		return nil, err
	}

	return out, nil
}

// CloseIssue closes the issue with the number; one already closed stays closed.
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	in := struct {
		State string `json:"state"`
	}{State: issueClosed}

	return c.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/%d", owner, repo, number), in, nil)
}

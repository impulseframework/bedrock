// checks.go is the check runs GitHub holds for a commit: what each check that ran on it
// concluded and which app reported it, which is how a command tells whether a commit
// passed the checks a ruleset requires.

package github

import (
	"context"
	"fmt"
	"net/http"
)

// CheckRun is one check run on a commit: the check's name, its status (queued,
// in_progress, completed), its conclusion once it completed (success, failure, skipped
// and GitHub's other words for how a run ended; empty until then) and the app that
// reported it.
type CheckRun struct {
	Name       string   `json:"name"`
	Status     string   `json:"status"`
	Conclusion string   `json:"conclusion"`
	App        CheckApp `json:"app"`
}

// CheckApp is the GitHub App a check run was reported by: its App ID, which a required
// check names its reporter by, and its slug.
type CheckApp struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
}

// CheckRuns lists the check runs on the commit ref names (a commit SHA, or a branch or
// tag by name, whose head it is), every page of them. GitHub lists the latest run of each
// check, not the runs a later one replaced.
func (c *Client) CheckRuns(ctx context.Context, owner, repo, ref string) ([]CheckRun, error) {
	var all []CheckRun
	for page := 1; ; page++ {
		var answer struct {
			CheckRuns []CheckRun `json:"check_runs"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=%d&page=%d", owner, repo, ref, perPage, page), nil, &answer); err != nil {
			return nil, err
		}
		all = append(all, answer.CheckRuns...)
		if len(answer.CheckRuns) < perPage {
			return all, nil
		}
	}
}

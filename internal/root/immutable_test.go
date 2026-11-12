package root_test

import (
	"bytes"
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/root"
)

// The call that sets the organization's policy on immutable releases, as the stand-in
// records it, and the words every sentence about the policy carries.
const (
	setsImmutable  = "PUT /orgs/imf-example/settings/immutable-releases"
	immutableWords = "immutable releases"
)

// sets counts the calls that set the organization's policy on immutable releases.
func (w *githubWorld) sets() int {
	var n int
	w.srv.Update(func() {
		for _, call := range w.srv.Calls {
			if call == setsImmutable {
				n++
			}
		}
	})

	return n
}

// policy is the organization's policy on immutable releases as the stand-in holds it.
func (w *githubWorld) policy() string {
	var policy string
	w.srv.Update(func() { policy = w.srv.ImmutableReleases[w.p.GithubOrganization] })

	return policy
}

// TestEngineImmutableReleases: the bootstrap's apply leaves the organization's policy on
// immutable releases at every repository. A policy that names no repository, or the ones
// an owner selected, is a change a check and a plan list and neither makes; the apply
// widens it to every repository with one call, and the check after it is clean. A policy
// that names every repository already is left alone: no change, and nothing set.
func TestEngineImmutableReleases(t *testing.T) {
	t.Parallel()

	selected := "GitHub: turns immutable releases on for every repository of the imf-example organization (its policy names only the repositories an owner selected today): a release published from then on keeps its tag and its assets as published"
	tests := []struct {
		name string
		// policy is what the organization's policy says before the run, empty for an
		// organization that never set one.
		policy string
		want   []string
	}{
		{name: "never set", want: []string{immutableSentence}},
		{name: "no repository", policy: github.ImmutableReleasesNone, want: []string{immutableSentence}},
		{name: "selected repositories", policy: github.ImmutableReleasesSelected, want: []string{selected}},
		{name: "every repository", policy: github.ImmutableReleasesAll},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			owner := w.p.GithubOrganization
			if got := w.policy(); got != github.ImmutableReleasesAll || w.sets() != 1 {
				t.Fatalf("after the bootstrap's apply the policy is %q, set %d time(s); want %q, set once", got, w.sets(), github.ImmutableReleasesAll)
			}
			w.srv.Update(func() {
				w.srv.ImmutableReleases[owner] = tt.policy
				if tt.policy == "" {
					delete(w.srv.ImmutableReleases, owner)
				}
			})

			for _, mode := range []root.Mode{root.Check, root.Plan} {
				report, _ := w.run(t, mode, nil)
				sameSentences(t, "the changes a run that applies nothing lists", whats(report), tt.want)
				if got := w.policy(); got != tt.policy || w.sets() != 1 || report.Applied != 0 {
					t.Errorf("a run that applies nothing left the policy %q, set %d time(s), with %d change(s) applied; want %q, the bootstrap's one set and none applied", got, w.sets(), report.Applied, tt.policy)
				}
				if got := mentioning(report.Notes, immutableWords); len(got) != 0 || len(report.Findings) != 0 {
					t.Errorf("a policy the token reads is noted %q and found %q; want neither", got, report.Findings)
				}
			}

			applied, out := w.run(t, root.Apply, nil)
			sameSentences(t, "the changes of the apply", whats(applied), tt.want)
			if applied.Applied != len(tt.want) {
				t.Errorf("the apply made %d change(s), want %d:\n%s", applied.Applied, len(tt.want), out)
			}
			if got, want := w.policy(), github.ImmutableReleasesAll; got != want {
				t.Errorf("after the apply the policy is %q, want %q", got, want)
			}
			if got, want := w.sets(), 1+len(tt.want); got != want {
				t.Errorf("the policy was set %d time(s) in all, want %d: the bootstrap's, and one for each change", got, want)
			}
			w.clean(t)
			if got, want := w.sets(), 1+len(tt.want); got != want {
				t.Errorf("the check after the apply set the policy: %d set(s) in all, want %d", got, want)
			}
		})
	}
}

// TestEngineImmutableReleasesUnread: GitHub answers a token that may not administer the
// organization not found and tells it nothing of the policy on immutable releases, so a
// run with such a token says the policy was not checked, with the place an owner sees
// it, and takes the answer neither for a policy that is off nor for one that is on: it
// lists no change, an apply sets nothing, and the check stays clean.
func TestEngineImmutableReleasesUnread(t *testing.T) {
	t.Parallel()

	note := `immutable releases were not checked: this token cannot read the imf-example organization's policy on them (the infrastructure app's reads it, and an owner's); an owner sees it in the organization's Settings on GitHub, under Repository, then General: in the "Releases" section, "All repositories"`
	for _, policy := range []string{github.ImmutableReleasesNone, github.ImmutableReleasesAll} {
		t.Run(policy, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			owner := w.p.GithubOrganization
			w.srv.Update(func() {
				w.srv.ImmutableReleases[owner] = policy
				w.srv.ImmutableReleasesHidden[owner] = true
			})
			for _, mode := range []root.Mode{root.Check, root.Plan, root.Apply} {
				report, out := w.run(t, mode, nil)
				sameSentences(t, "the notes about immutable releases", mentioning(report.Notes, immutableWords), []string{note})
				if !report.Clean() {
					t.Errorf("a policy the token cannot read is a change or a finding:\n%s\nchanges: %q\nfindings: %q", out, whats(report), report.Findings)
				}
			}
			if got := w.policy(); got != policy || w.sets() != 1 {
				t.Errorf("the runs left the policy %q, set %d time(s); want %q and the bootstrap's one set alone", got, w.sets(), policy)
			}
		})
	}
}

// TestEngineImmutableReleasesRefused: an answer to the read that is neither the policy nor
// not found stops the run under a sentence that names the read, before anything else on
// GitHub is read; an apply whose set GitHub refuses stops under the change's own
// sentence, with the policy as it was.
func TestEngineImmutableReleasesRefused(t *testing.T) {
	t.Parallel()

	const reads = "GET /orgs/imf-example/settings/immutable-releases"
	tests := []struct {
		name    string
		refused string
		mode    root.Mode
		want    []string
	}{
		{
			name: "the read", refused: reads, mode: root.Check,
			want: []string{"reading the imf-example organization's policy on immutable releases", "GitHub " + reads + " answered 403: Resource not accessible by integration"},
		},
		{
			name: "the set", refused: setsImmutable, mode: root.Apply,
			want: []string{strings.TrimPrefix(immutableSentence, "GitHub: "), "GitHub " + setsImmutable + " answered 403: Resource not accessible by integration"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newGitHubWorld(t, libraryPlacement(t))
			clients := w.refusing(t, map[string]int{tt.refused: http.StatusForbidden})
			var out bytes.Buffer
			report, err := root.Run(context.Background(), clients, &root.Request{Placement: w.p, Mode: tt.mode, GitHubToken: "test-token", SealKeys: w.keys}, &out)
			if err == nil {
				t.Fatalf("Run() error = nil, want the refusal:\n%s", out.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("Run() error = %v, want it to say %q", err, want)
				}
			}
			if report != nil && report.Applied != 0 {
				t.Errorf("the run applied %d change(s) before the refusal, want none", report.Applied)
			}
			if got := w.policy(); got != "" {
				t.Errorf("after the refusal the policy is %q, want it never set", got)
			}
			w.srv.Update(func() {
				if slices.ContainsFunc(w.srv.Calls, func(call string) bool { return strings.HasPrefix(call, "GET /repos/") }) {
					t.Errorf("the run read a repository after the refusal: %q", w.srv.Calls)
				}
			})
		})
	}
}

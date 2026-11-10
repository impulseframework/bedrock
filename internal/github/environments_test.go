package github_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

// environments is a repository with an environment that keeps branch patterns of its
// own, with one pattern and one secret, and an environment any branch may deploy to.
func environments(t *testing.T) (*githubtest.Server, *githubtest.Repo) {
	t.Helper()

	srv := githubtest.New(t)
	repo := srv.AddRepo("acme", "quill", &githubtest.Repo{Environments: map[string]*githubtest.Environment{
		"prd": {Custom: true, Branches: []string{"master"}, Secrets: map[string]*githubtest.EnvSecret{
			"DEPLOY_KEY": {Value: []byte("the deploy key"), UpdatedAt: time.Date(2026, time.March, 1, 12, 30, 0, 0, time.UTC)},
		}},
		"open": {},
	}})

	return srv, repo
}

func TestEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repo        string
		environment string
		want        *github.Environment
		notFound    bool
	}{
		{name: "an environment that keeps its own branch patterns", repo: "quill", environment: "prd", want: &github.Environment{Name: "prd", BranchPolicy: &github.BranchPolicy{ProtectedBranches: false, CustomBranchPolicies: true}}},
		{name: "an environment any branch may deploy to has no policy", repo: "quill", environment: "open", want: &github.Environment{Name: "open"}},
		{name: "an absent environment is not found", repo: "quill", environment: "tst", notFound: true},
		{name: "an environment of a repository GitHub does not know is not found", repo: "nowhere", environment: "prd", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _ := environments(t)
			got, err := srv.Client().Environment(t.Context(), "acme", tt.repo, tt.environment)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("Environment() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("Environment() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Environment() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPutEnvironment: a put makes the environment, or sets the one there is, as one that
// takes deployments from its own branch patterns alone, and keeps the patterns and the
// secrets an environment already has.
func TestPutEnvironment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		repo        string
		environment string
		// wantBranches are the environment's patterns after the put.
		wantBranches []string
		wantSecrets  int
		notFound     bool
	}{
		{name: "a new environment, with no pattern yet", repo: "quill", environment: "tst", wantBranches: []string{}},
		{name: "an environment any branch could deploy to", repo: "quill", environment: "open", wantBranches: []string{}},
		{name: "an environment already so keeps what it has", repo: "quill", environment: "prd", wantBranches: []string{"master"}, wantSecrets: 1},
		{name: "a name GitHub wants encoded", repo: "quill", environment: "pull request", wantBranches: []string{}},
		{name: "a repository GitHub does not know is not found", repo: "nowhere", environment: "tst", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, repo := environments(t)
			c := srv.Client()
			err := c.PutEnvironment(t.Context(), "acme", tt.repo, tt.environment)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("PutEnvironment() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("PutEnvironment() error = %v", err)
			}
			held := repo.Environments[tt.environment]
			if held == nil || !held.Custom || len(held.Secrets) != tt.wantSecrets {
				t.Fatalf("the stand-in's environment = %+v, want one keeping its own patterns, with %d secrets", held, tt.wantSecrets)
			}
			got, err := c.Environment(t.Context(), "acme", "quill", tt.environment)
			if err != nil {
				t.Fatalf("Environment() error = %v", err)
			}
			want := &github.Environment{Name: tt.environment, BranchPolicy: &github.BranchPolicy{ProtectedBranches: false, CustomBranchPolicies: true}}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Errorf("Environment() after the put mismatch (-want +got):\n%s", diff)
			}
			branches, err := c.EnvironmentBranches(t.Context(), "acme", "quill", tt.environment)
			if err != nil {
				t.Fatalf("EnvironmentBranches() error = %v", err)
			}
			if diff := cmp.Diff(tt.wantBranches, branches); diff != "" {
				t.Errorf("EnvironmentBranches() after the put mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestEnvironmentBranches: an environment's branch patterns are listed in the order they
// were added, and a pattern is added to an environment that keeps its own; an absent
// environment and one any branch may deploy to refuse a pattern as not found, and a
// pattern the environment already has is answered as moved.
func TestEnvironmentBranches(t *testing.T) {
	t.Parallel()

	srv, repo := environments(t)
	c := srv.Client()
	ctx := t.Context()
	branches, err := c.EnvironmentBranches(ctx, "acme", "quill", "prd")
	if err != nil {
		t.Fatalf("EnvironmentBranches() error = %v", err)
	}
	if diff := cmp.Diff([]string{"master"}, branches); diff != "" {
		t.Errorf("EnvironmentBranches() mismatch (-want +got):\n%s", diff)
	}
	if err := c.AddEnvironmentBranch(ctx, "acme", "quill", "prd", "release/*"); err != nil {
		t.Fatalf("AddEnvironmentBranch() error = %v", err)
	}
	branches, err = c.EnvironmentBranches(ctx, "acme", "quill", "prd")
	if err != nil {
		t.Fatalf("EnvironmentBranches() after the addition error = %v", err)
	}
	if diff := cmp.Diff([]string{"master", "release/*"}, branches); diff != "" {
		t.Errorf("EnvironmentBranches() after the addition mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"master", "release/*"}, repo.Environments["prd"].Branches); diff != "" {
		t.Errorf("the stand-in's patterns mismatch (-want +got):\n%s", diff)
	}

	err = c.AddEnvironmentBranch(ctx, "acme", "quill", "prd", "master")
	if !github.Moved(err) || github.NotFound(err) {
		t.Errorf("AddEnvironmentBranch() of a pattern the environment has = %v, want moved (303)", err)
	}
	if len(repo.Environments["prd"].Branches) != 2 {
		t.Errorf("the refused pattern was added: %v", repo.Environments["prd"].Branches)
	}
	if err := c.AddEnvironmentBranch(ctx, "acme", "quill", "open", "master"); !github.NotFound(err) {
		t.Errorf("AddEnvironmentBranch() to an environment any branch may deploy to = %v, want not found", err)
	}
	if err := c.AddEnvironmentBranch(ctx, "acme", "quill", "tst", "master"); !github.NotFound(err) {
		t.Errorf("AddEnvironmentBranch() to an absent environment = %v, want not found", err)
	}
	if _, err := c.EnvironmentBranches(ctx, "acme", "quill", "tst"); !github.NotFound(err) {
		t.Errorf("EnvironmentBranches() of an absent environment = %v, want not found", err)
	}
	open, err := c.EnvironmentBranches(ctx, "acme", "quill", "open")
	if err != nil || len(open) != 0 {
		t.Errorf("EnvironmentBranches() of an environment any branch may deploy to = %v, %v, want none", open, err)
	}
}

// TestPutEnvironmentSecret: the value arrives sealed with the environment's key, so that
// what the stand-in opens is the value; a second put replaces it; an environment the
// repository lacks takes no secret.
func TestPutEnvironmentSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		secret      string
		value       []byte
		// wantSecrets is how many secrets the environment holds after the put.
		wantSecrets int
		notFound    bool
	}{
		{name: "a new secret", environment: "prd", secret: "API_TOKEN", value: []byte("the api token"), wantSecrets: 2},
		{name: "a secret there was is replaced", environment: "prd", secret: "DEPLOY_KEY", value: []byte("another deploy key"), wantSecrets: 1},
		{name: "the first secret of an environment", environment: "open", secret: "DEPLOY_KEY", value: []byte("-----BEGIN KEY-----\nline\n\x00\xff\n-----END KEY-----\n"), wantSecrets: 1},
		{name: "an absent environment is not found", environment: "tst", secret: "DEPLOY_KEY", value: []byte("the deploy key"), notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, repo := environments(t)
			before := time.Now()
			err := srv.Client().PutEnvironmentSecret(t.Context(), "acme", "quill", tt.environment, tt.secret, tt.value)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("PutEnvironmentSecret() error = %v, want not found", err)
				}
				if _, made := repo.Environments[tt.environment]; made {
					t.Error("the put made the environment")
				}

				return
			}
			if err != nil {
				t.Fatalf("PutEnvironmentSecret() error = %v", err)
			}
			held := repo.Environments[tt.environment].Secrets
			got := held[tt.secret]
			if got == nil || len(held) != tt.wantSecrets {
				t.Fatalf("the stand-in's environment holds %d secrets, %q among them: %t; want %d", len(held), tt.secret, got != nil, tt.wantSecrets)
			}
			if !bytes.Equal(got.Value, tt.value) {
				t.Errorf("the stand-in opened %q, want %q", got.Value, tt.value)
			}
			if got.UpdatedAt.Before(before) {
				t.Errorf("updated at %s, before the put at %s", got.UpdatedAt, before)
			}
			base := "/repos/acme/quill/environments/" + tt.environment + "/secrets/"
			if diff := cmp.Diff([]string{"GET " + base + "public-key", "PUT " + base + tt.secret}, srv.Calls); diff != "" {
				t.Errorf("calls mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEnvironmentSecret(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		secret      string
		want        *github.EnvironmentSecret
		notFound    bool
	}{
		{name: "a secret reads back without its value", environment: "prd", secret: "DEPLOY_KEY", want: &github.EnvironmentSecret{Name: "DEPLOY_KEY", UpdatedAt: time.Date(2026, time.March, 1, 12, 30, 0, 0, time.UTC)}},
		{name: "a secret the environment lacks is not found", environment: "prd", secret: "NOWHERE", notFound: true},
		{name: "a secret of an environment without any is not found", environment: "open", secret: "DEPLOY_KEY", notFound: true},
		{name: "a secret of an absent environment is not found", environment: "tst", secret: "DEPLOY_KEY", notFound: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srv, _ := environments(t)
			got, err := srv.Client().EnvironmentSecret(t.Context(), "acme", "quill", tt.environment, tt.secret)
			if tt.notFound {
				if !github.NotFound(err) {
					t.Fatalf("EnvironmentSecret() error = %v, want not found", err)
				}

				return
			}
			if err != nil {
				t.Fatalf("EnvironmentSecret() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("EnvironmentSecret() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestDeleteEnvironmentSecret: a secret put is read back, removed and then not found, to
// a read and to a second removal; the environment's other secrets stay.
func TestDeleteEnvironmentSecret(t *testing.T) {
	t.Parallel()

	srv, repo := environments(t)
	c := srv.Client()
	ctx := t.Context()
	if err := c.PutEnvironmentSecret(ctx, "acme", "quill", "prd", "API_TOKEN", []byte("the api token")); err != nil {
		t.Fatalf("PutEnvironmentSecret() error = %v", err)
	}
	read, err := c.EnvironmentSecret(ctx, "acme", "quill", "prd", "API_TOKEN")
	if err != nil || read.Name != "API_TOKEN" || !read.UpdatedAt.Equal(repo.Environments["prd"].Secrets["API_TOKEN"].UpdatedAt) {
		t.Fatalf("EnvironmentSecret() = %+v, %v, want the secret as the stand-in holds it", read, err)
	}
	if err := c.DeleteEnvironmentSecret(ctx, "acme", "quill", "prd", "API_TOKEN"); err != nil {
		t.Fatalf("DeleteEnvironmentSecret() error = %v", err)
	}
	if held := repo.Environments["prd"].Secrets; len(held) != 1 || held["DEPLOY_KEY"] == nil {
		t.Errorf("after the removal the environment holds %d secrets, want the other one alone", len(held))
	}
	if _, err := c.EnvironmentSecret(ctx, "acme", "quill", "prd", "API_TOKEN"); !github.NotFound(err) {
		t.Errorf("EnvironmentSecret() of a removed secret = %v, want not found", err)
	}
	if err := c.DeleteEnvironmentSecret(ctx, "acme", "quill", "prd", "API_TOKEN"); !github.NotFound(err) {
		t.Errorf("DeleteEnvironmentSecret() of an absent secret = %v, want not found", err)
	}
	if err := c.DeleteEnvironmentSecret(ctx, "acme", "quill", "tst", "DEPLOY_KEY"); !github.NotFound(err) {
		t.Errorf("DeleteEnvironmentSecret() in an absent environment = %v, want not found", err)
	}
}

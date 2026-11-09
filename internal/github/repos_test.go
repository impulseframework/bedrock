package github_test

import (
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
)

func TestArchive(t *testing.T) {
	t.Parallel()

	s := githubtest.New(t)
	s.Repos["imp-example/harbor"] = &githubtest.Repo{}
	c := s.Client()
	if err := c.Archive(context.Background(), "imp-example", "harbor"); err != nil {
		t.Fatalf("Archive() = %v", err)
	}
	if !s.Repos["imp-example/harbor"].Archived {
		t.Error("the repository is not archived")
	}
	if !slices.Contains(s.Calls, "PATCH /repos/imp-example/harbor") {
		t.Errorf("calls = %v, want the repository's PATCH", s.Calls)
	}
	err := c.Archive(context.Background(), "imp-example", "beacon")
	if !github.NotFound(err) {
		t.Errorf("Archive() of a repository the organization lacks = %v, want not found", err)
	}
}

// TestRepository: a repository reads back with an id, never 0, and with whether it is
// private and archived, however it came to be in the organization.
func TestRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// seed puts acme/quill in the stand-in.
		seed func(t *testing.T, s *githubtest.Server)
		// wantID is the id expected, when the seed chose it; any id but 0 otherwise.
		wantID int64
		want   github.Repository
	}{
		{
			name: "a private repository a test added",
			seed: func(_ *testing.T, s *githubtest.Server) {
				s.AddRepo("acme", "quill", &githubtest.Repo{Private: true, Refs: map[string]github.Object{"refs/heads/main": {Type: "commit", SHA: "c1"}}})
			},
			want: github.Repository{Name: "quill", FullName: "acme/quill", DefaultBranch: "main", Private: true, HTMLURL: "https://github.com/acme/quill"},
		},
		{
			name: "a public, archived repository added with an id of its own",
			seed: func(_ *testing.T, s *githubtest.Server) {
				s.AddRepo("acme", "quill", &githubtest.Repo{ID: 42, Archived: true})
			},
			wantID: 42,
			want:   github.Repository{Name: "quill", FullName: "acme/quill", DefaultBranch: "master", Archived: true, HTMLURL: "https://github.com/acme/quill"},
		},
		{
			name: "a repository placed in the stand-in's map directly",
			seed: func(_ *testing.T, s *githubtest.Server) {
				s.Repos["acme/quill"] = &githubtest.Repo{}
			},
			want: github.Repository{Name: "quill", FullName: "acme/quill", DefaultBranch: "master", HTMLURL: "https://github.com/acme/quill"},
		},
		{
			name: "a repository made private through the API",
			seed: func(t *testing.T, s *githubtest.Server) {
				t.Helper()

				if _, err := s.Client().CreateRepository(t.Context(), "acme", github.RepositoryRequest{Name: "quill", Private: true, AutoInit: true}); err != nil {
					t.Fatalf("CreateRepository() error = %v", err)
				}
			},
			want: github.Repository{Name: "quill", FullName: "acme/quill", DefaultBranch: "main", Private: true, HTMLURL: "https://github.com/acme/quill"},
		},
		{
			name: "a repository made public through the API",
			seed: func(t *testing.T, s *githubtest.Server) {
				t.Helper()

				if _, err := s.Client().CreateRepository(t.Context(), "acme", github.RepositoryRequest{Name: "quill", AutoInit: true}); err != nil {
					t.Fatalf("CreateRepository() error = %v", err)
				}
			},
			want: github.Repository{Name: "quill", FullName: "acme/quill", DefaultBranch: "main", HTMLURL: "https://github.com/acme/quill"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := githubtest.New(t)
			tt.seed(t, s)
			got, err := s.Client().Repository(t.Context(), "acme", "quill")
			if err != nil {
				t.Fatalf("Repository() error = %v", err)
			}
			if got.ID == 0 || (tt.wantID != 0 && got.ID != tt.wantID) {
				t.Errorf("Repository() id = %d, want %d (0: any id but 0)", got.ID, tt.wantID)
			}
			if got.ID != s.Repos["acme/quill"].ID {
				t.Errorf("Repository() id = %d, the stand-in holds %d", got.ID, s.Repos["acme/quill"].ID)
			}
			got.ID = 0
			if diff := cmp.Diff(&tt.want, got); diff != "" {
				t.Errorf("Repository() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

// TestRepositoryIDs: each repository has an id of its own, and the one its creation
// answers is the one it reads back with.
func TestRepositoryIDs(t *testing.T) {
	t.Parallel()

	s := githubtest.New(t)
	c := s.Client()
	added := s.AddRepo("acme", "quill", &githubtest.Repo{})
	made, err := c.CreateRepository(t.Context(), "acme", github.RepositoryRequest{Name: "harbor", Private: true})
	if err != nil {
		t.Fatalf("CreateRepository() error = %v", err)
	}
	read, err := c.Repository(t.Context(), "acme", "harbor")
	if err != nil {
		t.Fatalf("Repository() error = %v", err)
	}
	if made.ID == 0 || made.ID != read.ID {
		t.Errorf("CreateRepository() id = %d, Repository() id = %d, want one id that is not 0", made.ID, read.ID)
	}
	if added.ID == 0 || added.ID == made.ID {
		t.Errorf("the added repository's id = %d and the made one's = %d, want two ids that are not 0", added.ID, made.ID)
	}
	if _, err := c.Repository(t.Context(), "acme", "nowhere"); !github.NotFound(err) {
		t.Errorf("Repository() of a repository the organization lacks = %v, want not found", err)
	}
}

func TestSetRepositoryPrivate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// was is whether the repository is private before the call; private what the call asks.
		was     bool
		private bool
	}{
		{name: "a public repository is made private", was: false, private: true},
		{name: "a private repository is made public", was: true, private: false},
		{name: "a private repository stays private", was: true, private: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := githubtest.New(t)
			squash := true
			repo := s.AddRepo("acme", "quill", &githubtest.Repo{Private: tt.was, Archived: true, Settings: github.RepositorySettings{AllowSquashMerge: &squash}})
			c := s.Client()
			if err := c.SetRepositoryPrivate(t.Context(), "acme", "quill", tt.private); err != nil {
				t.Fatalf("SetRepositoryPrivate() error = %v", err)
			}
			if repo.Private != tt.private {
				t.Errorf("the stand-in's repository is private = %t, want %t", repo.Private, tt.private)
			}
			got, err := c.Repository(t.Context(), "acme", "quill")
			if err != nil {
				t.Fatalf("Repository() error = %v", err)
			}
			if got.Private != tt.private {
				t.Errorf("Repository() private = %t, want %t", got.Private, tt.private)
			}
			// The change names private alone: nothing else of the repository moves.
			if !repo.Archived || repo.Settings.AllowSquashMerge == nil || !*repo.Settings.AllowSquashMerge || repo.Settings.AllowMergeCommit != nil {
				t.Errorf("the repository's other settings changed: archived = %t, settings = %+v", repo.Archived, repo.Settings)
			}
			if !slices.Contains(s.Calls, "PATCH /repos/acme/quill") {
				t.Errorf("calls = %v, want the repository's PATCH", s.Calls)
			}
		})
	}
}

func TestSetRepositoryPrivateNotFound(t *testing.T) {
	t.Parallel()

	s := githubtest.New(t)
	if err := s.Client().SetRepositoryPrivate(t.Context(), "acme", "nowhere", true); !github.NotFound(err) {
		t.Errorf("SetRepositoryPrivate() of a repository the organization lacks = %v, want not found", err)
	}
}

package create

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"google.golang.org/api/option"
)

// TestNewGoogleSplitsTheQuotaProjectFromStorage: the clients open with a quota project
// when the storage client is kept out of it, and Google's transport refuses the storage
// client a quota project beside the HTTP client it appends itself, which is why
// NewGoogleBilledTo splits the options. No client dials at construction; the credentials
// are left out so the test needs none.
func TestNewGoogleSplitsTheQuotaProjectFromStorage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	none := option.WithoutAuthentication()
	billed := []option.ClientOption{none, option.WithQuotaProject("imr-root-gbl-core-f2a4")}
	g, err := newGoogle(ctx, optionsForAll(billed, []option.ClientOption{none}))
	if err != nil {
		t.Fatalf("newGoogle() with the storage client kept out of the quota project = %v", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if _, err := newGoogle(ctx, optionsForAll(billed, billed)); err == nil || !strings.Contains(err.Error(), "WithHTTPClient is incompatible with QuotaProject") {
		t.Errorf("newGoogle() with the quota project on the storage client too = %v, want Google's refusal, the reason for the split", err)
	}
}

// TestBilledToOptionsKeepTheQuotaProjectOffStorage: NewGoogleBilledTo hands every client
// the quota project and the storage client nothing, and with no quota project it hands no
// client anything. The lists are read as billedToOptions answers them, since
// NewGoogleBilledTo itself opens with the process's own credentials and a test has none.
// A quota project handed to the storage client would otherwise show at the break-glass
// account's terminal alone, as the refusal TestNewGoogleSplitsTheQuotaProjectFromStorage
// reads; one taken off another client, as that API's refusal of a person's credentials.
func TestBilledToOptionsKeepTheQuotaProjectOffStorage(t *testing.T) {
	t.Parallel()

	const project = "imr-root-gbl-core-f2a4"
	billed := []option.ClientOption{option.WithQuotaProject(project)}
	tests := []struct {
		name         string
		quotaProject string
		want         *googleOptions
	}{
		{
			// Every client is named but the storage client. A client added to the set is
			// decided here too: with the quota project, or beside the storage client.
			name:         "a quota project is handed to every client but the storage client",
			quotaProject: project,
			want: &googleOptions{
				folders: billed, projects: billed, iam: billed, billing: billed, services: billed, servicesBeta: billed,
				secrets: billed, dns: billed, budgets: billed,
			},
		},
		{
			name:         "no quota project is no option for any client",
			quotaProject: "",
			want:         &googleOptions{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := billedToOptions(tt.quotaProject)
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(googleOptions{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("billedToOptions(%q) hands a client other options than NewGoogleBilledTo is to open it with (-want +got):\n%s", tt.quotaProject, diff)
			}
		})
	}
}

// TestNewGoogleHandsEveryClientTheOptionsItIsGiven: NewGoogle opens every client, the
// storage client too, with the options it is given. NewGoogleAs gives it the creating
// identity's token source, and a client opened without it would act as the process's own
// identity, which shows at that client's first call alone. Here the option given is the
// one that asks for no credentials, and the process's default credentials are named by a
// file that is not there: a client handed the option opens, and a client handed nothing
// looks for the credentials and cannot be opened.
func TestNewGoogleHandsEveryClientTheOptionsItIsGiven(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	ctx := context.Background()
	// The reading rests on this: with nothing handed to it, a client looks for the
	// credentials and is refused for want of the file.
	if _, err := NewGoogle(ctx); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("NewGoogle() with no option = %v, want the refusal for want of %s: without it this test cannot tell a client that was handed nothing", err, missing)
	}
	g, err := NewGoogle(ctx, option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("NewGoogle() with the option that asks for no credentials = %v: a client was opened without the option and looked for credentials", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
}

// TestOptionsForAllLeavesNoClientOut: every client of the set is given the options the
// constructors pass, and the storage client its own. A client added to the set and left
// out of optionsForAll would open with no options at all: with the process's own
// credentials where the others open as the creating identity, and with no quota project.
func TestOptionsForAllLeavesNoClientOut(t *testing.T) {
	t.Parallel()

	none := option.WithoutAuthentication()
	shared, storage := []option.ClientOption{none}, []option.ClientOption{none, none}
	got := reflect.ValueOf(optionsForAll(shared, storage)).Elem()
	for i := range got.NumField() {
		name, want := got.Type().Field(i).Name, len(shared)
		if name == "storage" {
			want = len(storage)
		}
		if n := got.Field(i).Len(); n != want {
			t.Errorf("optionsForAll() gives the %s client %d option(s), want %d", name, n, want)
		}
	}
}

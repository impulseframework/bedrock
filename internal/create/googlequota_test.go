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

// TestOptionsForAllLeavesNoClientOut: optionsForAll fills every list of googleOptions,
// the storage client's with the storage options and each other with the shared ones. A
// list it left empty would open its client with no options at all: with the process's own
// credentials where the others open as the creating identity, and with no quota project.
// The test reads the lists of googleOptions and nothing else: that every client of the
// set has a list, and opens with it, is TestEveryClientOpensWithItsOwnOptions.
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

// TestEveryClientOpensWithItsOwnOptions: every client of the set has a list of options of
// its own in googleOptions, and opens with that list and no other. A client added to the
// set with no list, or opened with nothing, would open with the process's own credentials
// where the others open as the creating identity, and with no quota project; a client
// opened with another client's list leaves its own unread. Three things are read. Every
// client of the set has a list by its name. The whole set opens with lists that need no
// credentials while the process's default credentials are named by a file that is not
// there, so a client opened with nothing looks for them and the set cannot open. And each
// list in turn is replaced with options Google's transport refuses, and the opening must
// then fail at the client the list belongs to: a list no client opens with fails nothing,
// and a list another client opens with fails there.
func TestEveryClientOpensWithItsOwnOptions(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	// refusals are, by the name of each list of googleOptions, how that list is replaced
	// with the refused options, and the constructor the failed opening then names.
	refusals := map[string]struct {
		refuse  func(opts *googleOptions)
		wantErr string
	}{
		"folders": {
			refuse: func(opts *googleOptions) {
				opts.folders = refused()
			},
			wantErr: "resourcemanager.NewFoldersClient()",
		},
		"projects": {
			refuse: func(opts *googleOptions) {
				opts.projects = refused()
			},
			wantErr: "resourcemanager.NewProjectsClient()",
		},
		"iam": {
			refuse: func(opts *googleOptions) {
				opts.iam = refused()
			},
			wantErr: "admin.NewIamClient()",
		},
		"billing": {
			refuse: func(opts *googleOptions) {
				opts.billing = refused()
			},
			wantErr: "cloudbilling.NewService()",
		},
		"services": {
			refuse: func(opts *googleOptions) {
				opts.services = refused()
			},
			wantErr: "serviceusage.NewService()",
		},
		"servicesBeta": {
			refuse: func(opts *googleOptions) {
				opts.servicesBeta = refused()
			},
			wantErr: "serviceusagebeta.NewService()",
		},
		"storage": {
			refuse: func(opts *googleOptions) {
				opts.storage = refused()
			},
			wantErr: "storage.NewClient()",
		},
		"secrets": {
			refuse: func(opts *googleOptions) {
				opts.secrets = refused()
			},
			wantErr: "secretmanager.NewClient()",
		},
		"dns": {
			refuse: func(opts *googleOptions) {
				opts.dns = refused()
			},
			wantErr: "dns.NewService()",
		},
		"budgets": {
			refuse: func(opts *googleOptions) {
				opts.budgets = refused()
			},
			wantErr: "billingbudgets.NewService()",
		},
	}

	// Every field of the set is a client, but for the two named here, and a client has a
	// list by its own name.
	notClients := map[string]bool{"secretOpts": true, "regional": true}
	lists, set := reflect.TypeFor[googleOptions](), reflect.TypeFor[google]()
	listed := map[string]bool{}
	for i := range lists.NumField() {
		listed[lists.Field(i).Name] = true
	}
	for i := range set.NumField() {
		name := set.Field(i).Name
		if !listed[name] && !notClients[name] {
			t.Errorf("the set's %s has no list of options by its name in googleOptions: a client opens with a list of its own, which optionsForAll fills, and a field that is not a client is named in this test", name)
		}
	}

	ctx := context.Background()
	opts, _ := watchedGoogleOptions(t)
	whole, err := newGoogle(ctx, opts)
	if err != nil {
		t.Fatalf("newGoogle() with every list filled = %v: with the process's credentials missing, a client that opens with nothing, or with a list that was not filled, cannot be opened", err)
	}
	if err := whole.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}

	for i := range lists.NumField() {
		name := lists.Field(i).Name
		tt, ok := refusals[name]
		if !ok {
			t.Errorf("googleOptions has the list %s, and this test does not refuse it: enter it with the constructor its client opens with", name)

			continue
		}
		opts, _ := watchedGoogleOptions(t)
		tt.refuse(opts)
		// The entry refuses the list it is entered under, and that list alone.
		for j, got := 0, reflect.ValueOf(opts).Elem(); j < got.NumField(); j++ {
			if other := got.Type().Field(j).Name; (got.Field(j).Len() == len(refused())) != (other == name) {
				t.Fatalf("the entry for %s leaves the list %s with %d option(s): it is to refuse %s and no other list", name, other, got.Field(j).Len(), name)
			}
		}
		g, err := newGoogle(ctx, opts)
		if err == nil {
			_ = g.Close()
			t.Errorf("newGoogle() opened every client with the list %s refused: no client opens with that list", name)

			continue
		}
		if !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("newGoogle() with the list %s refused = %v, want the refusal of %s: another client opens with that list", name, err, tt.wantErr)
		}
	}
}

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

// TestNewGoogleSplitsTheQuotaProjectFromStorage: the REST clients open with a quota
// project when the storage client is kept out of it, and Google's transport refuses the
// storage client a quota project beside the HTTP client it appends itself, which is why
// NewGoogleBilledTo splits the options. The credentials are left out, so the test needs
// none. Each gRPC client is handed a connection that is never dialed in place of the
// quota project, so nothing is dialed; what NewGoogleBilledTo hands those four is read by
// TestNewGoogleBilledToKeepsTheQuotaProjectOffStorage.
func TestNewGoogleSplitsTheQuotaProjectFromStorage(t *testing.T) {
	// A storage client validates no option when this variable names an emulator, and would
	// open with the quota project: a machine that runs one is not to fail the test for it.
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("STORAGE_EMULATOR_HOST", "")

	ctx := context.Background()
	none := option.WithoutAuthentication()
	billed := []option.ClientOption{none, option.WithQuotaProject("imr-root-gbl-core-f2a4")}
	split, both := optionsForAll(billed, []option.ClientOption{none}), optionsForAll(billed, billed)
	for _, opts := range []*googleOptions{split, both} {
		opts.folders = []option.ClientOption{option.WithGRPCConn(undialed(t))}
		opts.projects = []option.ClientOption{option.WithGRPCConn(undialed(t))}
		opts.iam = []option.ClientOption{option.WithGRPCConn(undialed(t))}
		opts.secrets = []option.ClientOption{option.WithGRPCConn(undialed(t))}
	}
	g, err := newGoogle(ctx, split)
	if err != nil {
		t.Fatalf("newGoogle() with the storage client kept out of the quota project = %v", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if _, err := newGoogle(ctx, both); err == nil || !strings.Contains(err.Error(), "WithHTTPClient is incompatible with QuotaProject") {
		t.Errorf("newGoogle() with the quota project on the storage client too = %v, want Google's refusal, the reason for the split", err)
	}
}

// TestNewGoogleBilledToKeepsTheQuotaProjectOffStorage: NewGoogleBilledTo opens every
// client with the quota project it is given and nothing else, the storage client with
// nothing, and with no quota project it opens no client with anything. A quota project
// handed to the storage client would otherwise show at the break-glass account's terminal
// alone, as the refusal TestNewGoogleSplitsTheQuotaProjectFromStorage reads; one taken
// off another client, as that API's refusal of a person's credentials. The ten
// constructors are the package's variables, which hold functions here that record what
// NewGoogleBilledTo's set calls them with (recordGoogleOpenings) and open each client
// without credentials and without a network. The process's default credentials are named
// by a file that is not there, so a client opened by any other way than its variable
// cannot be opened at all.
func TestNewGoogleBilledToKeepsTheQuotaProjectOffStorage(t *testing.T) {
	// The constructors swapped below are the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	const project = "imr-root-gbl-core-f2a4"
	billed, nothing := [][]option.ClientOption{{option.WithQuotaProject(project)}}, [][]option.ClientOption{nil}
	tests := []struct {
		name         string
		quotaProject string
		want         map[string][][]option.ClientOption
	}{
		{
			// Every client is named, each opened once. A client added to the set is decided
			// here too: with the quota project, or beside the storage client.
			name:         "a quota project is handed to every client but the storage client",
			quotaProject: project,
			want: map[string][][]option.ClientOption{
				"folders": billed, "projects": billed, "iam": billed, "billing": billed, "services": billed, "servicesBeta": billed,
				"storage": nothing, "secrets": billed, "dns": billed, "budgets": billed,
			},
		},
		{
			name:         "no quota project is no option for any client",
			quotaProject: "",
			want: map[string][][]option.ClientOption{
				"folders": nothing, "projects": nothing, "iam": nothing, "billing": nothing, "services": nothing, "servicesBeta": nothing,
				"storage": nothing, "secrets": nothing, "dns": nothing, "budgets": nothing,
			},
		},
	}
	ctx := context.Background()
	opened := recordGoogleOpenings(t)
	for _, tt := range tests {
		opened.forget()
		g, err := NewGoogleBilledTo(ctx, tt.quotaProject)
		if err != nil {
			t.Errorf("%s: NewGoogleBilledTo(%q) = %v", tt.name, tt.quotaProject, err)

			continue
		}
		if err := g.Close(); err != nil {
			t.Errorf("%s: Close() = %v", tt.name, err)
		}
		if diff := cmp.Diff(tt.want, opened.calls(), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s: NewGoogleBilledTo(%q) opened a client with other options than it is to (-want +got):\n%s", tt.name, tt.quotaProject, diff)
		}
	}
}

// TestNewGoogleHandsEveryClientTheOptionsItIsGiven: NewGoogle opens every client, the
// storage client too, with the options it is given, in the order given and with nothing
// else, and with nothing when it is given none. NewGoogleAs gives it the creating
// identity's token source, and a client opened without it would act as the process's own
// identity, which shows at that client's first call alone. The ten constructors are the
// package's variables, which hold functions here that record what NewGoogle's set calls
// them with (recordGoogleOpenings) and open each client without credentials and without a
// network. The process's default credentials are named by a file that is not there, so a
// client opened by any other way than its variable cannot be opened at all.
func TestNewGoogleHandsEveryClientTheOptionsItIsGiven(t *testing.T) {
	// The constructors swapped below are the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	opened := recordGoogleOpenings(t)
	for _, given := range [][]option.ClientOption{
		nil,
		{option.WithUserAgent("bedrock-test"), option.WithRequestReason("a test of the options")},
	} {
		opened.forget()
		g, err := NewGoogle(ctx, given...)
		if err != nil {
			t.Errorf("NewGoogle() with %d option(s) = %v", len(given), err)

			continue
		}
		if err := g.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
		for name, got := range opened.calls() {
			if diff := cmp.Diff([][]option.ClientOption{given}, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("NewGoogle() with %d option(s) opened the %s client other than once, with those options (-want +got):\n%s", len(given), name, diff)
			}
		}
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
	// The variables are the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))
	// A storage client validates no option when this variable names an emulator, and would
	// open with the refused options: a machine that runs one is not to fail the test for it.
	t.Setenv("STORAGE_EMULATOR_HOST", "")

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

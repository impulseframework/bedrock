package create

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/oauth2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
)

// creator is the identity the tests open the clients as.
const creator = "sa-creator@imr-root-gbl-core-f2a4.iam.gserviceaccount.com"

// TestNewGoogleAsOpensEveryClientWithTheIdentityTokenSource: NewGoogleAs asks for the
// token of the identity it is given, with Cloud Platform's scope and no option, and opens
// every client of the set, the storage client too, with the source of that token and
// nothing else. A client opened without it would act as the process's own identity, the
// apply trigger's runner, which shows at that client's first call alone; an option given
// to the impersonation would change the credentials the identity is impersonated with, so
// any option fails the test. The impersonation and the ten constructors are the package's
// variables, which hold functions here that record what NewGoogleAs calls them with
// (recordImpersonations, recordGoogleOpenings) and open each client without credentials
// and without a network. Each client's options are read to be the one that carries the
// token source the impersonation answered, that very one. The process's default
// credentials are named by a file that is not there, so a client opened with nothing, by
// any other way than its variable, cannot be opened at all.
func TestNewGoogleAsOpensEveryClientWithTheIdentityTokenSource(t *testing.T) {
	// The impersonation and the constructors swapped below are the package's and the
	// variable the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	asked := recordImpersonations(t)
	opened := recordGoogleOpenings(t)
	g, err := NewGoogleAs(context.Background(), creator)
	if err != nil {
		t.Fatalf("NewGoogleAs() = %v", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	wantAsked := []impersonation{{config: impersonate.CredentialsConfig{TargetPrincipal: creator, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}}}}
	if diff := cmp.Diff(wantAsked, asked.calls, cmp.AllowUnexported(impersonation{}), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("NewGoogleAs() asked for other than that identity's token, once, with Cloud Platform's scope and no option (-want +got):\n%s", diff)
	}
	// The option holds the token source in a field of its own, which is not exported, so
	// each client's openings are compared whole: they are equal when the client was opened
	// once, with the option that holds the source the impersonation answered.
	want := [][]option.ClientOption{{option.WithTokenSource(asked.answers[0])}}
	for name, got := range opened.calls() {
		if !reflect.DeepEqual(want, got) {
			t.Errorf("NewGoogleAs() opened the %s client with %#v, want one opening with the source of the identity's token and nothing else, %#v", name, got, want)
		}
	}
}

// TestNewGoogleAsImpersonatesWithTheProcessCredentials: with nothing swapped, the
// impersonation NewGoogleAs asks for looks for the process's default credentials. Those
// are named by a file that is not there: the refusal names the file the impersonation
// looked for. It ends with the identity NewGoogleAs was given, as NewGoogleAs writes it
// into its own message, which says the refusal is the impersonation's and not a client's;
// whose token NewGoogleAs asks for is read by the test above.
func TestNewGoogleAsImpersonatesWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	g, err := NewGoogleAs(context.Background(), creator)
	if err == nil {
		_ = g.Close()
		t.Fatalf("NewGoogleAs() opened the clients with the process's credentials missing: the impersonation does not open with them")
	}
	if want := "impersonate.CredentialsTokenSource(): " + creator; !strings.HasSuffix(err.Error(), want) {
		t.Errorf("NewGoogleAs() = %v, want it to end with %q: the refusal is not the impersonation's", err, want)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("NewGoogleAs() = %v, want it to name %s: the impersonation did not look for the process's credentials", err, missing)
	}
}

// impersonation is one call of the impersonation: the identity and the scopes the token
// was asked for, and the options the call was given.
type impersonation struct {
	config  impersonate.CredentialsConfig
	options []option.ClientOption
}

// impersonations are the calls of the impersonation a test recorded, and the token
// source each was answered, in the order of the calls.
type impersonations struct {
	calls   []impersonation
	answers []oauth2.TokenSource
}

// recordImpersonations puts a function in the place of the package's impersonation for
// the length of the test, and answers the calls it gets. The function asks Google's own
// impersonation for the token source, for the identity and the scopes it was called
// with, and gives it an HTTP client that carries no request in place of the options it
// was called with: the source is made without credentials, and no test asks it for a
// token. The variable is the package's, so a test that swaps it does not run in parallel.
func recordImpersonations(t *testing.T) *impersonations {
	t.Helper()

	library := impersonatedTokenSource
	recorded := &impersonations{}
	impersonatedTokenSource = func(ctx context.Context, config impersonate.CredentialsConfig, opts ...option.ClientOption) (oauth2.TokenSource, error) {
		source, err := library(ctx, config, option.WithHTTPClient(&http.Client{Transport: &idleCloses{}}))
		recorded.calls = append(recorded.calls, impersonation{config: config, options: opts})
		recorded.answers = append(recorded.answers, source)

		return source, err
	}
	t.Cleanup(func() {
		impersonatedTokenSource = library
	})

	return recorded
}

// openings are the options each client of the set was opened with, opening by opening,
// under the name googleOptions lists the client by.
type openings map[string]*[][]option.ClientOption

// calls are the openings recorded so far, client by client.
func (o openings) calls() map[string][][]option.ClientOption {
	calls := make(map[string][][]option.ClientOption, len(o))
	for name, recorded := range o {
		calls[name] = *recorded
	}

	return calls
}

// forget drops the openings recorded so far, so the next set's are read alone.
func (o openings) forget() {
	for _, recorded := range o {
		*recorded = nil
	}
}

// recordGoogleOpenings puts a function in the place of each of the ten constructors the
// set opens its clients with, for the length of the test, and answers the calls they get
// (recordOpenings). In place of the options a constructor was called with, each gRPC
// client opens on a connection of its own that is never dialed, and each other client
// with the option that asks for no credentials: the set opens without credentials and
// without a network whatever its caller chose. Every list of googleOptions is to have a
// constructor here: a client added to the set without one would open with what its
// caller chose, unread.
func recordGoogleOpenings(t *testing.T) openings {
	t.Helper()

	onConnection := func() []option.ClientOption {
		return []option.ClientOption{option.WithGRPCConn(undialed(t))}
	}
	unauthenticated := func() []option.ClientOption {
		return []option.ClientOption{option.WithoutAuthentication()}
	}
	recorded := openings{
		"folders":      recordOpenings(t, &newFoldersClient, onConnection),
		"projects":     recordOpenings(t, &newProjectsClient, onConnection),
		"iam":          recordOpenings(t, &newIAMAdminClient, onConnection),
		"billing":      recordOpenings(t, &newBillingService, unauthenticated),
		"services":     recordOpenings(t, &newServiceUsageService, unauthenticated),
		"servicesBeta": recordOpenings(t, &newServiceUsageBeta, unauthenticated),
		"storage":      recordOpenings(t, &newStorageClient, unauthenticated),
		"secrets":      recordOpenings(t, &newSecretManagerClient, onConnection),
		"dns":          recordOpenings(t, &newDNSService, unauthenticated),
		"budgets":      recordOpenings(t, &newBudgetsClient, unauthenticated),
	}
	lists := reflect.TypeFor[googleOptions]()
	for i := range lists.NumField() {
		if name := lists.Field(i).Name; recorded[name] == nil {
			t.Fatalf("googleOptions has the list %s, and no constructor is recorded under that name: record the constructor its client opens with", name)
		}
	}

	return recorded
}

// recordOpenings puts a function in the constructor's place for the length of the test,
// and answers the calls it gets, each the options it was given. The function opens the
// client with the stand-in's options in place of the ones it was called with. The
// variable is the package's, so a test that swaps it does not run in parallel.
func recordOpenings[C any](t *testing.T, constructor *func(context.Context, ...option.ClientOption) (C, error), standIn func() []option.ClientOption) *[][]option.ClientOption {
	t.Helper()

	library := *constructor
	calls := &[][]option.ClientOption{}
	*constructor = func(ctx context.Context, opts ...option.ClientOption) (C, error) {
		*calls = append(*calls, opts)

		return library(ctx, standIn()...)
	}
	t.Cleanup(func() {
		*constructor = library
	})

	return calls
}

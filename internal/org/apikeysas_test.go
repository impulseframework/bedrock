package org

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/oauth2"
	"google.golang.org/api/apikeys/v2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
)

// asIdentity is the identity the tests open the API Keys client as.
const asIdentity = "sa-org-apply@imr-example-dev-core-9c1d.iam.gserviceaccount.com"

// TestNewKeyClientAsCallsAsTheIdentity: NewKeyClientAs asks for the token of the identity
// it is given, with Cloud Platform's scope and no option, opens the API Keys service with
// the source of that token and nothing else, and the client it opens calls with the token
// it was answered. A client opened as another identity, or with the process's own, shows
// that at its first call alone. The impersonation is the package's variable, which holds
// a function here that records what the constructor calls it with
// (recordImpersonations): an option the constructor gave it would change the credentials
// the identity is impersonated with, so any option fails the test. The token source it
// answers is Google's own, asking a stand-in for the IAM Credentials API that refuses
// every token and says whose it was asked for, so a call of the client ends at that
// refusal and its own request is never sent. That refusal comes before anything else the
// client was opened with is used: an option beside the token source, which could send
// the client's calls somewhere else or bill them to another project, shows at no call.
// So the service's constructor is the package's variable too, which holds a function
// here that records the options of the call and opens the service with them
// (recordKeysServiceOpenings): the options are read to be the one that carries the token
// source the impersonation answered, that very one. The process's default credentials
// are named by a file that is not there, so a client that was not handed the token
// source cannot be opened at all.
func TestNewKeyClientAsCallsAsTheIdentity(t *testing.T) {
	// The impersonation and the constructor swapped below are the package's and the
	// variable the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	asked := recordImpersonations(t)
	opened := recordKeysServiceOpenings(t)
	client, err := NewKeyClientAs(ctx, asIdentity)
	if err != nil {
		t.Fatalf("NewKeyClientAs() = %v", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	wantAsked := []impersonation{{config: impersonate.CredentialsConfig{TargetPrincipal: asIdentity, Scopes: []string{cloudPlatformScope}}}}
	if diff := cmp.Diff(wantAsked, asked.calls, cmp.AllowUnexported(impersonation{}), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("NewKeyClientAs() asked for other than that identity's token, once, with Cloud Platform's scope and no option (-want +got):\n%s", diff)
	}
	// The option holds the token source in a field of its own, which is not exported, so
	// the lists are compared whole: they are equal when the one option of the call holds
	// the source the impersonation answered. An impersonation asked for other than once is
	// reported above, and leaves no one source to compare with.
	if len(asked.answers) == 1 {
		if want := [][]option.ClientOption{{option.WithTokenSource(asked.answers[0])}}; !reflect.DeepEqual(want, *opened) {
			t.Errorf("NewKeyClientAs() called the service's constructor with %#v, want one call with the source of the identity's token and nothing else, %#v", *opened, want)
		}
	}
	want := tokenRefusal(asIdentity, cloudPlatformScope)
	if _, err := client.ProjectKeys(ctx, "imr-example-dev-core-9c1d"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a call of the client NewKeyClientAs() opened = %v, want the refusal %q: the client does not call with the token it asked for", err, want)
	}
}

// TestNewKeyClientAsImpersonatesWithTheProcessCredentials: with nothing swapped, the
// impersonation NewKeyClientAs asks for looks for the process's default credentials.
// Those are named by a file that is not there: the refusal names the file the
// impersonation looked for. It ends with the identity the constructor was given, as the
// constructor writes it into its own message, which says the refusal is the
// impersonation's and not the client's; whose token the constructor asks for is read by
// TestNewKeyClientAsCallsAsTheIdentity.
func TestNewKeyClientAsImpersonatesWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	client, err := NewKeyClientAs(context.Background(), asIdentity)
	if err == nil {
		_ = client.Close()
		t.Fatalf("NewKeyClientAs() opened a client with the process's credentials missing: the impersonation does not open with them")
	}
	if want := "impersonate.CredentialsTokenSource(): " + asIdentity; !strings.HasSuffix(err.Error(), want) {
		t.Errorf("NewKeyClientAs() = %v, want it to end with %q: the refusal is not the impersonation's", err, want)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("NewKeyClientAs() = %v, want it to name %s: the impersonation did not look for the process's credentials", err, missing)
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
// with, and gives it an HTTP client that stands in for the IAM Credentials API
// (tokenRefusals) in place of the options it was called with. The variable is the
// package's, so a test that swaps it does not run in parallel.
func recordImpersonations(t *testing.T) *impersonations {
	t.Helper()

	library := impersonatedTokenSource
	recorded := &impersonations{}
	impersonatedTokenSource = func(ctx context.Context, config impersonate.CredentialsConfig, opts ...option.ClientOption) (oauth2.TokenSource, error) {
		source, err := library(ctx, config, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
		recorded.calls = append(recorded.calls, impersonation{config: config, options: opts})
		recorded.answers = append(recorded.answers, source)

		return source, err
	}
	t.Cleanup(func() {
		impersonatedTokenSource = library
	})

	return recorded
}

// recordKeysServiceOpenings puts a function in the place of the API Keys service's
// constructor for the length of the test, and answers the calls it gets, each the options
// it was given. The function calls Google's constructor with those very options, so the
// service opens as its caller chose, and a call of the client still shows whose token it
// calls with. What it records is a copy, which nothing the constructor does with the list
// it is handed can change. The variable is the package's, so a test that swaps it does
// not run in parallel.
func recordKeysServiceOpenings(t *testing.T) *[][]option.ClientOption {
	t.Helper()

	library := newKeysService
	calls := &[][]option.ClientOption{}
	newKeysService = func(ctx context.Context, opts ...option.ClientOption) (*apikeys.Service, error) {
		*calls = append(*calls, slices.Clone(opts))

		return library(ctx, opts...)
	}
	t.Cleanup(func() {
		newKeysService = library
	})

	return calls
}

// tokenRefusals stands in for the IAM Credentials API, where a client opened as an
// identity asks for that identity's token: it refuses every request, and its refusal
// names the identity the token was asked for and the scopes it was asked with. A REST
// client asks for its token before it sends a request, so its first call fails with this
// refusal, which says whose token the client calls with, and the request is never sent.
type tokenRefusals struct{}

func (tokenRefusals) RoundTrip(r *http.Request) (*http.Response, error) {
	var asked struct {
		Scope []string `json:"scope"`
	}
	if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
		return nil, fmt.Errorf("the request for a token holds no JSON: %w", err)
	}
	identity := strings.TrimSuffix(path.Base(r.URL.Path), ":generateAccessToken")

	return &http.Response{
		StatusCode: http.StatusForbidden,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(tokenRefusal(identity, asked.Scope...))),
		Request:    r,
	}, nil
}

// tokenRefusal is what tokenRefusals answers a request for the identity's token with the
// scopes. The list of scopes is closed by brackets, so a request that asks with a scope
// more does not hold the refusal a test waits for.
func tokenRefusal(identity string, scopes ...string) string {
	return "no token for " + identity + " with the scopes [" + strings.Join(scopes, " ") + "]"
}

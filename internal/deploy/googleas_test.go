package deploy

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/oauth2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// asIdentity is the identity the tests open a client as.
const asIdentity = "sa-plan@imr-example-dev-core-9c1d.iam.gserviceaccount.com"

// TestStorageAsCallsAsTheIdentity: NewStorageAs asks for the token of the identity it is
// given with the scope that reads, NewStorageAsWriter with the scope that writes, each
// with no option, and the client each opens calls with the token it was answered. A
// client opened as the process's own identity, the deploy identity, may not read what the
// plan identity may; one opened with the other scope would write where it is to read
// alone, or be refused its write. The impersonation is the package's variable, which
// holds a function here that records what the constructor calls it with
// (recordImpersonations): an option the constructor gave it would change the credentials
// the identity is impersonated with, so any option fails the test. The token source it
// answers is Google's own, asking a stand-in for the IAM Credentials API that refuses
// every token and says whose it was asked for, and with which scopes, so a call of the
// client ends at that refusal and its own request is never sent. The process's default
// credentials are named by a file that is not there, so a client that was not handed the
// token source cannot be opened at all.
func TestStorageAsCallsAsTheIdentity(t *testing.T) {
	// The impersonation swapped below is the package's and the variables the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))
	// A storage client opens without credentials when this variable names an emulator, and
	// would never ask for the identity's token: a machine that runs one is not to fail the
	// test for it.
	t.Setenv("STORAGE_EMULATOR_HOST", "")

	ctx := context.Background()
	asked := recordImpersonations(t)
	tests := []struct {
		name      string
		open      func() (Store, error)
		wantScope string
	}{
		{
			name: "NewStorageAs",
			open: func() (Store, error) {
				return NewStorageAs(ctx, asIdentity)
			},
			wantScope: "https://www.googleapis.com/auth/devstorage.read_only",
		},
		{
			name: "NewStorageAsWriter",
			open: func() (Store, error) {
				return NewStorageAsWriter(ctx, asIdentity)
			},
			wantScope: "https://www.googleapis.com/auth/devstorage.read_write",
		},
	}
	for _, tt := range tests {
		asked.calls, asked.answers = nil, nil
		store, err := tt.open()
		if err != nil {
			t.Errorf("%s() = %v", tt.name, err)

			continue
		}
		wantAsked := []impersonation{{config: impersonate.CredentialsConfig{TargetPrincipal: asIdentity, Scopes: []string{tt.wantScope}}}}
		if diff := cmp.Diff(wantAsked, asked.calls, cmp.AllowUnexported(impersonation{}), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s() asked for other than that identity's token, once, with that scope and no option (-want +got):\n%s", tt.name, diff)
		}
		want := tokenRefusal(asIdentity, tt.wantScope)
		if _, err := store.Read(ctx, "imr-example-dev-records", "example/record.json"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a call of the client %s() opened = %v, want the refusal %q: the client does not call with the token it asked for", tt.name, err, want)
		}
		if err := store.Close(); err != nil {
			t.Errorf("Close() of the client %s() opened = %v", tt.name, err)
		}
	}
}

// TestNewSecretManagerAsOpensWithTheIdentityTokenSource: NewSecretManagerAs asks for the
// token of the identity it is given, with Cloud Platform's scope and no option, and calls
// the Secret Manager client's constructor with the source of that token and nothing
// else. The client is a gRPC client, which asks for its token only once it holds a
// connection to Google, so whose token it calls with cannot be read from a call of it.
// The constructor is the package's variable, which holds a function here that records
// the options of the call and opens the client on a connection that is never dialed, so
// without credentials and without a network; the options are read to be the one that
// carries the token source the impersonation answered, that very one. The process's
// default credentials are named by a file that is not there, so a client opened by any
// other way than the variable cannot be opened at all.
func TestNewSecretManagerAsOpensWithTheIdentityTokenSource(t *testing.T) {
	// The impersonation and the constructor swapped below are the package's and the
	// variable the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	asked := recordImpersonations(t)
	opened := recordSecretManagerOpenings(t)
	secrets, err := NewSecretManagerAs(context.Background(), asIdentity)
	if err != nil {
		t.Fatalf("NewSecretManagerAs() = %v", err)
	}
	if err := secrets.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	wantAsked := []impersonation{{config: impersonate.CredentialsConfig{TargetPrincipal: asIdentity, Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}}}}
	if diff := cmp.Diff(wantAsked, asked.calls, cmp.AllowUnexported(impersonation{}), cmpopts.EquateEmpty()); diff != "" {
		t.Fatalf("NewSecretManagerAs() asked for other than that identity's token, once, with Cloud Platform's scope and no option (-want +got):\n%s", diff)
	}
	// The option holds the token source in a field of its own, which is not exported, so
	// the lists are compared whole: they are equal when the option of the call holds the
	// source the impersonation answered.
	if want := [][]option.ClientOption{{option.WithTokenSource(asked.answers[0])}}; !reflect.DeepEqual(want, *opened) {
		t.Errorf("NewSecretManagerAs() called the client's constructor with %#v, want one call with the source of the identity's token and nothing else, %#v", *opened, want)
	}
}

// TestTheAsConstructorsImpersonateWithTheProcessCredentials: with nothing swapped, the
// impersonation each exported constructor asks for looks for the process's default
// credentials. Those are named by a file that is not there: the refusal names the file
// the impersonation looked for. It ends with the identity the constructor was given, as
// the constructor writes it into its own message, which says the refusal is the
// impersonation's and not the client's; whose token each constructor asks for is read by
// the tests above.
func TestTheAsConstructorsImpersonateWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	ctx := context.Background()
	tests := []struct {
		name string
		open func() (io.Closer, error)
	}{
		{
			name: "NewStorageAs",
			open: func() (io.Closer, error) {
				return NewStorageAs(ctx, asIdentity)
			},
		},
		{
			name: "NewStorageAsWriter",
			open: func() (io.Closer, error) {
				return NewStorageAsWriter(ctx, asIdentity)
			},
		},
		{
			name: "NewSecretManagerAs",
			open: func() (io.Closer, error) {
				return NewSecretManagerAs(ctx, asIdentity)
			},
		},
	}
	for _, tt := range tests {
		client, err := tt.open()
		if err == nil {
			_ = client.Close()
			t.Errorf("%s() opened a client with the process's credentials missing: the impersonation does not open with them", tt.name)

			continue
		}
		if want := "impersonate.CredentialsTokenSource(): " + asIdentity; !strings.HasSuffix(err.Error(), want) {
			t.Errorf("%s() = %v, want it to end with %q: the refusal is not the impersonation's", tt.name, err, want)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("%s() = %v, want it to name %s: the impersonation did not look for the process's credentials", tt.name, err, missing)
		}
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

// recordSecretManagerOpenings puts a function in the place of the Secret Manager
// client's constructor for the length of the test, and answers the calls it gets, each
// the options it was given. The function opens the client on a gRPC connection that is
// never dialed, in place of the options it was called with, so the client opens without
// credentials and without a network whatever the caller chose; closing the client closes
// the connection, and the end of the test does when the client was never closed. The
// connection carries TLS credentials, as a connection to Google does. The variable is the
// package's, so a test that swaps it does not run in parallel.
func recordSecretManagerOpenings(t *testing.T) *[][]option.ClientOption {
	t.Helper()

	conn, err := grpc.NewClient("passthrough:///cloud.invalid:443", grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	library := newSecretManagerClient
	calls := &[][]option.ClientOption{}
	newSecretManagerClient = func(ctx context.Context, opts ...option.ClientOption) (*secretmanager.Client, error) {
		*calls = append(*calls, opts)

		return library(ctx, option.WithGRPCConn(conn))
	}
	t.Cleanup(func() {
		newSecretManagerClient = library
		_ = conn.Close()
	})

	return calls
}

// tokenRefusals stands in for the IAM Credentials API, where a client opened as an
// identity asks for that identity's token: it refuses every request, and its refusal
// names the identity the token was asked for and the scopes it was asked with. A client
// that calls over HTTP asks for its token before it sends a request, so its first call
// fails with this refusal, which says whose token the client calls with, and the request
// is never sent.
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

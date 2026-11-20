package root

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"golang.org/x/oauth2"
	"google.golang.org/api/impersonate"
	"google.golang.org/api/option"
	"google.golang.org/api/siteverification/v1"
)

// asIdentity is the identity the tests open a client as.
const asIdentity = "sa-applier@imr-root-gbl-core-f2a4.iam.gserviceaccount.com"

// TestTheAsConstructorsCallAsTheIdentity: each of NewDNSAs, NewVerifierAs and NewBudgetsAs
// asks for the token of the identity it is given, with that constructor's scopes and
// with no option, and the client it opens calls with the token it was answered. A client
// opened as another identity, or with the process's own, shows that at its first call
// alone. The impersonation is the package's variable, which holds a function here that
// records what the constructor calls it with (recordImpersonations): an option the
// constructor gave it would change the credentials the identity is impersonated with, so
// any option fails the test. The token source it answers is Google's own, asking a
// stand-in for the IAM Credentials API that refuses every token and says whose it was
// asked for, so a call of the client ends at that refusal and its own request is never
// sent. The process's default credentials are named by a file that is not there, so a
// client that was not handed the token source cannot be opened at all.
func TestTheAsConstructorsCallAsTheIdentity(t *testing.T) {
	// The impersonation swapped below is the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	asked := recordImpersonations(t)
	tests := []struct {
		name string
		// call opens the client as the identity and makes one call of it.
		call func() error
		// wantScopes are the scopes the identity's token is asked for with.
		wantScopes []string
	}{
		{
			name: "NewDNSAs",
			call: func() error {
				client, err := NewDNSAs(ctx, asIdentity)
				if err != nil {
					return err
				}
				defer client.Close()
				_, err = client.Zones(ctx, "imr-root-gbl-net-0a1b")

				return err
			},
			wantScopes: []string{cloudPlatformScope},
		},
		{
			name: "NewVerifierAs",
			call: func() error {
				client, err := NewVerifierAs(ctx, asIdentity)
				if err != nil {
					return err
				}
				defer client.Close()
				_, err = client.Owned(ctx, "apps.example.com")

				return err
			},
			wantScopes: []string{cloudPlatformScope, siteverification.SiteverificationScope},
		},
		{
			name: "NewBudgetsAs",
			call: func() error {
				client, err := NewBudgetsAs(ctx, asIdentity)
				if err != nil {
					return err
				}
				defer client.Close()
				_, err = client.Budgets(ctx, "012345-6789AB-CDEF01")

				return err
			},
			wantScopes: []string{cloudPlatformScope},
		},
	}
	for _, tt := range tests {
		*asked = nil
		err := tt.call()
		wantAsked := []impersonation{{config: impersonate.CredentialsConfig{TargetPrincipal: asIdentity, Scopes: tt.wantScopes}}}
		if diff := cmp.Diff(wantAsked, *asked, cmp.AllowUnexported(impersonation{}), cmpopts.EquateEmpty()); diff != "" {
			t.Errorf("%s() asked for other than that identity's token, once, with those scopes and no option (-want +got):\n%s", tt.name, diff)
		}
		if want := tokenRefusal(asIdentity, tt.wantScopes...); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a call of the client %s() opened = %v, want the refusal %q: the client does not call with the token it asked for", tt.name, err, want)
		}
	}
}

// TestTheAsConstructorsImpersonateWithTheProcessCredentials: with nothing swapped, the
// impersonation each exported constructor asks for looks for the process's default
// credentials. Those are named by a file that is not there: the refusal names the file
// the impersonation looked for. It ends with the identity the constructor was given, as
// the constructor writes it into its own message, which says the refusal is the
// impersonation's and not the client's; whose token the constructor asks for is read by
// TestTheAsConstructorsCallAsTheIdentity.
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
			name: "NewDNSAs",
			open: func() (io.Closer, error) {
				return NewDNSAs(ctx, asIdentity)
			},
		},
		{
			name: "NewVerifierAs",
			open: func() (io.Closer, error) {
				return NewVerifierAs(ctx, asIdentity)
			},
		},
		{
			name: "NewBudgetsAs",
			open: func() (io.Closer, error) {
				return NewBudgetsAs(ctx, asIdentity)
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

// TestTheBilledConstructorsOpenWithTheQuotaProjectAlone: NewOrgPolicies and NewBudgets
// call their client's constructor with the quota project they are given and with nothing
// else. Without it a person's credentials, which name no project of their own, are
// refused by an API that wants one, which shows at the break-glass account's terminal
// alone; an option beside it could change the identity the client calls as. Each
// constructor is the package's variable, which holds a function here that records the
// options of the call (recordOpenings) and opens the client without credentials and
// without a network. The process's default credentials are named by a file that is not
// there, so a client opened by any other way than the variable cannot be opened at all.
func TestTheBilledConstructorsOpenWithTheQuotaProjectAlone(t *testing.T) {
	// The constructors swapped below are the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	const project = "imr-root-gbl-core-f2a4"
	tests := []struct {
		name string
		open func() (io.Closer, error)
		// calls are the calls of the client's constructor, each the options it was given.
		calls *[][]option.ClientOption
	}{
		{
			name: "NewOrgPolicies",
			open: func() (io.Closer, error) {
				return NewOrgPolicies(ctx, project)
			},
			calls: recordOpenings(t, &newOrgPolicyClient, option.WithGRPCConn(undialed(t))),
		},
		{
			name: "NewBudgets",
			open: func() (io.Closer, error) {
				return NewBudgets(ctx, project)
			},
			calls: recordOpenings(t, &newBudgetsClient, option.WithoutAuthentication()),
		},
	}
	want := [][]option.ClientOption{{option.WithQuotaProject(project)}}
	for _, tt := range tests {
		client, err := tt.open()
		if err != nil {
			t.Errorf("%s() = %v", tt.name, err)

			continue
		}
		_ = client.Close()
		if diff := cmp.Diff(want, *tt.calls); diff != "" {
			t.Errorf("%s() called its client's constructor with other than the quota project alone, once (-want +got):\n%s", tt.name, diff)
		}
	}
}

// TestTheBilledConstructorsOpenWithTheProcessCredentials: with nothing swapped,
// NewOrgPolicies and NewBudgets open their client with the process's default
// credentials. Those are named by a file that is not there, so neither client can be
// opened, and each refusal names the file the client looked for.
func TestTheBilledConstructorsOpenWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	ctx := context.Background()
	const project = "imr-root-gbl-core-f2a4"
	tests := []struct {
		name string
		open func() (io.Closer, error)
	}{
		{
			name: "NewOrgPolicies",
			open: func() (io.Closer, error) {
				return NewOrgPolicies(ctx, project)
			},
		},
		{
			name: "NewBudgets",
			open: func() (io.Closer, error) {
				return NewBudgets(ctx, project)
			},
		},
	}
	for _, tt := range tests {
		client, err := tt.open()
		if err == nil {
			_ = client.Close()
			t.Errorf("%s() opened a client with the process's credentials missing: the client does not open with them", tt.name)

			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("%s() = %v, want the refusal for want of %s", tt.name, err, missing)
		}
	}
}

// impersonation is one call of the impersonation: the identity and the scopes the token
// was asked for, and the options the call was given.
type impersonation struct {
	config  impersonate.CredentialsConfig
	options []option.ClientOption
}

// recordImpersonations puts a function in the place of the package's impersonation for
// the length of the test, and answers the calls it gets. The function asks Google's own
// impersonation for the token source, for the identity and the scopes it was called
// with, and gives it an HTTP client that stands in for the IAM Credentials API
// (tokenRefusals) in place of the options it was called with. The variable is the
// package's, so a test that swaps it does not run in parallel.
func recordImpersonations(t *testing.T) *[]impersonation {
	t.Helper()

	library := impersonatedTokenSource
	calls := &[]impersonation{}
	impersonatedTokenSource = func(ctx context.Context, config impersonate.CredentialsConfig, opts ...option.ClientOption) (oauth2.TokenSource, error) {
		*calls = append(*calls, impersonation{config: config, options: opts})

		return library(ctx, config, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
	}
	t.Cleanup(func() {
		impersonatedTokenSource = library
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

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
	"google.golang.org/api/option"
	"google.golang.org/api/siteverification/v1"
)

// asIdentity is the identity the tests open a client as.
const asIdentity = "sa-applier@imr-root-gbl-core-f2a4.iam.gserviceaccount.com"

// TestTheAsConstructorsCallAsTheIdentity: the client each of NewDNSAs, NewVerifierAs and
// NewBudgetsAs opens calls with the identity's token, asked for with that constructor's
// scopes. A client opened without the identity's token source would act as the process's
// own identity, which shows at its first call alone. Here the IAM Credentials API is a
// stand-in that refuses every token and says whose it was asked for, so a call of the
// client fails with that refusal before anything is sent. The process's default
// credentials are named by a file that is not there, so a client that was not handed the
// token source cannot be opened at all.
func TestTheAsConstructorsCallAsTheIdentity(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	standIn := option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}})
	tests := []struct {
		name string
		// call opens the client as the identity and makes one call of it.
		call func() error
		// wantScopes are the scopes the identity's token is asked for with.
		wantScopes []string
	}{
		{
			name: "newDNSAs",
			call: func() error {
				client, err := newDNSAs(ctx, asIdentity, standIn)
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
			name: "newVerifierAs",
			call: func() error {
				client, err := newVerifierAs(ctx, asIdentity, standIn)
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
			name: "newBudgetsAs",
			call: func() error {
				client, err := newBudgetsAs(ctx, asIdentity, standIn)
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
		want := tokenRefusal(asIdentity, tt.wantScopes...)
		if err := tt.call(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a call of the client %s() opened = %v, want the refusal %q: the client does not call with that identity's token, asked for with those scopes", tt.name, err, want)
		}
	}
}

// TestTheAsConstructorsImpersonateWithTheProcessCredentials: each exported constructor
// passes the identity it is given on, and gives the impersonation no option of its own,
// so the identity is impersonated with the process's default credentials. Those are named
// by a file that is not there: the refusal names the file the impersonation looked for,
// and ends with the identity it was to impersonate, which is the one given and no other.
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
			t.Errorf("%s() = %v, want it to end with %q: the identity impersonated is not the one given", tt.name, err, want)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("%s() = %v, want it to name %s: the impersonation did not look for the process's credentials", tt.name, err, missing)
		}
	}
}

// TestBilledToIsTheQuotaProjectAlone: NewOrgPolicies and NewBudgets open their client
// with the quota project they are given and with nothing else. Without it a person's
// credentials, which name no project of their own, are refused by an API that wants one,
// which shows at the break-glass account's terminal alone; an option beside it could
// change the identity the client calls as.
func TestBilledToIsTheQuotaProjectAlone(t *testing.T) {
	t.Parallel()

	const project = "imr-root-gbl-core-f2a4"
	if diff := cmp.Diff([]option.ClientOption{option.WithQuotaProject(project)}, billedTo(project)); diff != "" {
		t.Errorf("billedTo(%q) are other options than the quota project alone (-want +got):\n%s", project, diff)
	}
}

// TestTheBilledConstructorsOpenWithTheProcessCredentials: NewOrgPolicies and NewBudgets
// open their client with the process's default credentials. Those are named by a file
// that is not there, so neither client can be opened, and each refusal names the file the
// client looked for.
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

// tokenRefusals stands in for the IAM Credentials API, where a client opened as an
// identity asks for that identity's token: it refuses every request, and its refusal
// names the identity the token was asked for and the scopes it was asked with. A client
// asks for its token before it sends anything, so its first call fails with this refusal,
// which says whose token the client calls with, and no request leaves the test.
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

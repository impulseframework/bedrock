package org

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

	"google.golang.org/api/option"
)

// asIdentity is the identity the tests open the API Keys client as.
const asIdentity = "sa-org-apply@imr-example-dev-core-9c1d.iam.gserviceaccount.com"

// TestNewKeyClientAsCallsAsTheIdentity: the client NewKeyClientAs opens calls with the
// identity's token, asked for with Cloud Platform's scope. A client opened without the
// identity's token source would act as the process's own identity, which shows at its
// first call alone. Here the IAM Credentials API is a stand-in that refuses every token
// and says whose it was asked for, so a call of the client fails with that refusal before
// anything is sent. The process's default credentials are named by a file that is not
// there, so a client that was not handed the token source cannot be opened at all.
func TestNewKeyClientAsCallsAsTheIdentity(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	client, err := newKeyClientAs(ctx, asIdentity, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
	if err != nil {
		t.Fatalf("newKeyClientAs() = %v: with the process's credentials missing, a client that was not handed the identity's token source cannot be opened", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	want := tokenRefusal(asIdentity, cloudPlatformScope)
	if _, err := client.ProjectKeys(ctx, "imr-example-dev-core-9c1d"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a call of the client newKeyClientAs() opened = %v, want the refusal %q: the client does not call with that identity's token, asked for with that scope", err, want)
	}
}

// TestNewKeyClientAsImpersonatesWithTheProcessCredentials: NewKeyClientAs passes the
// identity it is given on, and gives the impersonation no option of its own, so the
// identity is impersonated with the process's default credentials. Those are named by a
// file that is not there: the refusal names the file the impersonation looked for, and
// ends with the identity it was to impersonate, which is the one given and no other.
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
		t.Errorf("NewKeyClientAs() = %v, want it to end with %q: the identity impersonated is not the one given", err, want)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("NewKeyClientAs() = %v, want it to name %s: the impersonation did not look for the process's credentials", err, missing)
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

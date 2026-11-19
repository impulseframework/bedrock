package create

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
)

// creator is the identity the tests open the clients as.
const creator = "sa-creator@imr-root-gbl-core-f2a4.iam.gserviceaccount.com"

// TestNewGoogleAsCallsAsTheIdentity: the clients NewGoogleAs opens call with the
// identity's token, asked for with Cloud Platform's scope. A set opened without the
// identity's token source would act as the process's own identity, the apply trigger's
// runner, which shows at the first call alone. Here the IAM Credentials API is a stand-in
// that refuses every token and says whose it was asked for, so a call of the set fails
// with that refusal before anything is sent. The process's default credentials are named
// by a file that is not there, so a set that was not handed the token source cannot be
// opened at all.
func TestNewGoogleAsCallsAsTheIdentity(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	g, err := newGoogleAs(ctx, creator, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
	if err != nil {
		t.Fatalf("newGoogleAs() = %v: with the process's credentials missing, clients that were not handed the identity's token source cannot be opened", err)
	}
	t.Cleanup(func() { _ = g.Close() })

	want := tokenRefusal(creator, cloudPlatformScope)
	if _, _, err := g.PublicZone(ctx, "imr-root-gbl-net-0a1b", "apps"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a call of the clients newGoogleAs() opened = %v, want the refusal %q: the clients do not call with that identity's token, asked for with that scope", err, want)
	}
}

// TestNewGoogleAsImpersonatesWithTheProcessCredentials: NewGoogleAs passes the identity
// it is given on, and gives the impersonation no option of its own, so the identity is
// impersonated with the process's default credentials. Those are named by a file that is
// not there: the refusal names the file the impersonation looked for, and ends with the
// identity it was to impersonate, which is the one given and no other.
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
		t.Errorf("NewGoogleAs() = %v, want it to end with %q: the identity impersonated is not the one given", err, want)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("NewGoogleAs() = %v, want it to name %s: the impersonation did not look for the process's credentials", err, missing)
	}
}

// TestNewGoogleBilledToOpensWithItsOptions: NewGoogleBilledTo opens the set with the
// options billedToOptions answers for the quota project it is given. The process's
// default credentials are named by a file made up for the test, a person's in form, so
// the clients open as they do at the break-glass account's terminal; no call is made, so
// no token is asked for with it. The set keeps one of its lists, the Secret Manager
// client's, for the regional clients it opens later, and the test reads the quota project
// from that one. The storage client was handed none, or the set would not have opened
// (TestNewGoogleSplitsTheQuotaProjectFromStorage).
func TestNewGoogleBilledToOpensWithItsOptions(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", madeUpCredentials(t))

	const project = "imr-root-gbl-core-f2a4"
	opened, err := NewGoogleBilledTo(context.Background(), project)
	if err != nil {
		t.Fatalf("NewGoogleBilledTo() = %v", err)
	}
	t.Cleanup(func() { _ = opened.Close() })
	g, ok := opened.(*google)
	if !ok {
		t.Fatalf("NewGoogleBilledTo() answered a %T, want the set of clients", opened)
	}
	if diff := cmp.Diff([]option.ClientOption{option.WithQuotaProject(project)}, g.secretOpts); diff != "" {
		t.Errorf("NewGoogleBilledTo(%q) opened the Secret Manager client with other options than the quota project (-want +got):\n%s", project, diff)
	}
}

// madeUpCredentials writes a file of application default credentials for the test and
// answers its path: a person's credentials in form, every value made up. A client opens
// with them without a network, and could get no token with them.
func madeUpCredentials(t *testing.T) string {
	t.Helper()

	file := filepath.Join(t.TempDir(), "application_default_credentials.json")
	const madeUp = `{"type":"authorized_user","client_id":"made-up","client_secret":"made-up","refresh_token":"made-up"}`
	if err := os.WriteFile(file, []byte(madeUp), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	return file
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

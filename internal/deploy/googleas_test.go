package deploy

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

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/secretmanager/apiv1/secretmanagerpb"
	"google.golang.org/api/option"
)

// asIdentity is the identity the tests open a client as.
const asIdentity = "sa-plan@imr-example-dev-core-9c1d.iam.gserviceaccount.com"

// TestStorageAsCallsAsTheIdentity: the client NewStorageAs opens calls with the
// identity's token asked for with the scope that reads, and the one NewStorageAsWriter
// opens with the scope that writes. A client opened without the identity's token source
// would act as the process's own identity, the deploy identity, which may not read what
// the plan identity may; one opened with the other scope would write where it is to read
// alone, or be refused its write. Here the IAM Credentials API is a stand-in that refuses
// every token and says whose it was asked for, and with which scopes, so a call of the
// client fails with that refusal before anything is sent. The process's default
// credentials are named by a file that is not there, so a client that was not handed the
// token source cannot be opened at all.
func TestStorageAsCallsAsTheIdentity(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	standIn := option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}})
	tests := []struct {
		name      string
		open      func() (Store, error)
		wantScope string
	}{
		{
			name: "newStorageAs",
			open: func() (Store, error) {
				return newStorageAs(ctx, asIdentity, standIn)
			},
			wantScope: "https://www.googleapis.com/auth/devstorage.read_only",
		},
		{
			name: "newStorageAsWriter",
			open: func() (Store, error) {
				return newStorageAsWriter(ctx, asIdentity, standIn)
			},
			wantScope: "https://www.googleapis.com/auth/devstorage.read_write",
		},
	}
	for _, tt := range tests {
		store, err := tt.open()
		if err != nil {
			t.Errorf("%s() = %v: with the process's credentials missing, a client that was not handed the identity's token source cannot be opened", tt.name, err)

			continue
		}
		want := tokenRefusal(asIdentity, tt.wantScope)
		if _, err := store.Read(ctx, "imr-example-dev-records", "example/record.json"); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a call of the client %s() opened = %v, want the refusal %q: the client does not call with that identity's token, asked for with that scope", tt.name, err, want)
		}
		if err := store.Close(); err != nil {
			t.Errorf("Close() of the client %s() opened = %v", tt.name, err)
		}
	}
}

// TestSecretManagerAsOptionsAreTheIdentityTokenSource: the options NewSecretManagerAs
// opens its client with are the source of the identity's token, asked for with Cloud
// Platform's scope. The client itself is a gRPC client, which asks for its token only
// once it holds a connection to Google, so the options are read with the REST form of the
// same client, which asks before it sends anything: the stand-in for the IAM Credentials
// API refuses the token and says whose it was asked for, and the call fails with that
// refusal. The process's default credentials are named by a file that is not there, so
// options that fell back on them open no client.
func TestSecretManagerAsOptionsAreTheIdentityTokenSource(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	opts, err := secretManagerAsOptions(ctx, asIdentity, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
	if err != nil {
		t.Fatalf("secretManagerAsOptions() = %v", err)
	}
	client, err := secretmanager.NewRESTClient(ctx, opts...)
	if err != nil {
		t.Fatalf("secretmanager.NewRESTClient() with the options = %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	want := tokenRefusal(asIdentity, "https://www.googleapis.com/auth/cloud-platform")
	_, err = client.AccessSecretVersion(ctx, &secretmanagerpb.AccessSecretVersionRequest{Name: "projects/imr-example-dev-core-9c1d/secrets/example/versions/1"})
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("a call of a client opened with secretManagerAsOptions() = %v, want the refusal %q: the options do not carry that identity's token source, asked for with that scope", err, want)
	}
}

// TestNewSecretManagerAsOpensWithItsOptions: the Secret Manager client is opened with the
// options secretManagerAsOptions answers. The process's default credentials are named by
// a file that is not there, so a client that was not handed the options looks for them
// and cannot be opened. No call is made: the client asks for no token.
func TestNewSecretManagerAsOpensWithItsOptions(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	secrets, err := newSecretManagerAs(context.Background(), asIdentity, option.WithHTTPClient(&http.Client{Transport: tokenRefusals{}}))
	if err != nil {
		t.Fatalf("newSecretManagerAs() = %v: with the process's credentials missing, a client that was not handed the identity's token source cannot be opened", err)
	}
	if err := secrets.Close(); err != nil {
		t.Errorf("Close() = %v", err)
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
			t.Errorf("%s() = %v, want it to end with %q: the identity impersonated is not the one given", tt.name, err, want)
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("%s() = %v, want it to name %s: the impersonation did not look for the process's credentials", tt.name, err, missing)
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

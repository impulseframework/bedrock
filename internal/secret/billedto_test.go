package secret

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TestNewSecretManagerOpensWithTheQuotaProjectAlone: NewSecretManager calls the Secret
// Manager client's constructor with the quota project it is given and with nothing else.
// Without it a person's credentials, which name no project of their own, are refused by
// an API that wants one, which shows at a person's terminal alone; an option beside it
// could change the identity the client calls as. The constructor is the package's
// variable, which holds a function here that records the options of each call and opens
// the client on a connection that is never dialed, so without credentials and without a
// network. The process's default credentials are named by a file that is not there, so a
// client opened by any other way than the variable cannot be opened at all.
func TestNewSecretManagerOpensWithTheQuotaProjectAlone(t *testing.T) {
	// The constructor swapped below is the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	const project = "lab-tst-1"
	calls := recordOpenings(t)
	if _, err := NewSecretManager(context.Background(), project); err != nil {
		t.Fatalf("NewSecretManager() = %v", err)
	}
	if diff := cmp.Diff([][]option.ClientOption{{option.WithQuotaProject(project)}}, *calls); diff != "" {
		t.Errorf("NewSecretManager() called the client's constructor with other than the quota project alone, once (-want +got):\n%s", diff)
	}
}

// TestNewSecretManagerOpensWithTheProcessCredentials: with nothing swapped,
// NewSecretManager opens the Secret Manager client with the process's default
// credentials. Those are named by a file that is not there, so the client cannot be
// opened, and the refusal names the file the client looked for.
func TestNewSecretManagerOpensWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	client, err := NewSecretManager(context.Background(), "lab-tst-1")
	if err == nil {
		if opened, ok := client.(*secretManager); ok {
			_ = opened.client.Close()
		}
		t.Fatalf("NewSecretManager() opened a client with the process's credentials missing: the client does not open with them")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("NewSecretManager() = %v, want the refusal for want of %s", err, missing)
	}
}

// recordOpenings puts a function in the place of the Secret Manager client's constructor
// for the length of the test, and answers the calls it gets, each the options it was
// given. The function opens the client on a gRPC connection that is never dialed, in
// place of the options it was called with, so the client opens without credentials and
// without a network whatever the caller chose; the connection is closed when the test
// ends, and every client opened on it with it. It carries TLS credentials, as a
// connection to Google does. The variable is the package's, so a test that swaps it does
// not run in parallel.
func recordOpenings(t *testing.T) *[][]option.ClientOption {
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

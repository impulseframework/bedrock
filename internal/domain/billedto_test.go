package domain

import (
	"context"
	"crypto/tls"
	"path/filepath"
	"strings"
	"testing"

	domains "cloud.google.com/go/domains/apiv1beta1"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// TestTheConstructorsOpenWithTheQuotaProjectAlone: NewCloudDomains and
// NewRegistrationReader call the Cloud Domains client's constructor with the quota
// project they are given and with nothing else. Without it a person's credentials, which
// name no project of their own, are refused by an API that wants one, which shows at a
// person's terminal alone; an option beside it could change the identity the client
// calls as. The constructor is the package's variable, which holds a function here that
// records the options of each call and opens the client on a connection that is never
// dialed, so without credentials and without a network. The process's default
// credentials are named by a file that is not there, so a client opened by any other way
// than the variable cannot be opened at all.
func TestTheConstructorsOpenWithTheQuotaProjectAlone(t *testing.T) {
	// The constructor swapped below is the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	ctx := context.Background()
	const project = "imr-root-gbl-net-0a1b"
	calls := recordOpenings(t)
	tests := []struct {
		name string
		open func() error
	}{
		{
			name: "NewCloudDomains",
			open: func() error {
				_, err := NewCloudDomains(ctx, project)

				return err
			},
		},
		{
			name: "NewRegistrationReader",
			open: func() error {
				_, err := NewRegistrationReader(ctx, project)

				return err
			},
		},
	}
	want := [][]option.ClientOption{{option.WithQuotaProject(project)}}
	for _, tt := range tests {
		*calls = nil
		if err := tt.open(); err != nil {
			t.Errorf("%s() = %v", tt.name, err)

			continue
		}
		if diff := cmp.Diff(want, *calls); diff != "" {
			t.Errorf("%s() called the client's constructor with other than the quota project alone, once (-want +got):\n%s", tt.name, diff)
		}
	}
}

// TestTheConstructorsOpenWithTheProcessCredentials: with nothing swapped, NewCloudDomains
// and NewRegistrationReader open the Cloud Domains client with the process's default
// credentials. Those are named by a file that is not there, so neither client can be
// opened, and each refusal names the file the client looked for.
func TestTheConstructorsOpenWithTheProcessCredentials(t *testing.T) {
	// The variable is the process's, so the test runs alone: t.Setenv forbids t.Parallel.
	missing := filepath.Join(t.TempDir(), "no-credentials.json")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", missing)

	ctx := context.Background()
	const project = "imr-root-gbl-net-0a1b"
	tests := []struct {
		name string
		open func() error
	}{
		{
			name: "NewCloudDomains",
			open: func() error {
				client, err := NewCloudDomains(ctx, project)
				if opened, ok := client.(*cloudDomains); ok {
					_ = opened.client.Close()
				}

				return err
			},
		},
		{
			name: "NewRegistrationReader",
			open: func() error {
				client, err := NewRegistrationReader(ctx, project)
				if err == nil {
					_ = client.Close()
				}

				return err
			},
		},
	}
	for _, tt := range tests {
		err := tt.open()
		if err == nil {
			t.Errorf("%s() opened a client with the process's credentials missing: the client does not open with them", tt.name)

			continue
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("%s() = %v, want the refusal for want of %s", tt.name, err, missing)
		}
	}
}

// recordOpenings puts a function in the place of the Cloud Domains client's constructor
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
	library := newDomainsClient
	calls := &[][]option.ClientOption{}
	newDomainsClient = func(ctx context.Context, opts ...option.ClientOption) (*domains.Client, error) {
		*calls = append(*calls, opts)

		return library(ctx, option.WithGRPCConn(conn))
	}
	t.Cleanup(func() {
		newDomainsClient = library
		_ = conn.Close()
	})

	return calls
}

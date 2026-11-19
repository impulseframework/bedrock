package root

import (
	"context"
	"crypto/tls"
	"testing"

	admin "cloud.google.com/go/iam/admin/apiv1"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

// TestIAMClientsClosePassesOverTheUnopened: Close closes the clients that were opened and
// passes over the ones that never were, which is what NewIAM leans on when its opening
// fails partway. NewIAM itself opens with the process's credentials and takes no option,
// so the clients here are opened by hand, each on a connection of its own.
func TestIAMClientsClosePassesOverTheUnopened(t *testing.T) {
	t.Parallel()

	t.Run("no client was opened", func(t *testing.T) {
		t.Parallel()

		c := &iamClients{}
		if err := c.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	t.Run("the first two clients were opened", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		first, second := undialed(t), undialed(t)
		c := &iamClients{}
		var err error
		if c.admin, err = admin.NewIamClient(ctx, option.WithGRPCConn(first)); err != nil {
			t.Fatal(err)
		}
		if c.projects, err = resourcemanager.NewProjectsClient(ctx, option.WithGRPCConn(second)); err != nil {
			t.Fatal(err)
		}
		if err := c.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
		for i, conn := range []*grpc.ClientConn{first, second} {
			if got := conn.GetState(); got != connectivity.Shutdown {
				t.Errorf("the connection of client %d is %v, want %v: Close left the client open", i+1, got, connectivity.Shutdown)
			}
		}
	})
}

// undialed is a gRPC connection that is never dialed: a client built on it opens without
// credentials and without a network, and the connection's state says whether the client
// was closed. It carries TLS credentials, as a connection to Google does.
func undialed(t *testing.T) *grpc.ClientConn {
	t.Helper()

	conn, err := grpc.NewClient("passthrough:///cloud.invalid:443", grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

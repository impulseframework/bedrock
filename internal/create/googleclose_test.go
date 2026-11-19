package create

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"

	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

// TestNewGoogleClosesWhatItOpened: an opening that fails partway answers the failure and
// closes the clients it had opened before it, passing over the ones it never opened.
func TestNewGoogleClosesWhatItOpened(t *testing.T) {
	t.Parallel()

	t.Run("the first client cannot be opened", func(t *testing.T) {
		t.Parallel()

		// Google's transport refuses scopes beside an audience, whatever the client: no
		// client is open when the opening fails, and closing must call none.
		opts := []option.ClientOption{option.WithoutAuthentication(), option.WithScopes(cloudPlatformScope), option.WithAudiences("https://cloud.invalid")}
		if _, err := newGoogle(context.Background(), opts, opts); err == nil || !strings.Contains(err.Error(), "resourcemanager.NewFoldersClient()") {
			t.Errorf("newGoogle() = %v, want the refusal of the folders client, the first to open", err)
		}
	})
	t.Run("the fourth client cannot be opened", func(t *testing.T) {
		t.Parallel()

		// The first three clients are gRPC clients, and they take the connection handed to
		// them here. Cloud Billing's REST client, the fourth, is refused beside a gRPC
		// connection: by then the connection is the three clients' to close, and its
		// state says whether the failed opening closed them.
		conn := undialed(t)
		opts := []option.ClientOption{option.WithoutAuthentication(), option.WithGRPCConn(conn)}
		if _, err := newGoogle(context.Background(), opts, opts); err == nil || !strings.Contains(err.Error(), "cloudbilling.NewService()") {
			t.Fatalf("newGoogle() = %v, want the refusal of Cloud Billing's client, the fourth to open", err)
		}
		if got := conn.GetState(); got != connectivity.Shutdown {
			t.Errorf("the connection of the three clients opened before the refusal is %v, want %v: the failed opening left them open", got, connectivity.Shutdown)
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

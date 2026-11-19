package root

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"

	admin "cloud.google.com/go/iam/admin/apiv1"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

// TestNewIAMClosesWhatItOpened: an opening that fails partway answers the failure, closes
// each client it had opened before it, and touches no client it never reached. Every
// client that holds a connection opens on one of its own, so each closing is read from
// its own connection.
func TestNewIAMClosesWhatItOpened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// refuse makes the opening of one client fail.
		refuse func(opts *iamOptions)
		// wantErr is the constructor the failure names.
		wantErr string
		// wantClosed is how many of the clients that hold a connection were open by then,
		// in the order they open: IAM, projects, folders, organizations, Cloud Storage.
		wantClosed int
	}{
		{
			name:       "the first client cannot be opened",
			refuse:     func(opts *iamOptions) { opts.admin = refused() },
			wantErr:    "admin.NewIamClient()",
			wantClosed: 0,
		},
		{
			// The four gRPC clients are open by then, and the storage client is not.
			name:       "the fifth client cannot be opened",
			refuse:     func(opts *iamOptions) { opts.billing = refused() },
			wantErr:    "cloudbilling.NewService()",
			wantClosed: 4,
		},
		{
			name:       "the last client cannot be opened",
			refuse:     func(opts *iamOptions) { opts.services = refused() },
			wantErr:    "serviceusage.NewService()",
			wantClosed: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts, holders := watchedIAMOptions(t)
			tt.refuse(opts)
			c, err := newIAM(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("newIAM() = %v, want the refusal of %s", err, tt.wantErr)
			}
			if c != nil {
				t.Errorf("newIAM() answered a client beside its error")
			}
			for i, h := range holders {
				switch opened, closed := i < tt.wantClosed, h.closed(); {
				case opened && !closed:
					t.Errorf("the %s client was open at the refusal and its connection is not closed: the failed opening left it open", h.name)
				case !opened && closed:
					t.Errorf("the %s client's connection is closed, and the opening stopped before that client", h.name)
				}
			}
		})
	}
}

// TestIAMClientsClosePassesOverTheUnopened: Close closes the clients that were opened and
// passes over the ones that never were, which is what newIAM leans on when its opening
// fails partway. The sets here are built by hand, each client on a connection of its own;
// TestNewIAMClosesWhatItOpened runs the opening itself.
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

// holder is a client that holds a connection, and how a test reads whether that
// connection was closed.
type holder struct {
	name   string
	closed func() bool
}

// watchedIAMOptions are options the whole set opens with, without credentials and
// without a network, and the clients that hold a connection in the order they open. Each
// gRPC client is given a connection of its own that is never dialed, and the storage
// client an HTTP client of its own. The two REST services between and after them open
// with nothing to close.
func watchedIAMOptions(t *testing.T) (*iamOptions, []holder) {
	t.Helper()

	iam, projects, folders, organizations, buckets := undialed(t), undialed(t), undialed(t), undialed(t), &idleCloses{}
	unauthenticated := []option.ClientOption{option.WithoutAuthentication()}
	opts := &iamOptions{
		admin:         []option.ClientOption{option.WithGRPCConn(iam)},
		projects:      []option.ClientOption{option.WithGRPCConn(projects)},
		folders:       []option.ClientOption{option.WithGRPCConn(folders)},
		organizations: []option.ClientOption{option.WithGRPCConn(organizations)},
		billing:       unauthenticated,
		storage:       []option.ClientOption{option.WithoutAuthentication(), option.WithHTTPClient(&http.Client{Transport: buckets})},
		services:      unauthenticated,
	}

	return opts, []holder{
		{name: "IAM", closed: shutDown(iam)},
		{name: "projects", closed: shutDown(projects)},
		{name: "folders", closed: shutDown(folders)},
		{name: "organizations", closed: shutDown(organizations)},
		{name: "Cloud Storage", closed: buckets.closed},
	}
}

// refused are options no Google client opens with: the transport refuses scopes beside an
// audience, whatever the client.
func refused() []option.ClientOption {
	return []option.ClientOption{option.WithoutAuthentication(), option.WithScopes(cloudPlatformScope), option.WithAudiences("https://cloud.invalid")}
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

// shutDown reads whether the connection was closed: gRPC sets the state inside Close,
// before it returns.
func shutDown(conn *grpc.ClientConn) func() bool {
	return func() bool {
		return conn.GetState() == connectivity.Shutdown
	}
}

// idleCloses is an HTTP transport that carries no request and counts how often it is told
// to close its idle connections, which is what closing the storage client comes to for
// the HTTP client the storage client was opened with.
type idleCloses struct {
	count int
}

func (*idleCloses) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("this transport carries no request")
}

func (c *idleCloses) CloseIdleConnections() {
	c.count++
}

// closed reads whether the storage client on this transport was closed.
func (c *idleCloses) closed() bool {
	return c.count > 0
}

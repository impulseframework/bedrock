package create

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	admin "cloud.google.com/go/iam/admin/apiv1"
	resourcemanager "cloud.google.com/go/resourcemanager/apiv3"
	secretmanager "cloud.google.com/go/secretmanager/apiv1"
	"cloud.google.com/go/storage"
	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/credentials"
)

// TestNewGoogleClosesWhatItOpened: an opening that fails partway answers the failure,
// closes each client it had opened before it, and touches no client it never reached.
// Every client that holds a connection opens on one of its own, so each closing is read
// from its own connection.
func TestNewGoogleClosesWhatItOpened(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// refuse makes the opening of one client fail.
		refuse func(opts *googleOptions)
		// wantErr is the constructor the failure names.
		wantErr string
		// wantClosed is how many of the clients that hold a connection were open by then,
		// in the order they open: folders, projects, IAM, Cloud Storage, Secret Manager.
		wantClosed int
	}{
		{
			name:       "the first client cannot be opened",
			refuse:     func(opts *googleOptions) { opts.folders = refused() },
			wantErr:    "resourcemanager.NewFoldersClient()",
			wantClosed: 0,
		},
		{
			// The three gRPC clients before it are open by then; the storage client and
			// the Secret Manager client are not.
			name:       "the fourth client cannot be opened",
			refuse:     func(opts *googleOptions) { opts.billing = refused() },
			wantErr:    "cloudbilling.NewService()",
			wantClosed: 3,
		},
		{
			name:       "the last client cannot be opened",
			refuse:     func(opts *googleOptions) { opts.budgets = refused() },
			wantErr:    "billingbudgets.NewService()",
			wantClosed: 5,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts, holders := watchedGoogleOptions(t)
			tt.refuse(opts)
			g, err := newGoogle(context.Background(), opts)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("newGoogle() = %v, want the refusal of %s", err, tt.wantErr)
			}
			if g != nil {
				t.Errorf("newGoogle() answered a client beside its error")
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

// TestGoogleCloseClosesEachClientItHolds: Close closes every client that holds a
// connection, a regional Secret Manager client among them, and passes over the ones that
// were never opened. The sets here are built by hand, each client on a connection of its
// own; TestNewGoogleClosesWhatItOpened runs the opening itself.
func TestGoogleCloseClosesEachClientItHolds(t *testing.T) {
	t.Parallel()

	t.Run("no client was opened", func(t *testing.T) {
		t.Parallel()

		g := &google{}
		if err := g.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
	})
	t.Run("every client that holds a connection was opened", func(t *testing.T) {
		t.Parallel()

		ctx := context.Background()
		opts, holders := watchedGoogleOptions(t)
		regional := undialed(t)
		g := &google{regional: map[string]*secretmanager.Client{}}
		var err error
		if g.folders, err = resourcemanager.NewFoldersClient(ctx, opts.folders...); err != nil {
			t.Fatal(err)
		}
		if g.projects, err = resourcemanager.NewProjectsClient(ctx, opts.projects...); err != nil {
			t.Fatal(err)
		}
		if g.iam, err = admin.NewIamClient(ctx, opts.iam...); err != nil {
			t.Fatal(err)
		}
		if g.storage, err = storage.NewClient(ctx, opts.storage...); err != nil {
			t.Fatal(err)
		}
		if g.secrets, err = secretmanager.NewClient(ctx, opts.secrets...); err != nil {
			t.Fatal(err)
		}
		if g.regional["us-central1"], err = secretmanager.NewClient(ctx, option.WithGRPCConn(regional)); err != nil {
			t.Fatal(err)
		}
		if err := g.Close(); err != nil {
			t.Errorf("Close() = %v", err)
		}
		for _, h := range append(holders, holder{name: "regional Secret Manager", closed: shutDown(regional)}) {
			if !h.closed() {
				t.Errorf("the connection of the %s client is not closed: Close left the client open", h.name)
			}
		}
	})
}

// TestARegionalSecretClientOpensWithTheSecretManagerClientsOptions: the client for a
// regional secret opens at the region's endpoint, named from the location in the secret's
// name, and with the options the set's Secret Manager client opened with after it. Under
// NewGoogleAs those options are the creating identity's token source, and under
// NewGoogleBilledTo the quota project: a regional client opened without them would act as
// the process's own identity, or bill no project, and one opened at the global endpoint
// would be refused the regional secret; only a regional secret's first policy read would
// show either. Here the set is NewGoogleBilledTo's, and the constructors are the
// package's variables, which hold functions that record what they are called with
// (recordGoogleOpenings): the Secret Manager client's constructor is called once for the
// set, and once for the region with the region's endpoint and then the quota project. A
// second secret of the region opens no further client, and a secret that names no
// location is answered the set's own client.
func TestARegionalSecretClientOpensWithTheSecretManagerClientsOptions(t *testing.T) {
	// The constructors swapped below are the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	const project = "imr-root-gbl-core-f2a4"
	ctx := context.Background()
	opened := recordGoogleOpenings(t)
	set, err := NewGoogleBilledTo(ctx, project)
	if err != nil {
		t.Fatalf("NewGoogleBilledTo() = %v", err)
	}
	g, ok := set.(*google)
	if !ok {
		t.Fatalf("NewGoogleBilledTo() answered a %T, want the set of clients", set)
	}
	t.Cleanup(func() {
		_ = g.Close()
	})

	regional, err := g.secretClient(ctx, "projects/"+project+"/locations/us-central1/secrets/github-app-key")
	if err != nil {
		t.Fatalf("secretClient() for a regional secret = %v", err)
	}
	if regional == g.secrets {
		t.Fatal("secretClient() answered the set's own client for a regional secret")
	}
	if again, err := g.secretClient(ctx, "projects/"+project+"/locations/us-central1/secrets/webhook-secret"); err != nil || again != regional {
		t.Errorf("secretClient() for a second secret of the region = %p, %v, want the region's client, %p", again, err, regional)
	}
	if global, err := g.secretClient(ctx, "projects/"+project+"/secrets/github-app-key"); err != nil || global != g.secrets {
		t.Errorf("secretClient() for a secret that names no location = %p, %v, want the set's own client, %p", global, err, g.secrets)
	}
	want := [][]option.ClientOption{
		{option.WithQuotaProject(project)},
		{option.WithEndpoint("secretmanager.us-central1.rep.googleapis.com:443"), option.WithQuotaProject(project)},
	}
	if diff := cmp.Diff(want, *opened["secrets"]); diff != "" {
		t.Errorf("the Secret Manager client's constructor was called other than once with the set's options, and once with the region's endpoint and then those options (-want +got):\n%s", diff)
	}
}

// TestARegionalSecretClientOpensWithNoOtherClientsOptions: the options the set keeps for
// its regional clients are the Secret Manager client's list, and no other client's. Every
// exported constructor gives the Secret Manager client the list it gives the folders
// client and each other client but storage's, so a set one of them opened cannot show
// whose list was kept: a set that kept the folders client's list passes the test above.
// Here the set is opened with one quota project in every other client's list and another
// in the Secret Manager client's, and the constructors are the package's variables, which
// hold functions that record what they are called with (recordGoogleOpenings): the
// Secret Manager client's constructor is called for the region with the region's endpoint
// and then the Secret Manager client's own project.
func TestARegionalSecretClientOpensWithNoOtherClientsOptions(t *testing.T) {
	// The constructors swapped below are the package's and the variable the process's, so
	// the test runs alone: t.Setenv forbids t.Parallel.
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "no-credentials.json"))

	const others, own = "imr-root-gbl-core-f2a4", "imr-root-gbl-core-7c3e"
	ctx := context.Background()
	opened := recordGoogleOpenings(t)
	opts := optionsForAll([]option.ClientOption{option.WithQuotaProject(others)}, []option.ClientOption{option.WithQuotaProject(others)})
	opts.secrets = []option.ClientOption{option.WithQuotaProject(own)}
	set, err := newGoogle(ctx, opts)
	if err != nil {
		t.Fatalf("newGoogle() = %v", err)
	}
	g, ok := set.(*google)
	if !ok {
		t.Fatalf("newGoogle() answered a %T, want the set of clients", set)
	}
	t.Cleanup(func() {
		_ = g.Close()
	})

	if _, err := g.secretClient(ctx, "projects/"+own+"/locations/us-central1/secrets/github-app-key"); err != nil {
		t.Fatalf("secretClient() for a regional secret = %v", err)
	}
	want := [][]option.ClientOption{
		{option.WithQuotaProject(own)},
		{option.WithEndpoint("secretmanager.us-central1.rep.googleapis.com:443"), option.WithQuotaProject(own)},
	}
	if diff := cmp.Diff(want, *opened["secrets"]); diff != "" {
		t.Errorf("the Secret Manager client's constructor was called other than once with its own list, and once with the region's endpoint and then that list (-want +got):\n%s", diff)
	}
}

// holder is a client that holds a connection, and how a test reads whether that
// connection was closed.
type holder struct {
	name   string
	closed func() bool
}

// watchedGoogleOptions are options the whole set opens with, without credentials and
// without a network, and the clients that hold a connection in the order they open. Each
// gRPC client is given a connection of its own that is never dialed, and the storage
// client an HTTP client of its own. The REST services between and after them open with
// nothing to close.
func watchedGoogleOptions(t *testing.T) (*googleOptions, []holder) {
	t.Helper()

	folders, projects, iam, secrets, buckets := undialed(t), undialed(t), undialed(t), undialed(t), &idleCloses{}
	unauthenticated := []option.ClientOption{option.WithoutAuthentication()}
	opts := optionsForAll(unauthenticated, []option.ClientOption{option.WithoutAuthentication(), option.WithHTTPClient(&http.Client{Transport: buckets})})
	opts.folders = []option.ClientOption{option.WithGRPCConn(folders)}
	opts.projects = []option.ClientOption{option.WithGRPCConn(projects)}
	opts.iam = []option.ClientOption{option.WithGRPCConn(iam)}
	opts.secrets = []option.ClientOption{option.WithGRPCConn(secrets)}

	return opts, []holder{
		{name: "folders", closed: shutDown(folders)},
		{name: "projects", closed: shutDown(projects)},
		{name: "IAM", closed: shutDown(iam)},
		{name: "Cloud Storage", closed: buckets.closed},
		{name: "Secret Manager", closed: shutDown(secrets)},
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

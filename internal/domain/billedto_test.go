package domain

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
)

// TestBilledToIsTheQuotaProjectAlone: NewCloudDomains and NewRegistrationReader open the
// Cloud Domains client with the quota project they are given and with nothing else.
// Without it a person's credentials, which name no project of their own, are refused by
// an API that wants one, which shows at a person's terminal alone; an option beside it
// could change the identity the client calls as.
func TestBilledToIsTheQuotaProjectAlone(t *testing.T) {
	t.Parallel()

	const project = "imr-root-gbl-net-0a1b"
	if diff := cmp.Diff([]option.ClientOption{option.WithQuotaProject(project)}, billedTo(project)); diff != "" {
		t.Errorf("billedTo(%q) are other options than the quota project alone (-want +got):\n%s", project, diff)
	}
}

// TestTheConstructorsOpenWithTheProcessCredentials: NewCloudDomains and
// NewRegistrationReader open the Cloud Domains client with the process's default
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

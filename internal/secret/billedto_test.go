package secret

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"google.golang.org/api/option"
)

// TestBilledToIsTheQuotaProjectAlone: NewSecretManager opens the Secret Manager client
// with the quota project it is given and with nothing else. Without it a person's
// credentials, which name no project of their own, are refused by an API that wants one,
// which shows at a person's terminal alone; an option beside it could change the identity
// the client calls as.
func TestBilledToIsTheQuotaProjectAlone(t *testing.T) {
	t.Parallel()

	const project = "lab-tst-1"
	if diff := cmp.Diff([]option.ClientOption{option.WithQuotaProject(project)}, billedTo(project)); diff != "" {
		t.Errorf("billedTo(%q) are other options than the quota project alone (-want +got):\n%s", project, diff)
	}
}

// TestNewSecretManagerOpensWithTheProcessCredentials: NewSecretManager opens the Secret
// Manager client with the process's default credentials. Those are named by a file that
// is not there, so the client cannot be opened, and the refusal names the file the client
// looked for.
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

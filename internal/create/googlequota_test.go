package create

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/api/option"
)

// TestNewGoogleSplitsTheQuotaProjectFromStorage: the clients open with a quota project
// when the storage client is kept out of it, and Google's transport refuses the storage
// client a quota project beside the HTTP client it appends itself, which is why
// NewGoogleBilledTo splits the options. No client dials at construction; the credentials
// are left out so the test needs none.
func TestNewGoogleSplitsTheQuotaProjectFromStorage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	none := option.WithoutAuthentication()
	billed := []option.ClientOption{none, option.WithQuotaProject("imr-root-gbl-core-f2a4")}
	g, err := newGoogle(ctx, optionsForAll(billed, []option.ClientOption{none}))
	if err != nil {
		t.Fatalf("newGoogle() with the storage client kept out of the quota project = %v", err)
	}
	if err := g.Close(); err != nil {
		t.Errorf("Close() = %v", err)
	}
	if _, err := newGoogle(ctx, optionsForAll(billed, billed)); err == nil || !strings.Contains(err.Error(), "WithHTTPClient is incompatible with QuotaProject") {
		t.Errorf("newGoogle() with the quota project on the storage client too = %v, want Google's refusal, the reason for the split", err)
	}
}

// TestOptionsForAllLeavesNoClientOut: every client of the set is given the options the
// constructors pass, and the storage client its own. A client added to the set and left
// out of optionsForAll would open with no options at all: with the process's own
// credentials where the others open as the creating identity, and with no quota project.
func TestOptionsForAllLeavesNoClientOut(t *testing.T) {
	t.Parallel()

	none := option.WithoutAuthentication()
	shared, storage := []option.ClientOption{none}, []option.ClientOption{none, none}
	got := reflect.ValueOf(optionsForAll(shared, storage)).Elem()
	for i := range got.NumField() {
		name, want := got.Type().Field(i).Name, len(shared)
		if name == "storage" {
			want = len(storage)
		}
		if n := got.Field(i).Len(); n != want {
			t.Errorf("optionsForAll() gives the %s client %d option(s), want %d", name, n, want)
		}
	}
}

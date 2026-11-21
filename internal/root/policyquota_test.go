package root_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/errors/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
	"github.com/impulseframework/bedrock/internal/root/roottest"
)

// The two hosting organizations' own folders: imp's as the fixture records it, and the one
// these tests record for rgb.
const (
	impFolder = "123456789012345"
	rgbFolder = "223456789012345"
)

// policyFolderIDs are the folders of a hosting organization whose policies the root
// organization keeps, in the order it sets them: the organization's own, then the four
// beneath it by key.
func policyFolderIDs(own string, beneath map[string]string) []string {
	return []string{own, beneath["shared"], beneath["tst"], beneath["stg"], beneath["prd"]}
}

// withSecondOrganization makes rgb a second hosting organization that stands: recorded
// with its folder in the root organization's placement, and its record in the repository
// with four folders of its own.
func withSecondOrganization(t *testing.T, w *world) {
	t.Helper()

	record, err := org.ReadRecord(w.dir, "imp")
	if err != nil {
		t.Fatal(err)
	}
	record.Prefix = "rgb"
	record.Folders = rgbFolders()
	withRecordOf(t, w.dir, record)
	w.p.HostingOrganizations = append(w.p.HostingOrganizations, org.HostingOrganization{Prefix: "rgb", BootProject: "rgb-boot-gbl-core-e5f6", Folder: rgbFolder})
}

// withRecordOf writes the hosting organization's record into dir, a root repository.
func withRecordOf(t *testing.T, dir string, record *org.Placement) {
	t.Helper()

	path := org.RecordPath(dir, record.Prefix)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := record.Write(path); err != nil {
		t.Fatal(err)
	}
}

// waiting makes the run's waits the test's: each is noted and moves the quota's clock on,
// and returns at once.
func waiting(w *world) *[]time.Duration {
	var waits []time.Duration
	w.clients.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		w.fake.Wait(d)

		return nil
	}

	return &waits
}

// policyCalls are the Org Policy calls the stand-in recorded, in order.
func policyCalls(f *roottest.Fake) []string {
	var calls []string
	for _, call := range f.Calls {
		if strings.HasPrefix(call, "ListPolicies(") || strings.HasPrefix(call, "CreatePolicy(") || strings.HasPrefix(call, "UpdatePolicy(") {
			calls = append(calls, call)
		}
	}

	return calls
}

// TestPoliciesAreListedByFolder: the root organization's apply over two hosting
// organizations whose policies all stand, a check and then the apply inside one minute
// as the apply trigger's build runs them, finishes under the quota Google gives a project
// by default, and reads each folder's policies with one list: five calls for a hosting
// organization in each pass, where a read of each policy by its name made 96 and the two
// passes over two organizations 384, past the 300 reads a minute the lab's root apply was
// refused at.
func TestPoliciesAreListedByFolder(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	imp, err := org.ReadRecord(w.dir, "imp")
	if err != nil {
		t.Fatal(err)
	}
	withSecondOrganization(t, w)
	w.run(t, root.Apply, true)
	w.fake.PolicyQuota = roottest.GooglePolicyQuota()
	waits := waiting(w)

	for _, mode := range []root.Mode{root.Check, root.Apply} {
		w.fake.Calls = nil
		var out bytes.Buffer
		report, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: mode, Commit: "abc1234", GitHubToken: "test-token"}, &out)
		if err != nil {
			t.Fatalf("Run(%v) over two hosting organizations error = %v\n%s", mode, errors.Cause(err), out.String())
		}
		if !report.Clean() {
			t.Errorf("Run(%v) is not clean: %v, findings %q", mode, whats(report), report.Findings)
		}
		var want []string
		for _, folder := range slices.Concat(policyFolderIDs(impFolder, imp.Folders), policyFolderIDs(rgbFolder, rgbFolders())) {
			want = append(want, "ListPolicies(folders/"+folder+")")
		}
		if got := policyCalls(w.fake); !slices.Equal(got, want) {
			t.Errorf("Run(%v) made the Org Policy calls\n  %v\nwant one list of each folder and no other, in the order the policies are set:\n  %v", mode, got, want)
		}
	}
	if len(*waits) != 0 {
		t.Errorf("the two runs waited %v, want no wait: ten lists a pass are far inside the quota", *waits)
	}
}

// TestPolicyCallsWaitOutTheQuota: an Org Policy call Google refuses for the project's
// quota, which it counts by the minute, is made again after waits that grow, and goes
// through once the minute has turned: a list, a policy made and a policy replaced.
func TestPolicyCallsWaitOutTheQuota(t *testing.T) {
	t.Parallel()

	refusal := status.Error(codes.ResourceExhausted, "Quota exceeded for quota metric 'Policy write requests' and limit 'Policy write requests per minute' of service 'orgpolicy.googleapis.com' for consumer 'project_number:100000000011'.")
	tests := []struct {
		name string
		// arrange leaves the world as the run under test finds it, and names the run's
		// mode, the waits it makes and the line its first wait prints.
		arrange   func(t *testing.T, w *world) root.Mode
		wantWaits []time.Duration
		waiting   string
		applied   int
	}{
		{
			// Three lists a minute: the check's fourth list, of the stg folder, is refused
			// until the minute turns, 65 seconds of waits on.
			name: "a list",
			arrange: func(t *testing.T, w *world) root.Mode {
				t.Helper()

				w.run(t, root.Apply, true)
				w.fake.PolicyQuota = roottest.GooglePolicyQuota()
				w.fake.PolicyQuota.Lists = 3

				return root.Check
			},
			wantWaits: []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second},
			waiting:   "  (Google refuses to list the organization policies on folder stg (900000000013) of imp: the quota of the root project imr-root-gbl-core-c3d4, which it counts by the minute, is spent; trying again in 5s)\n",
		},
		{
			// Fifty writes a minute: of the 96 policies the apply makes, the 51st is refused
			// until the minute turns, and the 46 after it fit in the next.
			name: "a policy made",
			arrange: func(t *testing.T, w *world) root.Mode {
				t.Helper()

				w.run(t, root.Apply, true)
				w.fake.OrgPolicies = map[string]*root.Policy{}
				w.fake.PolicyQuota = roottest.GooglePolicyQuota()
				w.fake.PolicyQuota.Writes = 50

				return root.Apply
			},
			wantWaits: []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 30 * time.Second},
			waiting:   "  (Google refuses to set iam.serviceAccountKeyExpiryHours on folder stg (900000000013) of imp: the quota of the root project imr-root-gbl-core-c3d4, which it counts by the minute, is spent; trying again in 5s)\n",
			applied:   96,
		},
		{
			// A policy moved by hand, and Google refuses its replacement once.
			name: "a policy replaced",
			arrange: func(t *testing.T, w *world) root.Mode {
				t.Helper()

				w.run(t, root.Apply, true)
				w.fake.OrgPolicies["folders/900000000014/policies/compute.vmExternalIpAccess"].Rules = []root.PolicyRule{{AllowAll: true}}
				w.fake.RefuseOnce["UpdatePolicy"] = errors.Wrap(refusal, "orgpolicy.Client.UpdatePolicy()")

				return root.Apply
			},
			wantWaits: []time.Duration{5 * time.Second},
			waiting:   "  (Google refuses to bring compute.vmExternalIpAccess on folder prd (900000000014) of imp to the baseline: the quota of the root project imr-root-gbl-core-c3d4, which it counts by the minute, is spent; trying again in 5s)\n",
			applied:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			mode := tt.arrange(t, w)
			waits := waiting(w)
			var out bytes.Buffer
			report, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: mode, Commit: "abc1234", GitHubToken: "test-token"}, &out)
			if err != nil {
				t.Fatalf("Run(%v) error = %v, want the refused call made again until Google takes it\n%s", mode, errors.Cause(err), out.String())
			}
			if report.Applied != tt.applied {
				t.Errorf("the run made %d change(s), want %d:\n%s", report.Applied, tt.applied, out.String())
			}
			if !slices.Equal(*waits, tt.wantWaits) {
				t.Errorf("the run waited %v, want %v", *waits, tt.wantWaits)
			}
			if !strings.Contains(out.String(), tt.waiting) {
				t.Errorf("the run's log lacks %q:\n%s", tt.waiting, out.String())
			}
			w.fake.PolicyQuota = nil
			if clean, out := w.run(t, root.Check, false); !clean.Clean() {
				t.Errorf("the check after the run is not clean:\n%s\n%v", out, whats(clean))
			}
		})
	}
}

// TestPolicyQuotaSpentPastTheWait: Google refuses an Org Policy call for the quota through
// two minutes of tries, twice the minute it counts the quota over, and the run stops
// there with a sentence that names the call, the project the quota is counted for, what
// Google answered, and the way on, which is the run again as its mode has it run.
func TestPolicyQuotaSpentPastTheWait(t *testing.T) {
	t.Parallel()

	const (
		listRefused  = "Quota exceeded for quota metric 'Policy list requests' and limit 'Policy list requests per minute' of service 'orgpolicy.googleapis.com' for consumer 'project_number:100000000011'."
		writeRefused = "Quota exceeded for quota metric 'Policy write requests' and limit 'Policy write requests per minute' of service 'orgpolicy.googleapis.com' for consumer 'project_number:100000000011'."
		spent        = ": the quota of the root project imr-root-gbl-core-c3d4 is spent ("
		billed       = "). The organization policy calls of this run are billed to that project, Google counts them by the minute, and it still refused after 2 minutes of tries: another build or person billed to the project may be spending the quota. "
		list         = "Google refuses to list the organization policies on folder organization (123456789012345) of imp"
	)
	tests := []struct {
		name      string
		mode      root.Mode
		bootstrap bool
		// noWrites refuses every write, and the policies are gone, so the run stops at
		// the first policy it makes; otherwise every list is refused.
		noWrites bool
		want     string
		// failed is the change the stopped apply's report marks as the one it stopped at.
		failed string
	}{
		{name: "a check", mode: root.Check, want: list + spent + listRefused + billed + "Nothing was changed: run the check again (bedrock org check, or the trigger whose build ran it)"},
		{name: "a plan", mode: root.Plan, want: list + spent + listRefused + billed + "Nothing was changed: run the plan trigger imr-root-uc1-plan again"},
		{name: "an apply", mode: root.Apply, want: list + spent + listRefused + billed + "What this run made stands: run the apply trigger imr-root-uc1-apply again, and it goes on from here"},
		{name: "the bootstrap's apply", mode: root.Apply, bootstrap: true, want: list + spent + listRefused + billed + "What this run made stands: run bedrock org bootstrap again, and it goes on from here"},
		{
			name: "an apply, at a policy it makes", mode: root.Apply, noWrites: true,
			want:   "Google refuses to set compute.skipDefaultNetworkCreation on folder organization (123456789012345) of imp" + spent + writeRefused + billed + "What this run made stands: run the apply trigger imr-root-uc1-apply again, and it goes on from here",
			failed: "organization policies: sets compute.skipDefaultNetworkCreation on folder organization (123456789012345) of imp",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := newWorld(t)
			w.run(t, root.Apply, true)
			w.fake.PolicyQuota = roottest.GooglePolicyQuota()
			if tt.noWrites {
				w.fake.OrgPolicies = map[string]*root.Policy{}
				w.fake.PolicyQuota.Writes = 0
			} else {
				w.fake.PolicyQuota.Lists = 0
			}
			waits := waiting(w)
			var out bytes.Buffer
			_, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: tt.mode, Commit: "abc1234", GitHubToken: "test-token", Bootstrap: tt.bootstrap}, &out)
			if err == nil {
				t.Fatalf("the run went through, and Google refused the call every time:\n%s", out.String())
			}
			if got := errors.Cause(err).Error(); got != tt.want {
				t.Errorf("the run's error is\n  %s\nwant\n  %s", got, tt.want)
			}
			half := 30 * time.Second
			if want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, half, half, 25 * time.Second}; !slices.Equal(*waits, want) {
				t.Errorf("the run waited %v, want %v: two minutes in all, and no longer", *waits, want)
			}
			report := stoppedReport(t, err)
			if tt.failed != "" && !slices.Contains(whats(report), tt.failed) {
				t.Errorf("the stopped run's report lacks the change it stopped at, %q: %v", tt.failed, whats(report))
			}
		})
	}
}

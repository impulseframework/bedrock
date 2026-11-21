package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/create/createtest"
	"github.com/impulseframework/bedrock/internal/org"
)

// TestOrgCreate: in a root organization's repository, create writes the record for a
// person to fill in when there is none; with it filled, fills in what the root
// organization supplies, chooses the boot project's suffix, lists the organization in
// placement.json, renders, and says what the merge creates and who grants what before;
// a created organization is left as it is; a hosting organization creates nothing.
func TestOrgCreate(t *testing.T) {
	t.Parallel()

	dir := rootRepo(t)
	if code, out := runOrg(t, "org", "render", "--dir", dir); code != 0 {
		t.Fatalf("org render: %d\n%s", code, out)
	}
	record := org.RecordPath(dir, "rgb")

	code, out := runOrg(t, "org", "create", "rgb", "--dir", dir)
	if code != 0 {
		t.Fatalf("org create: %d\n%s", code, out)
	}
	if !strings.Contains(out, "Wrote "+record+" for you to fill in: appsDomain") {
		t.Errorf("output:\n%s", out)
	}
	h, err := org.ReadPlacementUnchecked(record)
	if err != nil {
		t.Fatal(err)
	}
	if h.Prefix != "rgb" || h.Root == nil || h.Root.Organization != "imr" || h.GithubOrganization != "imp-example" || h.SourceRepo != "rgb-infrastructure" || h.Regions[0].Name != "us-central1" {
		t.Errorf("skeleton = %+v", h)
	}
	root, _ := orgPlacement(dir, "")
	if root.HostingOrganization("rgb").Created() || len(root.Pending()) != 1 {
		t.Errorf("the listing changed on the skeleton: %v", root.HostingOrganizations)
	}

	// The person fills in what is the organization's own.
	h.AppsDomain, h.Spanner.Config, h.MonthlyBudget = "apps.rgb.example", "nam10", 500
	h.Regions[1] = org.Region{Name: "us-west3", Code: "uw3"}
	h.TeamGroups = map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"}
	if err := h.Write(record); err != nil {
		t.Fatal(err)
	}
	code, out = runOrg(t, "org", "create", "rgb", "--dir", dir)
	if code != 0 {
		t.Fatalf("org create again: %d\n%s", code, out)
	}
	filled, err := org.ReadRecord(dir, "rgb")
	if err != nil {
		t.Fatalf("the record does not read as a hosting placement: %v", err)
	}
	root, _ = orgPlacement(dir, "")
	listed := root.HostingOrganization("rgb")
	if listed == nil || listed.BootProject != filled.Projects["boot"] || listed.Created() {
		t.Errorf("listed = %+v, record's boot project %q", listed, filled.Projects["boot"])
	}
	wantOut := []string{
		"Recorded rgb: boot project " + filled.Projects["boot"] + ", state bucket " + filled.StateBucket + ", repository imp-example/rgb-infrastructure, under the root organization imr.\n",
		"The merge of this pull request creates rgb, as the creating identity (imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com):",
		"(its APIs, its billing link to 012345-6789AB-CDEF01)",
		"  - its spend budget on billing account 012345-6789AB-CDEF01, 500 a month in the account's currency on\n    its folder (monthlyBudget), with alerts at 50, 90 and 100 percent to the account's\n    administrators and users, which this organization's apply keeps there as the creating\n    identity;",
		// No credentials in this test: the closing text says what was not checked and why,
		// never what the creating identity is taken to hold.
		"Not checked: whether the creating identity holds what the creation needs on the billing\naccount rgb is billed to, 012345-6789AB-CDEF01. Only an account that may impersonate the\ncreating identity can ask Google as it, as the apply trigger's run does, and this run could\nnot (no Google credentials in this test).\n",
		"  - billing.accounts.setIamPolicy, for granting its boot and org identities Billing Account User.\nBilling Account Administrator (roles/billing.admin) is the one predefined role that carries them all.\nThe bootstrap granted the creating identity that role on this account, this organization's\nown.\nThe merge's apply asks Google as the creating identity before it makes anything, and refuses\nwith what to grant where a permission is missing.",
		"makes its spend budget (500 a month in the account's currency, monthlyBudget)\non its folder",
	}
	for _, want := range wantOut {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Nothing is granted by hand", "Google answers"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output says %q of an account Google was not asked about:\n%s", unwanted, out)
		}
	}
	if code, out := runOrg(t, "org", "check", "--dir", dir); code != 0 {
		t.Errorf("org check after create: %d\n%s", code, out)
	}

	// Created: the record stays.
	editPlacement(t, dir, func(p *org.Placement) { p.HostingOrganization("rgb").Folder = "900000000001" })
	code, out = runOrg(t, "org", "create", "rgb", "--dir", dir)
	if code != 0 || !strings.Contains(out, "rgb is created already (folder 900000000001): the record is kept as it stands") {
		t.Errorf("org create of a created organization: %d\n%s", code, out)
	}

	// A hosting organization creates nothing.
	hosting := fixtureRepo(t, orgFixture)
	if _, err := execute(orgDeps(""), "", "org", "create", "rgb", "--dir", hosting); err == nil || !strings.Contains(err.Error(), "a root organization creates a hosting organization") {
		t.Errorf("org create in a hosting organization: %v", err)
	}
	if _, err := os.Stat(filepath.Join(hosting, org.RecordsDir)); !os.IsNotExist(err) {
		t.Errorf("a hosting organization got a record directory: %v", err)
	}
}

// TestOrgCreateAppPrefix: under a root organization that names the word its GitHub Apps'
// names start with, because its GitHub organization's login is too long for the names,
// the record carries the word from the first run, the second run fills it into a record
// that lacks it and passes, and a record that names another word is refused with both.
func TestOrgCreateAppPrefix(t *testing.T) {
	t.Parallel()

	dir := rootRepo(t)
	editPlacement(t, dir, func(p *org.Placement) {
		p.GithubOrganization, p.GithubAppPrefix = "imp-example-organization", "imx"
	})
	if code, out := runOrg(t, "org", "render", "--dir", dir); code != 0 {
		t.Fatalf("org render: %d\n%s", code, out)
	}
	record := org.RecordPath(dir, "rgb")
	if code, out := runOrg(t, "org", "create", "rgb", "--dir", dir); code != 0 {
		t.Fatalf("org create: %d\n%s", code, out)
	}
	h, err := org.ReadPlacementUnchecked(record)
	if err != nil {
		t.Fatal(err)
	}
	if h.GithubAppPrefix != "imx" {
		t.Errorf("the skeleton's githubAppPrefix = %q, want the root organization's imx", h.GithubAppPrefix)
	}

	// The person fills in what is the organization's own; the word is taken out, as in a
	// record written before bedrock filled it in.
	h.GithubAppPrefix = ""
	h.AppsDomain, h.Spanner.Config, h.MonthlyBudget = "apps.rgb.example", "nam10", 500
	h.Regions[1] = org.Region{Name: "us-west3", Code: "uw3"}
	h.TeamGroups = map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"}
	if err := h.Write(record); err != nil {
		t.Fatal(err)
	}
	if code, out := runOrg(t, "org", "create", "rgb", "--dir", dir); code != 0 {
		t.Fatalf("org create again: %d\n%s", code, out)
	}
	filled, err := org.ReadRecord(dir, "rgb")
	if err != nil {
		t.Fatalf("the record does not read as a hosting placement: %v", err)
	}
	if filled.GithubAppPrefix != "imx" || filled.AppName(org.InfrastructureApp) != "imx-infrastructure" {
		t.Errorf("githubAppPrefix = %q, the infrastructure app %s; want the root organization's imx", filled.GithubAppPrefix, filled.AppName(org.InfrastructureApp))
	}

	filled.GithubAppPrefix = "rgb"
	if err := filled.Write(record); err != nil {
		t.Fatal(err)
	}
	want := `githubAppPrefix is "rgb", and the root organization's GitHub Apps' names start with "imx"`
	if _, err := execute(orgDeps(""), "", "org", "create", "rgb", "--dir", dir); err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), record) {
		t.Errorf("org create of a record that names another word: %v, want %q", err, want)
	}
}

// TestOrgCreateBillingAccount: a hosting organization is billed to the billing account
// its record names. The first run writes the record with the root organization's account,
// and a person who names another has it kept: the second run records the organization on
// that account and says the boot project is linked to it. With no credentials to ask
// Google as the creating identity, its closing text says the account was not checked, and
// that an administrator of the account grants the creating identity its role there, which
// nothing of bedrock does.
func TestOrgCreateBillingAccount(t *testing.T) {
	t.Parallel()

	dir := rootRepo(t)
	if code, out := runOrg(t, "org", "render", "--dir", dir); code != 0 {
		t.Fatalf("org render: %d\n%s", code, out)
	}
	record := org.RecordPath(dir, "rgb")
	if code, out := runOrg(t, "org", "create", "rgb", "--dir", dir); code != 0 {
		t.Fatalf("org create: %d\n%s", code, out)
	}
	h, err := org.ReadPlacementUnchecked(record)
	if err != nil {
		t.Fatal(err)
	}
	if h.BillingAccount != rootBillingAccount {
		t.Errorf("the skeleton's billingAccount = %q, want the root organization's %s", h.BillingAccount, rootBillingAccount)
	}

	// The person fills in what is the organization's own, and names its own account.
	h.BillingAccount = ownBillingAccount
	h.AppsDomain, h.Spanner.Config, h.MonthlyBudget = "apps.rgb.example", "nam10", 500
	h.Regions[1] = org.Region{Name: "us-west3", Code: "uw3"}
	h.TeamGroups = map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"}
	if err := h.Write(record); err != nil {
		t.Fatal(err)
	}
	code, out := runOrg(t, "org", "create", "rgb", "--dir", dir)
	if code != 0 {
		t.Fatalf("org create of a record that names its own billing account: %d\n%s", code, out)
	}
	filled, err := org.ReadRecord(dir, "rgb")
	if err != nil {
		t.Fatalf("the record does not read as a hosting placement: %v", err)
	}
	if filled.BillingAccount != ownBillingAccount {
		t.Errorf("the record's billingAccount = %q, want its own kept, %s", filled.BillingAccount, ownBillingAccount)
	}
	wantOut := []string{
		"(its APIs, its billing link to ABCDEF-012345-6789AB)",
		"  - its spend budget on billing account ABCDEF-012345-6789AB, 500 a month in the account's currency on\n    its folder (monthlyBudget)",
		"Not checked: whether the creating identity holds what the creation needs on the billing\naccount rgb is billed to, ABCDEF-012345-6789AB. Only an account that may impersonate the\ncreating identity can ask Google as it, as the apply trigger's run does, and this run could\nnot (no Google credentials in this test).\n",
		"Billing Account Administrator (roles/billing.admin) is the one predefined role that carries them all.\nNothing of bedrock grants the creating identity anything on this account, which is not this\norganization's (billingAccount in organizations/rgb/placement.json): before the merge, an\nadministrator of the account grants that role on it to\nimr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com.\nThe merge's apply asks Google as the creating identity before it makes anything, and refuses\nwith what to grant where a permission is missing.",
	}
	for _, want := range wantOut {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"Nothing is granted by hand", "Google answers", "The bootstrap granted the creating identity that role", rootBillingAccount} {
		if strings.Contains(out, unwanted) {
			t.Errorf("output says %q of an organization billed to its own account, which Google was not asked about:\n%s", unwanted, out)
		}
	}
}

const (
	// rootBillingAccount is the root fixture's billing account, and ownBillingAccount one
	// a hosting organization's record names for itself.
	rootBillingAccount = "012345-6789AB-CDEF01"
	ownBillingAccount  = "ABCDEF-012345-6789AB"

	// creatingIdentity is the root fixture's creating identity.
	creatingIdentity = "imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com"
)

// TestOrgCreateBillingCheck: before create writes the record it asks Google, as the
// creating identity, what the identity holds on the billing account the organization is
// billed to, the root organization's like any other. An account that lacks a permission
// is refused in one sentence, with the record and the listing as the person left them and
// nothing printed. An account that grants everything goes on, and the closing text says
// what Google answered.
func TestOrgCreateBillingCheck(t *testing.T) {
	t.Parallel()

	asked := "BillingAccountPermissions(%s, billing.resourceAssociations.create, billing.budgets.list, billing.budgets.create, billing.budgets.update, billing.accounts.getIamPolicy, billing.accounts.setIamPolicy)"
	tests := []struct {
		name string
		// account is the billing account the record names, and lacks the permissions
		// Google answers the creating identity does not hold on it.
		account string
		lacks   []string
		wantErr string
		wantOut []string
	}{
		{
			name:    "an account of the record's own that lacks permissions is refused before anything is written",
			account: ownBillingAccount,
			lacks:   []string{"billing.accounts.setIamPolicy", "billing.resourceAssociations.create"},
			wantErr: "rgb is billed to the billing account ABCDEF-012345-6789AB (billingAccount in organizations/rgb/placement.json, the root organization's where the record names none), and Google answers that the creating identity imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com lacks billing.resourceAssociations.create (linking its projects to the account) and billing.accounts.setIamPolicy (granting its boot and org identities Billing Account User) on it: an administrator of that billing account grants the creating identity Billing Account Administrator (roles/billing.admin) on it, the one predefined role that carries every permission the creation needs there, then run bedrock org create rgb again; nothing was written",
		},
		{
			name:    "the root organization's account that lacks a permission is refused too",
			account: rootBillingAccount,
			lacks:   []string{"billing.budgets.create"},
			wantErr: "rgb is billed to the billing account 012345-6789AB-CDEF01 (billingAccount in organizations/rgb/placement.json, the root organization's where the record names none), and Google answers that the creating identity imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com lacks billing.budgets.create (making its spend budget) on it: an administrator of that billing account grants the creating identity Billing Account Administrator (roles/billing.admin) on it, the one predefined role that carries every permission the creation needs there, then run bedrock org create rgb again; nothing was written",
		},
		{
			name:    "an account of the record's own that grants everything goes on",
			account: ownBillingAccount,
			wantOut: []string{
				"Recorded rgb: boot project rgb-boot-gbl-core-",
				"Listed rgb in placement.json (hostingOrganizations)",
				"Nothing is granted by hand before the merge. Asked as the creating identity, Google answers\nthat it holds on the billing account rgb is billed to, ABCDEF-012345-6789AB, every permission\nthe creation needs there:\n  - billing.resourceAssociations.create, for linking its projects to the account;\n  - billing.budgets.list, for reading the account's budgets;\n  - billing.budgets.create, for making its spend budget;\n  - billing.budgets.update, for keeping its spend budget at the amount its record names;\n  - billing.accounts.getIamPolicy, for reading the account's IAM policy;\n  - billing.accounts.setIamPolicy, for granting its boot and org identities Billing Account User.\nWith them it links the new organization's projects to the account",
			},
		},
		{
			name:    "the root organization's account that grants everything goes on",
			account: rootBillingAccount,
			wantOut: []string{
				"Listed rgb in placement.json (hostingOrganizations)",
				"Nothing is granted by hand before the merge. Asked as the creating identity, Google answers\nthat it holds on the billing account rgb is billed to, 012345-6789AB-CDEF01, every permission\nthe creation needs there:\n",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := rootRepo(t)
			if code, out := runOrg(t, "org", "render", "--dir", dir); code != 0 {
				t.Fatalf("org render: %d\n%s", code, out)
			}
			record := org.RecordPath(dir, "rgb")
			if code, out := runOrg(t, "org", "create", "rgb", "--dir", dir); code != 0 {
				t.Fatalf("org create: %d\n%s", code, out)
			}
			h, err := org.ReadPlacementUnchecked(record)
			if err != nil {
				t.Fatal(err)
			}
			h.BillingAccount = tt.account
			h.AppsDomain, h.Spanner.Config, h.MonthlyBudget = "apps.rgb.example", "nam10", 500
			h.Regions[1] = org.Region{Name: "us-west3", Code: "uw3"}
			h.TeamGroups = map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"}
			if err := h.Write(record); err != nil {
				t.Fatal(err)
			}
			recordBefore, placementBefore := readFile(t, record), readFile(t, filepath.Join(dir, "placement.json"))

			google := createtest.New()
			google.BillingLacks[tt.account] = tt.lacks
			d := orgDeps("")
			d.org.googleAs = google.Open
			out, err := execute(d, "", "org", "create", "rgb", "--dir", dir)

			// Asked once, as the creating identity, about the record's account, and of
			// Google nothing else.
			if want := []string{creatingIdentity}; !reflect.DeepEqual(google.Identities, want) {
				t.Errorf("Google was opened as %v, want %v", google.Identities, want)
			}
			if want := []string{strings.Replace(asked, "%s", tt.account, 1)}; !reflect.DeepEqual(google.Calls, want) {
				t.Errorf("calls = %q, want %q", google.Calls, want)
			}
			if tt.wantErr != "" {
				if err == nil || errors.Cause(err).Error() != tt.wantErr {
					t.Fatalf("org create: %v\nwant: %s", err, tt.wantErr)
				}
				if strings.Contains(out, "Recorded") || strings.Contains(out, "The merge of this pull request creates") {
					t.Errorf("a refused record was reported as recorded:\n%s", out)
				}
				if got := readFile(t, record); !bytes.Equal(got, recordBefore) {
					t.Errorf("the refused record was written:\n%s", got)
				}
				if got := readFile(t, filepath.Join(dir, "placement.json")); !bytes.Equal(got, placementBefore) {
					t.Errorf("placement.json was written on a refused record:\n%s", got)
				}

				return
			}
			if err != nil {
				t.Fatalf("org create of a record whose account grants everything: %v\n%s", err, out)
			}
			if bytes.Equal(readFile(t, record), recordBefore) {
				t.Error("the record was not filled in")
			}
			root, _ := orgPlacement(dir, "")
			if root.HostingOrganization("rgb") == nil {
				t.Error("the organization was not listed in placement.json")
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			if strings.Contains(out, "Not checked") {
				t.Errorf("output says the account was not checked, and Google answered:\n%s", out)
			}
		})
	}
}

// TestOrgCreateBillingNotAnswered: a run that opens Google as the creating identity and
// is refused the answer (an account that may not impersonate the identity) refuses
// nothing and takes nothing for granted: the record is written, and the closing text says
// the account was not checked, with Google's own word for why.
func TestOrgCreateBillingNotAnswered(t *testing.T) {
	t.Parallel()

	dir := rootRepo(t)
	if code, out := runOrg(t, "org", "render", "--dir", dir); code != 0 {
		t.Fatalf("org render: %d\n%s", code, out)
	}
	record := org.RecordPath(dir, "rgb")
	if code, out := runOrg(t, "org", "create", "rgb", "--dir", dir); code != 0 {
		t.Fatalf("org create: %d\n%s", code, out)
	}
	h, err := org.ReadPlacementUnchecked(record)
	if err != nil {
		t.Fatal(err)
	}
	h.AppsDomain, h.Spanner.Config, h.MonthlyBudget = "apps.rgb.example", "nam10", 500
	h.Regions[1] = org.Region{Name: "us-west3", Code: "uw3"}
	h.TeamGroups = map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"}
	if err := h.Write(record); err != nil {
		t.Fatal(err)
	}

	google := createtest.New()
	google.Refuse["BillingAccountPermissions"] = errors.New("impersonation refused: iam.serviceAccounts.getAccessToken denied")
	d := orgDeps("")
	d.org.googleAs = google.Open
	out, err := execute(d, "", "org", "create", "rgb", "--dir", dir)
	if err != nil {
		t.Fatalf("org create when Google does not answer: %v\n%s", err, out)
	}
	if _, err := org.ReadRecord(dir, "rgb"); err != nil {
		t.Errorf("the record does not read as a hosting placement: %v", err)
	}
	want := "and this run could\nnot (impersonation refused: iam.serviceAccounts.getAccessToken denied).\n"
	if !strings.Contains(out, "Not checked: whether the creating identity holds what the creation needs") || !strings.Contains(out, want) {
		t.Errorf("output lacks the account not checked and %q:\n%s", want, out)
	}
	if strings.Contains(out, "Google answers") {
		t.Errorf("output says Google answered, and it did not:\n%s", out)
	}
}

// readFile reads the file whole.
func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return data
}

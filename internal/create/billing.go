// billing.go is the check made before anything of a hosting organization is made: whether
// the creating identity holds, on the billing account the organization is billed to, the
// permissions the creation's calls and the root organization's apply need there. The
// account is the one the record names (billingAccount), the root organization's where it
// names none, and the root organization's is asked about like any other: the bootstrap
// granted the identity Billing Account Administrator on it, and a grant can be taken
// off. On an account of the record's own nothing of bedrock grants the identity anything:
// that account's administrator does, and the refusal says so.

package create

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/org"
)

// BillingAdminRole is the one predefined role that carries every permission of
// BillingPermissions, Billing Account Administrator: billing.accounts.setIamPolicy is in
// no other (https://cloud.google.com/iam/docs/roles-permissions/billing), so no narrower
// set of predefined roles covers what the creation needs.
const (
	BillingAdminRole  = "roles/billing.admin"
	billingAdminTitle = "Billing Account Administrator"
)

// BillingPermission is one permission the creating identity needs on the billing account
// a hosting organization is billed to.
type BillingPermission struct {
	// Permission is the IAM permission, as Cloud Billing names it.
	Permission string
	// For is what the creating identity does with it for the organization, as a refusal
	// says it.
	For string
}

// BillingPermissions are what the creating identity needs on the billing account a
// hosting organization is billed to, each named by Google's documentation of the call
// that needs it:
//
//   - billing.resourceAssociations.create links a project to the account: the boot
//     project's link (Google.LinkBilling, projects.updateBillingInfo) and the links of
//     the six projects 1-org's first apply makes as the creating identity
//     (https://cloud.google.com/billing/docs/how-to/modify-project).
//   - billing.budgets.list and billing.budgets.create read the account's budgets and make
//     the organization's (Google.Budgets, Google.CreateBudget;
//     https://cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/list
//     and .../billingAccounts.budgets/create), and billing.budgets.update brings one
//     changed by hand back, which the root organization's apply does as the same
//     identity (https://cloud.google.com/billing/docs/how-to/budgets).
//   - billing.accounts.getIamPolicy and billing.accounts.setIamPolicy grant the boot and
//     org identities Billing Account User (Google.GrantBillingAccount;
//     https://cloud.google.com/billing/docs/reference/rest/v1/billingAccounts/getIamPolicy
//     and .../billingAccounts/setIamPolicy).
var BillingPermissions = []BillingPermission{
	{Permission: "billing.resourceAssociations.create", For: "linking its projects to the account"},
	{Permission: "billing.budgets.list", For: "reading the account's budgets"},
	{Permission: "billing.budgets.create", For: "making its spend budget"},
	{Permission: "billing.budgets.update", For: "keeping its spend budget at the amount its record names"},
	{Permission: "billing.accounts.getIamPolicy", For: "reading the account's IAM policy"},
	{Permission: "billing.accounts.setIamPolicy", For: "granting its boot and org identities Billing Account User"},
}

// BillingAccess is what Google answered about the creating identity on a hosting
// organization's billing account.
type BillingAccess struct {
	// Prefix is the hosting organization, Account the billing account it is billed to,
	// and Identity the creating identity, which was asked about.
	Prefix, Account, Identity string
	// Missing are the permissions of BillingPermissions the identity does not hold on the
	// account, in that order: none when it holds them all.
	Missing []BillingPermission
}

// CheckBillingAccess asks Google which of BillingPermissions the creating identity holds
// on the billing account the hosting organization is billed to. Cloud Billing's
// billingAccounts.testIamPermissions answers for its caller alone
// (https://cloud.google.com/billing/docs/reference/rest/v1/billingAccounts/testIamPermissions),
// so g is Google opened as the creating identity. An error says the question was not
// answered, and nothing about what the identity holds.
func CheckBillingAccess(ctx context.Context, g Google, root, h *org.Placement) (*BillingAccess, error) {
	asked := make([]string, 0, len(BillingPermissions))
	for _, p := range BillingPermissions {
		asked = append(asked, p.Permission)
	}
	held, err := g.BillingAccountPermissions(ctx, h.BillingAccount, asked)
	if err != nil {
		return nil, errors.Wrapf(err, "what the creating identity %s holds on billing account %s", root.CreatingIdentity(), h.BillingAccount)
	}
	access := &BillingAccess{Prefix: h.Prefix, Account: h.BillingAccount, Identity: root.CreatingIdentity()}
	for _, p := range BillingPermissions {
		if !slices.Contains(held, p.Permission) {
			access.Missing = append(access.Missing, p)
		}
	}

	return access, nil
}

// AskBillingAccess opens Google as the creating identity, asks what CheckBillingAccess
// asks, and closes it: bedrock org create's check, made before it writes the record. An
// error says Google could not be asked as the creating identity, which only an account
// that may impersonate it can.
func AskBillingAccess(ctx context.Context, open GoogleAsFunc, root, h *org.Placement) (*BillingAccess, error) {
	g, err := open(ctx, root.CreatingIdentity())
	if err != nil {
		return nil, err
	}
	defer g.Close()

	return CheckBillingAccess(ctx, g, root, h)
}

// Refusal is the one sentence a missing permission is refused with: the file and the
// field, the account, the creating identity, each permission it lacks with what the
// creation does with it, and what to do, which ends with again (the command to run once
// the grant stands, and what this run left alone).
func (a *BillingAccess) Refusal(again string) error {
	missing := make([]string, 0, len(a.Missing))
	for _, p := range a.Missing {
		missing = append(missing, p.Permission+" ("+p.For+")")
	}

	return errors.Newf("%s is billed to the billing account %s (billingAccount in %s, the root organization's where the record names none), and Google answers that the creating identity %s lacks %s on it: an administrator of that billing account grants the creating identity %s (%s) on it, the one predefined role that carries every permission the creation needs there, then %s",
		a.Prefix, a.Account, filepath.ToSlash(filepath.Join(org.RecordsDir, a.Prefix, org.PlacementFile)), a.Identity, list(missing), billingAdminTitle, BillingAdminRole, again)
}

// BillingPermissionNames are the permissions of BillingPermissions as one phrase, for the
// lines that say what was asked.
func BillingPermissionNames() string {
	names := make([]string, 0, len(BillingPermissions))
	for _, p := range BillingPermissions {
		names = append(names, p.Permission)
	}

	return list(names)
}

// list joins the items as a sentence names them: a, b and c.
func list(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}

	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

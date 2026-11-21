// orgcreate.go is bedrock org create: the record of a hosting organization the root
// organization creates by pull request (README.md, "Creating a hosting organization").

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-playground/errors/v5"
	"github.com/spf13/cobra"

	"github.com/impulseframework/bedrock/internal/create"
	"github.com/impulseframework/bedrock/internal/org"
)

const createCommand = "create"

func newOrgCreate(d deps) *cobra.Command {
	var dir, placement string
	cmd := &cobra.Command{
		Use:   createCommand + " <prefix>",
		Short: "Record a hosting organization for the root organization's apply trigger to create",
		Long: `create, in a root organization's repository, records a hosting organization the apply
trigger creates on the merge: the organization's folder at the organization node and the
foundation under it (the terraform folder, the boot project, the boot identity with its roles
on the folder, the state bucket), the first applies of its 0-bootstrap and 1-org with the
projects recorded in its placement, its infrastructure repository with the rendered
foundation as its first push, which its own apply trigger runs on, and the hand-off, a pull
request here that records the folder. Nobody applies by hand (README.md, "Creating a hosting
organization").

The record is the organization's own placement, at organizations/<prefix>/placement.json:
the hosting shape, with what is the organization's to decide written by a person (appsDomain,
spanner.config, two regions, teamGroups, contactDomains, entitlementDurations, labels,
vanityDomains) and what the root organization supplies filled in by this command (the
Google Cloud organization and the billing account, the GitHub organization, its Bot
Account, default branch and apps, the Cloud Build authorization, the break-glass account, the
bedrock pin, and root, which names this organization). The new organization is billed to
the billing account its record names (billingAccount), and to this organization's where
the record names none. The boot project's suffix is chosen here too, so its id is known
before anything is made. Without the file, create writes one to fill in and stops. With
it, create fills it, lists the organization in placement.json (hostingOrganizations: its
prefix and its boot project; the folder is recorded by the hand-off), renders, and says
what the merge creates.

For an organization billed to this organization's billing account nobody grants anything
by hand: the bootstrap granted the creating identity Folder Creator at the organization
node and Billing Account Administrator on that account, and with them it links the new
organization's projects to the account, grants the new organization's identities Billing
Account User itself, and makes the new organization's spend budget, the amount its record
names (monthlyBudget), on its folder. On a billing account of the record's own, an
administrator of that account grants the creating identity Billing Account Administrator
(roles/billing.admin) before the merge: the bootstrap made the identity an administrator
of this organization's account alone.

Before it writes the record, create asks Google, as the creating identity, whether the
identity holds on the organization's billing account the permissions the creation needs
there, this organization's account like any other, and refuses a record whose account
lacks one, saying what to grant; its closing text says what Google answered. Only an
account that may impersonate the creating identity can ask as it, which the apply
trigger's runner does and a person at a terminal as a rule does not: such a run says the
permissions were not checked, and why, and goes on. The merge's apply asks the same before
it makes anything, and refuses the same way.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.orgCreate(cmd.Context(), dir, placement, args[0], cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&dir, "dir", ".", "the root organization's repository root")
	cmd.Flags().StringVar(&placement, "placement", "", "placement file (default: placement.json in the repository root)")

	return cmd
}

// orgCreate records the organization: the skeleton when the record is absent, else the
// filled record, the listing, the render and the preview. Before the filled record is
// written, it asks what the creating identity holds on the organization's billing
// account (billingStanding), and a record whose account lacks a permission is refused
// with nothing written.
func (d deps) orgCreate(ctx context.Context, dir, placement, prefix string, out io.Writer) error {
	root, err := orgPlacement(dir, placement)
	if err != nil {
		return err
	}
	if !root.IsRoot() {
		return errors.New("this placement is a hosting organization's: a root organization creates a hosting organization (kind root in placement.json)")
	}
	if err := d.orgPinned(root, orgPlacementPath(dir, placement)); err != nil {
		return err
	}
	path := org.RecordPath(dir, prefix)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return writeSkeleton(root, prefix, path, out)
	} else if err != nil {
		return errors.Wrapf(err, "os.Stat(): %s", path)
	}
	h, err := readRecord(path, prefix)
	if err != nil {
		return err
	}
	if err := org.Fill(root, h); err != nil {
		return errors.Wrapf(err, "%s", path)
	}
	var billing billingStanding
	if listed := root.HostingOrganization(prefix); listed == nil || !listed.Created() {
		billing = d.billingStanding(ctx, root, h)
		if err := billing.refusal(); err != nil {
			return err
		}
	}
	if err := h.Write(path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Recorded %s: boot project %s, state bucket %s, repository %s/%s, under the root organization %s.\n", prefix, h.Projects["boot"], h.StateBucket, h.GithubOrganization, h.SourceRepo, root.Prefix)
	changed := root.Record(h)
	if changed {
		if err := root.Write(orgPlacementPath(dir, placement)); err != nil {
			return err
		}
		fmt.Fprintf(out, "Listed %s in placement.json (hostingOrganizations); its folder is recorded by the creation's hand-off.\n", prefix)
	}
	if record := root.HostingOrganization(prefix); record != nil && record.Created() {
		fmt.Fprintf(out, "%s is created already (folder %s): the record is kept as it stands, and nothing is created again.\n", prefix, record.Folder)

		return nil
	}
	files, err := org.Render(root)
	if err != nil {
		return err
	}
	if _, err := org.Write(root, files, dir); err != nil {
		return err
	}
	fmt.Fprint(out, creationSteps(root, h, billing))

	return nil
}

// billingStanding is what bedrock org create learned about the creating identity on the
// billing account the organization is billed to: Google's answer (access), or why Google
// could not be asked (unasked).
type billingStanding struct {
	access  *create.BillingAccess
	unasked error
}

// billingStanding asks Google, as the creating identity, what the identity holds on the
// billing account the organization is billed to (create.AskBillingAccess), the root
// organization's like any other. A run that cannot ask as the creating identity (no
// credentials, or an account that may not impersonate it, as a person at a terminal as a
// rule may not) is answered nothing, and keeps why: the closing text says the permissions
// were not checked and why, and the merge's apply asks again before it makes anything.
func (d deps) billingStanding(ctx context.Context, root, h *org.Placement) billingStanding {
	if d.org == nil || d.org.googleAs == nil {
		return billingStanding{unasked: errors.New("no Google client as an identity is wired")}
	}
	access, err := create.AskBillingAccess(ctx, d.org.googleAs, root, h)
	if err != nil {
		return billingStanding{unasked: err}
	}

	return billingStanding{access: access}
}

// refusal refuses a record on whose billing account Google answers the creating identity
// lacks a permission, in the sentence the creation refuses with; an account that grants
// everything, and one Google was not asked about, are refused nothing.
func (b billingStanding) refusal() error {
	if b.access == nil || len(b.access.Missing) == 0 {
		return nil
	}

	return b.access.Refusal("run bedrock org create " + b.access.Prefix + " again; nothing was written")
}

// readRecord reads the record as a person wrote it: a hosting placement that
// may lack what the root organization fills in, so it is read without validation and
// validated once filled.
func readRecord(path, prefix string) (*org.Placement, error) {
	h, err := org.ReadPlacementUnchecked(path)
	if err != nil {
		return nil, err
	}
	if h.Prefix == "" {
		h.Prefix = prefix
	}
	if h.Prefix != prefix {
		return nil, errors.Newf("%s has prefix %q, and the record under %s/%s is that organization's", path, h.Prefix, org.RecordsDir, prefix)
	}

	return h, nil
}

// writeSkeleton writes the record for a person to fill in, with what the root
// organization supplies already in it and the organization's own values empty.
func writeSkeleton(root *org.Placement, prefix, path string, out io.Writer) error {
	h := &org.Placement{
		Prefix:  prefix,
		Regions: []org.Region{{Name: root.Primary().Name, Code: root.Primary().Code}, {}},
		TeamGroups: map[string]string{
			org.Environments[0]: "", org.Environments[1]: "", org.Environments[2]: "",
		},
		ContactDomains: []string{"@" + root.OrganizationDomain},
		Labels:         root.Labels,
	}
	h.OrganizationDomain, h.OrganizationID, h.BillingAccount = root.OrganizationDomain, root.OrganizationID, root.BillingAccount
	h.GithubOrganization, h.GithubBotAccount, h.GithubDefaultBranch = root.GithubOrganization, root.GithubBotAccount, root.GithubDefaultBranch
	h.GithubInfrastructureAppID, h.GithubDeployerAppID, h.GithubReleaseAppID = root.GithubInfrastructureAppID, root.GithubDeployerAppID, root.GithubReleaseAppID
	h.GithubAppPrefix, h.GithubReleaseAppSlug = root.GithubAppPrefix, root.ReleaseAppSlug()
	h.BreakGlassAccount = root.BreakGlassAccount
	h.SourceRepo = prefix + "-infrastructure"
	h.Root = &org.RootPlace{Organization: root.Prefix, Project: root.RootProject()}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return errors.Wrapf(err, "os.MkdirAll(): %s", filepath.Dir(path))
	}
	if err := h.Write(path); err != nil {
		return err
	}
	fmt.Fprintf(out, `Wrote %s for you to fill in: appsDomain (the domain the applications' names hang under),
spanner.config (the shared instance's configuration, nam10 for example), the second region
(regions, the primary first; the primary is the root organization's, since the Cloud Build
token is read in its region), teamGroups (one Workspace group per environment),
contactDomains, monthlyBudget (its spend budget, whole units of the billing account's
currency a month, which the creation makes on its folder), and entitlementDurations,
vanityDomains and labels as wanted. Then run
bedrock org create %s again: it fills in what the root organization supplies, chooses the
boot project's suffix and lists the organization in placement.json.
`, path, prefix)

	return nil
}

// creationSteps says what the merge creates, and what the creating identity holds on the
// organization's billing account as org create found it: Google's answer, or that Google
// could not be asked, never what the bootstrap is taken to have left.
func creationSteps(root, h *org.Placement, billing billingStanding) string {
	var b strings.Builder
	fmt.Fprintf(&b, `
The merge of this pull request creates %s, as the creating identity (%s):
  - folder %s at the organization node, the terraform folder under it, the boot project %s
    (its APIs, its billing link to %s), the boot identity %s-boot-gbl-tofu with its roles on
    the folder, the break-glass account's Folder Admin on the folder, and the state bucket
    %s;
  - its spend budget on billing account %s, %d a month in the account's currency on
    its folder (monthlyBudget), with alerts at 50, 90 and 100 percent to the account's
    administrators and users, which this organization's apply keeps there as the creating
    identity;
  - the first applies of its 0-bootstrap and 1-org, against that bucket: its connection from
    the Cloud Build authorization this organization recorded, its repository link and its
    two triggers; its folders, its six projects with their identities, recorded in its
    placement (projects, projectNumbers);
  - the public zone of its apps domain %s in its net project, made as the creating
    identity, which this organization's apply verified as an owner of the domain, and
    recorded in its placement (appsZone), which its 2-net adopts;
  - the repository %s/%s, private, with the rendered foundation as its first push, which
    its apply trigger runs on, every layer as its own identity;
  - the creating identity's own grants off everything it made, a marker in this
    organization's state bucket, and the hand-off pull request here, which records the
    folder (placement.json, hostingOrganizations) and the placement as created, and hands
    the apps domain's label onward to its zone (domains), from which the apply writes the
    NS record set.
`, h.Prefix, root.CreatingIdentity(), h.Prefix, h.Projects["boot"], h.BillingAccount, h.Prefix, h.StateBucket, h.BillingAccount, h.MonthlyBudget, h.AppsDomain, h.GithubOrganization, h.SourceRepo)
	b.WriteString(billingText(root, h, billing))
	b.WriteString(`After the merge, for the new organization: its consent screen and role groups follow as its
README says.
`)

	return b.String()
}

// billingText is the closing text's word on the billing account. Where Google answered,
// it says what Google answered the creating identity holds there. Where Google could not
// be asked, it says so and why, names the permissions the creation needs and the role
// that carries them, and says who grants it: the bootstrap did on the root
// organization's account, and an administrator of the account does on another, to the
// creating identity by its email.
func billingText(root, h *org.Placement, billing billingStanding) string {
	var b strings.Builder
	if billing.access != nil {
		fmt.Fprintf(&b, `Nothing is granted by hand before the merge. Asked as the creating identity, Google answers
that it holds on the billing account %s is billed to, %s, every permission
the creation needs there:
%sWith them it links the new organization's projects to the account, grants its boot and org
identities Billing Account User itself and makes its spend budget (%d a month in the account's
currency, monthlyBudget) on its folder. The bootstrap granted it Folder Creator at the
organization node.
`, h.Prefix, h.BillingAccount, billingPermissionLines(), h.MonthlyBudget)

		return b.String()
	}
	fmt.Fprintf(&b, `Not checked: whether the creating identity holds what the creation needs on the billing
account %s is billed to, %s. Only an account that may impersonate the
creating identity can ask Google as it, as the apply trigger's run does, and this run could
not (%v).
The creation needs there:
%sBilling Account Administrator (%s) is the one predefined role that carries them all.
`, h.Prefix, h.BillingAccount, errors.Cause(billing.unasked), billingPermissionLines(), create.BillingAdminRole)
	if h.BillingAccount == root.BillingAccount {
		b.WriteString(`The bootstrap granted the creating identity that role on this account, this organization's
own.
`)
	} else {
		fmt.Fprintf(&b, `Nothing of bedrock grants the creating identity anything on this account, which is not this
organization's (billingAccount in %s): before the merge, an
administrator of the account grants that role on it to
%s.
`, filepath.ToSlash(filepath.Join(org.RecordsDir, h.Prefix, org.PlacementFile)), root.CreatingIdentity())
	}
	fmt.Fprintf(&b, `The merge's apply asks Google as the creating identity before it makes anything, and refuses
with what to grant where a permission is missing. With the permissions it links the new
organization's projects to the account, grants its boot and org identities Billing Account
User itself and makes its spend budget (%d a month in the account's currency, monthlyBudget)
on its folder. The bootstrap granted it Folder Creator at the organization node.
`, h.MonthlyBudget)

	return b.String()
}

// billingPermissionLines lists the permissions the creation needs on the billing account,
// one a line with what each is for.
func billingPermissionLines() string {
	var b strings.Builder
	for i, p := range create.BillingPermissions {
		end := ";"
		if i == len(create.BillingPermissions)-1 {
			end = "."
		}
		fmt.Fprintf(&b, "  - %s, for %s%s\n", p.Permission, p.For, end)
	}

	return b.String()
}

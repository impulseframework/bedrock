// policies.go is the organization-policy baseline the root organization sets on each
// hosting organization's folders. Google grants Organization Policy Administrator at the
// organization node and nowhere lower, and nothing of a hosting organization holds a role
// there (the root README, "Several organizations"), so the hosting organization's 1-org
// makes the folders and the root organization's apply, whose applier holds the role (and
// whose planner holds Organization Policy Viewer, both granted by the bootstrap), sets the
// policies on them: in the creation, right after 1-org's first apply and before the push
// the organization's own apply trigger applies every layer on (create.Request.Organized),
// and on every apply after, from the organization's record
// (organizations/<prefix>/placement.json), which the creation filled with the folders'
// ids and each environment project's public-invoker tag (folders, publicInvokerTags). One
// constraint is set earlier and higher, on the organization's own folder right after the
// seed (OrganizationFolderBaseline), so that 1-org's first apply already runs under it.

package root

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/impulseframework/bedrock/internal/org"
)

// The two constraints set beside the baseline list, each with a shape of its own.
const (
	memberDomainsConstraint  = "iam.allowedPolicyMemberDomains"
	contactDomainsConstraint = "essentialcontacts.managed.allowedContactDomains"
)

// policyFolders are the folders 1-org makes under the organization's folder, by key, in
// the order the policies are set: the terraform folder is left out, so the boot project
// and the layer identities in it are never locked out by a constraint they depend on.
var policyFolders = []string{"shared", "tst", "stg", "prd"}

// organizationFolderKey names the hosting organization's own folder in a policy's report
// line; the folder has no key in the record, which lists the four beneath it.
const organizationFolderKey = "organization"

// OrganizationFolderBaseline is the one constraint set on the hosting organization's own
// folder, which every folder beneath inherits, the terraform folder included: set by the
// creation right after the seed and before 1-org's first apply, so no project of the
// organization is ever made with a default network, and the project factory's deletion
// of it, the step Google's race refused in the lab, has nothing to do. Only a constraint
// that can lock nothing out goes here; the rest of the baseline stays off the terraform
// folder (policyFolders).
var OrganizationFolderBaseline = []BaselinePolicy{
	{Constraint: "compute.skipDefaultNetworkCreation", Rule: enforced()},
}

// OrganizationFolderPolicies are the policies set on the hosting organization's own
// folder, by its numeric id.
func OrganizationFolderPolicies(folderID string) []FolderPolicy {
	policies := make([]FolderPolicy, 0, len(OrganizationFolderBaseline))
	for _, b := range OrganizationFolderBaseline {
		policies = append(policies, FolderPolicy{FolderKey: organizationFolderKey, FolderID: folderID, Constraint: b.Constraint, Policy: Policy{Name: "folders/" + folderID + "/policies/" + b.Constraint, Rules: []PolicyRule{b.Rule}}})
	}

	return policies
}

// BaselinePolicy is one constraint of the baseline and the rule set on every folder.
type BaselinePolicy struct {
	Constraint string
	Rule       PolicyRule
}

func enforced() PolicyRule {
	t := true

	return PolicyRule{Enforce: &t}
}

// Baseline is the constraints set on every folder of every hosting organization, trimmed
// to the ones that cost nothing operationally: each closes a door nothing here uses
// (service account keys, VM serial consoles, external IPs, HMAC keys) or pins a decision
// already made (one load balancer type, Cloud Run behind it, GitHub as the only Cloud
// Build source).
var Baseline = []BaselinePolicy{
	// Service account credentials: keys are the one long-lived credential a service
	// account can have, nothing here needs one, and every runner authenticates as its
	// service account directly.
	{Constraint: "iam.managed.disableServiceAccountKeyCreation", Rule: enforced()},
	{Constraint: "iam.managed.disableServiceAccountKeyUpload", Rule: enforced()},
	{Constraint: "iam.serviceAccountKeyExpiryHours", Rule: PolicyRule{AllowedValues: []string{"2160h"}}},
	{Constraint: "iam.allowServiceAccountCredentialLifetimeExtension", Rule: PolicyRule{DenyAll: true}},
	{Constraint: "iam.automaticIamGrantsForDefaultServiceAccounts", Rule: enforced()},
	{Constraint: "iam.disableAuditLoggingExemption", Rule: enforced()},
	// Cloud Storage: every bucket is uniform-access, private, and reached over TLS with
	// an IAM identity rather than an HMAC key.
	{Constraint: "storage.publicAccessPrevention", Rule: enforced()},
	{Constraint: "storage.secureHttpTransport", Rule: enforced()},
	{Constraint: "storage.uniformBucketLevelAccess", Rule: enforced()},
	{Constraint: "storage.restrictAuthTypes", Rule: PolicyRule{DeniedValues: []string{"in:ALL_HMAC_SIGNED_REQUESTS"}}},
	// Compute: no VMs are planned, so these cost nothing; they keep any VM that does
	// appear off the internet and off the serial console.
	{Constraint: "compute.disableGlobalSerialPortAccess", Rule: enforced()},
	{Constraint: "compute.disableSerialPortAccess", Rule: enforced()},
	{Constraint: "compute.requireOsLogin", Rule: enforced()},
	{Constraint: "compute.requireShieldedVm", Rule: enforced()},
	{Constraint: "compute.skipDefaultNetworkCreation", Rule: enforced()},
	{Constraint: "compute.vmCanIpForward", Rule: PolicyRule{DenyAll: true}},
	{Constraint: "compute.vmExternalIpAccess", Rule: PolicyRule{DenyAll: true}},
	// Ingress: only the global external managed HTTP(S) load balancer, so every request
	// path runs through the same TLS and Cloud Armor configuration, and Cloud Run is
	// reachable only through it or from inside the VPC.
	{Constraint: "compute.restrictLoadBalancerCreationForTypes", Rule: PolicyRule{AllowedValues: []string{"GLOBAL_EXTERNAL_MANAGED_HTTP_HTTPS"}}},
	{Constraint: "run.allowedIngress", Rule: PolicyRule{AllowedValues: []string{"internal-and-cloud-load-balancing"}}},
	{Constraint: "gcp.restrictTLSVersion", Rule: PolicyRule{DeniedValues: []string{"TLS_VERSION_1", "TLS_VERSION_1_1"}}},
	// Cloud Build: builds run as an explicit service account (the legacy default one is
	// never created), and source connections are GitHub only.
	{Constraint: "cloudbuild.disableCreateDefaultServiceAccount", Rule: enforced()},
	{Constraint: "cloudbuild.allowedIntegrations", Rule: PolicyRule{AllowedValues: []string{"github.com"}}},
}

// FolderPolicy is one policy to set: the folder's key and id, the constraint, and the
// policy itself.
type FolderPolicy struct {
	FolderKey  string
	FolderID   string
	Constraint string
	Policy     Policy
}

// What names the policy for a line of the report.
func (f FolderPolicy) What(prefix string) string {
	return fmt.Sprintf("%s on folder %s (%s) of %s", f.Constraint, f.FolderKey, f.FolderID, prefix)
}

// DesiredPolicies are the policies of one hosting organization, from its record: the
// baseline on each of its four folders; on each environment folder, domain-restricted
// sharing to the Google Cloud organization's own customer with the public-invoker
// exception, a Cloud Run service tagged with that environment project's public-invoker
// tag, which may name allUsers; and on each folder the allowed contact domains, the
// record's. A folder or a tag the record lacks is a finding, and that folder's policies
// wait for it.
func DesiredPolicies(h *org.Placement) (policies []FolderPolicy, findings []string) {
	for _, key := range policyFolders {
		id := h.Folders[key]
		if id == "" {
			findings = append(findings, fmt.Sprintf("the record of %s names no folder %s (folders), so no policy is set on it: the creation records the folders from 1-org's first apply; an organization created before that recorded them has them written by hand (gcloud resource-manager folders list --folder <organization folder>)", h.Prefix, key))

			continue
		}
		name := func(constraint string) string { return "folders/" + id + "/policies/" + constraint }
		for _, b := range Baseline {
			policies = append(policies, FolderPolicy{FolderKey: key, FolderID: id, Constraint: b.Constraint, Policy: Policy{Name: name(b.Constraint), Rules: []PolicyRule{b.Rule}}})
		}
		if slices.Contains(org.Environments, key) {
			tag, ok := h.PublicInvokerTags[key]
			if !ok {
				findings = append(findings, fmt.Sprintf("the record of %s names no public-invoker tag for %s (publicInvokerTags), so domain-restricted sharing is not set on folder %s: the creation records the tags from 1-org's first apply (its outputs public_invoker_tag_key and public_invoker_tag_value)", h.Prefix, key, key))
			} else {
				policies = append(policies, FolderPolicy{FolderKey: key, FolderID: id, Constraint: memberDomainsConstraint, Policy: Policy{Name: name(memberDomainsConstraint), Rules: []PolicyRule{
					{Condition: &Condition{Title: "public-invoker", Description: "A Cloud Run service the application layer tagged for the load balancer.", Expression: fmt.Sprintf("resource.matchTagId('%s', '%s')", tag.Key, tag.Value)}, AllowAll: true},
					{AllowedValues: []string{h.DirectoryCustomerID}},
				}}})
			}
		}
		parameters, _ := json.Marshal(map[string]any{"allowedDomains": h.ContactDomains})
		policies = append(policies, FolderPolicy{FolderKey: key, FolderID: id, Constraint: contactDomainsConstraint, Policy: Policy{Name: name(contactDomainsConstraint), Rules: []PolicyRule{{Enforce: enforced().Enforce, Parameters: string(parameters)}}}})
	}

	return policies, findings
}

// RulesEqual reports whether two policies' rules say the same: rule for rule, the same
// shape, the same values (in any order), the same condition, and the same parameters
// (as JSON, in any key order).
func RulesEqual(got, want []PolicyRule) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !ruleEqual(got[i], want[i]) {
			return false
		}
	}

	return true
}

func ruleEqual(a, b PolicyRule) bool {
	if (a.Enforce == nil) != (b.Enforce == nil) || (a.Enforce != nil && *a.Enforce != *b.Enforce) {
		return false
	}
	if a.DenyAll != b.DenyAll || a.AllowAll != b.AllowAll || !sameSet(a.AllowedValues, b.AllowedValues) || !sameSet(a.DeniedValues, b.DeniedValues) {
		return false
	}
	if (a.Condition == nil) != (b.Condition == nil) || (a.Condition != nil && *a.Condition != *b.Condition) {
		return false
	}

	return sameJSON(a.Parameters, b.Parameters)
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)

	return slices.Equal(a, b)
}

func sameJSON(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		return a == b
	}

	return reflect.DeepEqual(x, y)
}

// orgPolicies is the section: for each hosting organization created (recorded with its
// folder, or found created by this run), the constraint on its own folder and each policy
// of the baseline on the four beneath that it lacks or holds otherwise is set. An
// organization this run's creation made is passed over: the creation set both moments
// ago, each at its step (creations), and a second pass would list its findings twice.
func (e *engine) orgPolicies(ctx context.Context) error {
	for _, record := range e.p.HostingOrganizations {
		if e.policiesSet[record.Prefix] {
			continue
		}
		h, ok, err := e.hostingRecord(record)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		if folder := e.organizationFolder(record); folder != "" {
			if err := e.setPolicies(ctx, h.Prefix, OrganizationFolderPolicies(folder)); err != nil {
				return err
			}
		}
		if err := e.baseline(ctx, h); err != nil {
			return err
		}
	}

	return nil
}

// baseline sets, on the four folders the hosting organization's placement records, each
// policy of the baseline that its folder lacks or holds otherwise, and lists what the
// placement lacks as a finding: the section's work for an organization that stands, and
// the creation's for the one it makes, once 1-org's first apply has made the folders and
// before the push (create.Request.Organized).
func (e *engine) baseline(ctx context.Context, h *org.Placement) error {
	policies, findings := DesiredPolicies(h)
	for _, f := range findings {
		e.finding(f)
	}

	return e.setPolicies(ctx, h.Prefix, policies)
}

// organizationFolder is the hosting organization's own folder: the one this run's
// creation made, else the one its hand-off recorded; empty for an organization not
// created yet.
func (e *engine) organizationFolder(record org.HostingOrganization) string {
	if folder, ok := e.createdFolders[record.Prefix]; ok {
		return folder
	}

	return record.Folder
}

// setPolicies sets each policy of the hosting organization that its folder lacks or holds
// otherwise, each a change of the policies section, in the order given. What stands is
// read a folder at a time, with one list of the folder's policies made when the first
// policy wanted on it is reached (standingPolicies): a policy the list lacks is made, one
// it holds with other rules is replaced, and one it holds that is not wanted here is
// left as it is.
func (e *engine) setPolicies(ctx context.Context, prefix string, policies []FolderPolicy) error {
	standing := map[string]map[string]*Policy{}
	for _, want := range policies {
		onFolder, listed := standing[want.FolderID]
		if !listed {
			var err error
			if onFolder, err = e.standingPolicies(ctx, prefix, want); err != nil {
				return err
			}
			standing[want.FolderID] = onFolder
		}
		got, exists := onFolder[want.Constraint]
		switch {
		case !exists:
			if err := e.change(ctx, sectionPolicies, "sets "+want.What(prefix), func(ctx context.Context) error {
				return e.withinQuota(ctx, "to set "+want.What(prefix), func() error {
					return e.policies.CreatePolicy(ctx, "folders/"+want.FolderID, &want.Policy)
				})
			}); err != nil {
				return err
			}
		case !RulesEqual(got.Rules, want.Policy.Rules):
			if err := e.change(ctx, sectionPolicies, "brings "+want.What(prefix)+" to the baseline", func(ctx context.Context) error {
				return e.withinQuota(ctx, "to bring "+want.What(prefix)+" to the baseline", func() error {
					return e.policies.UpdatePolicy(ctx, &want.Policy)
				})
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

// policyOf is what a policy's resource name puts before its constraint: the name is
// PARENT/policies/CONSTRAINT, the parent a folder here (folders/F).
const policyOf = "/policies/"

// standingPolicies are the policies that stand on the folder of the policy f, by
// constraint, read with one list of the folder: a hosting organization's 96 policies are
// five lists, one for each folder, where a read of each by its name was 96 calls against
// the quota Google counts for the root project by the minute, and the root's apply over
// two hosting organizations was refused for it.
func (e *engine) standingPolicies(ctx context.Context, prefix string, f FolderPolicy) (map[string]*Policy, error) {
	var listed []*Policy
	if err := e.withinQuota(ctx, fmt.Sprintf("to list the organization policies on folder %s (%s) of %s", f.FolderKey, f.FolderID, prefix), func() error {
		var err error
		listed, err = e.policies.ListPolicies(ctx, "folders/"+f.FolderID)

		return err
	}); err != nil {
		return nil, err
	}
	standing := make(map[string]*Policy, len(listed))
	for _, p := range listed {
		if _, constraint, named := strings.Cut(p.Name, policyOf); named {
			standing[constraint] = p
		}
	}

	return standing, nil
}

// Google counts the Org Policy calls billed to a project by the minute, each kind on its
// own: for the root project, where the root organization's are billed (open), the lab
// read 300 reads of a policy by its name, 300 lists and 100 writes a minute, the limits
// Google gives by default (2026-11-21). A call past the minute's limit is refused as
// ResourceExhausted, and what a run spent in one minute is free again in the next.
//
// A call refused so is made again, the tries starting policyQuotaFirstWait apart, each
// wait twice the one before it and none longer than policyQuotaLongestWait, half the
// minute, so that every minute is tried at least twice; and for no longer than
// policyQuotaTries in all, two of the quota's minutes. The first minute is all a run
// needs when nothing else spends the quota, however much the run itself spent before the
// refusal: a creation's 95 policies are inside the 100 writes of one minute, and a
// baseline grown past the hundred would wait out one minute for each hundred; a pass
// over a hosting organization is 5 lists, so the check and the apply of one build are 20
// over two hosting organizations and 50 over five, of the minute's 300. The second minute
// is for a quota something else was spending when the first turned: another build of the
// root organization (a pull request's plan beside a merge's apply), a person's check.
// Past that the wait is no longer the quota's minute, and the run stops with a sentence
// instead of sitting in its build step: of the steps that run the engine the check's
// time limit is the shortest, ten minutes (org.TriggerSteps), which a call that waits
// its two minutes and stops is well inside, and an apply that stops gives its lock back
// (ApplyRun), where one killed at its step's limit would leave it for a person to delete.
const (
	policyQuotaWindow      = time.Minute
	policyQuotaTries       = 2 * policyQuotaWindow
	policyQuotaFirstWait   = 5 * time.Second
	policyQuotaLongestWait = policyQuotaWindow / 2
)

// withinQuota makes one Org Policy call (call), named for a person by what it does (to
// list ..., to set ...), and waits out the quota for it: a call Google refuses for the
// quota is made again with a backoff, for no longer than policyQuotaTries in all, and a
// refusal that outlasts it ends the run with a sentence that names the call, the project
// whose quota it is, what Google answered and the way on. Any other answer is the
// call's at once.
func (e *engine) withinQuota(ctx context.Context, what string, call func() error) error {
	wait, waited := policyQuotaFirstWait, time.Duration(0)
	for {
		err := call()
		if err == nil || !quotaSpent(err) {
			return err
		}
		if waited >= policyQuotaTries {
			return errors.Newf("Google refuses %s: the quota of the root project %s is spent (%s). The organization policy calls of this run are billed to that project, Google counts them by the minute, and it still refused after %d minutes of tries: another build or person billed to the project may be spending the quota. %s",
				what, e.d.Project(), status.Convert(errors.Cause(err)).Message(), int(policyQuotaTries.Minutes()), e.again())
		}
		wait = min(wait, policyQuotaTries-waited)
		fmt.Fprintf(e.out, "  (Google refuses %s: the quota of the root project %s, which it counts by the minute, is spent; trying again in %s)\n", what, e.d.Project(), wait)
		if werr := e.sleep(ctx, wait); werr != nil {
			return werr
		}
		waited += wait
		wait = min(2*wait, policyQuotaLongestWait)
	}
}

// quotaSpent reports whether the error is an API's refusal of a call for a quota.
func quotaSpent(err error) bool {
	return status.Code(errors.Cause(err)) == codes.ResourceExhausted
}

// again says how a person gets past a refusal that a later run does not meet: the run
// again, as its mode has it run, and what of this run is kept.
func (e *engine) again() string {
	switch {
	case e.req.Bootstrap:
		return "What this run made stands: run bedrock org bootstrap again, and it goes on from here"
	case e.req.Mode == Apply:
		return "What this run made stands: run the apply trigger " + e.p.ApplyTriggerName() + " again, and it goes on from here"
	case e.req.Mode == Plan:
		return "Nothing was changed: run the plan trigger " + e.p.PlanTriggerName() + " again"
	default:
		return "Nothing was changed: run the check again (bedrock org check, or the trigger whose build ran it)"
	}
}

// hostingRecord is the hosting organization's placement the policies are set from: the
// one this run's creation recorded, else the record in the repository of an organization
// recorded with its folder; an organization not created yet has none (ok false).
func (e *engine) hostingRecord(record org.HostingOrganization) (*org.Placement, bool, error) {
	if h, ok := e.created[record.Prefix]; ok {
		return h, true, nil
	}
	if !record.Created() {
		return nil, false, nil
	}
	h, err := org.ReadRecord(e.req.Dir, record.Prefix)
	if err != nil {
		return nil, false, errors.Wrapf(err, "the record of %s, from which the root organization sets its organization policies", record.Prefix)
	}

	return h, true, nil
}

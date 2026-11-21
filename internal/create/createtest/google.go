// Package createtest is a stand-in for the creation's Google seam: what the creation
// asks Google Cloud, answered from memory and recorded, so a test can read what a
// creation made and granted, and start from an organization half made. It refuses what
// Google would refuse the identity the seam was opened as: a call that needs a grant the
// identity does not hold, or holds no longer (iam.go).
package createtest

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/create"
	"github.com/impulseframework/bedrock/internal/org"
)

// FakeGoogle is the stand-in. Folders are by "parent|name"; Projects, Accounts and
// Buckets by name; Grants are the bindings made, "resource role member", in order;
// Calls every call, in order; Identities the identities the seam was opened as.
//
// A seam opened as an identity (Open) is checked: each call on a folder or a project, or
// on what a project holds, needs a role of the identity's that carries the call's
// permission, on the resource or above it, and is refused with Google's PermissionDenied
// without one. The calls on what stands outside the organization's folder (the billing
// account and its budgets, the shared secrets) are not checked, nor is a seam never
// opened, which is a person's.
type FakeGoogle struct {
	mu       sync.Mutex
	Folders  map[string]string
	Projects map[string]bool
	// ProjectFolders is each project's folder (numeric id), as CreateProject was told.
	ProjectFolders map[string]string
	Numbers        map[string]string
	Accounts       map[string]bool
	Buckets        map[string]bool
	// BucketProjects is each bucket's project, as CreateBucket was told; a bucket in none
	// is checked against nothing.
	BucketProjects map[string]string
	// Policies are the roles held, by resource (organizations/N, folders/N, projects/ID),
	// then by role: the members. Parents is each folder's and project's parent, by which a
	// grant on a folder reaches what is under it.
	Policies map[string]map[string][]string
	Parents  map[string]string
	// Denied are the calls refused for a grant the caller lacks, in order.
	Denied []string
	// Zones are the public zones by project|name, each with its name servers.
	Zones   map[string][]string
	Enabled map[string][]string
	Billing map[string]string
	// SpendBudgets are the budgets by billing account.
	SpendBudgets map[string][]org.Budget
	Grants       []string
	Revoked      []string
	Calls        []string
	Identities   []string
	// Agents records each service agent asked for, by "project/service".
	Agents []string
	// Refuse names a call (its method name) to answer with the error, for a creation
	// that stops halfway.
	Refuse map[string]error
	// Stop, when set, is asked before each call, with the call as Calls records it, and
	// its error stops that call: a test's way of stopping a creation at one call of several
	// by the same name. It runs inside the stand-in and asks it nothing.
	Stop func(call string) error
	// caller is the identity the seam was last opened as, as an IAM member; empty for a
	// seam never opened.
	caller string
	next   int
}

// New makes an empty stand-in: no folder, no project.
func New() *FakeGoogle {
	return &FakeGoogle{
		Folders: map[string]string{}, Projects: map[string]bool{}, Numbers: map[string]string{}, Accounts: map[string]bool{},
		Buckets: map[string]bool{}, Zones: map[string][]string{}, Enabled: map[string][]string{}, Billing: map[string]string{}, SpendBudgets: map[string][]org.Budget{}, Refuse: map[string]error{},
		ProjectFolders: map[string]string{}, BucketProjects: map[string]string{}, Policies: map[string]map[string][]string{}, Parents: map[string]string{},
	}
}

// Budgets answers the billing account's budgets from memory.
func (g *FakeGoogle) Budgets(_ context.Context, billingAccount string) ([]org.Budget, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("Budgets", billingAccount); err != nil {
		return nil, err
	}

	return slices.Clone(g.SpendBudgets[billingAccount]), nil
}

// CreateBudget remembers the budget on the billing account, named after its place.
func (g *FakeGoogle) CreateBudget(_ context.Context, billingAccount string, b *org.Budget) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("CreateBudget", billingAccount, b.DisplayName); err != nil {
		return err
	}
	b.Name = fmt.Sprintf("billingAccounts/%s/budgets/%d", billingAccount, len(g.SpendBudgets[billingAccount])+1)
	b.CurrencyCode = "USD"
	g.SpendBudgets[billingAccount] = append(g.SpendBudgets[billingAccount], *b)

	return nil
}

// ZoneNameServers are the name servers the stand-in assigns every zone it makes.
var ZoneNameServers = []string{"ns-cloud-z1.googledomains.com.", "ns-cloud-z2.googledomains.com.", "ns-cloud-z3.googledomains.com.", "ns-cloud-z4.googledomains.com."}

// PublicZone answers the zone's name servers when it exists.
func (g *FakeGoogle) PublicZone(_ context.Context, project, name string) (nameServers []string, exists bool, err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("PublicZone", project, name); err != nil {
		return nil, false, err
	}
	if err := g.need("PublicZone", ZonesGet, "projects/"+project); err != nil {
		return nil, false, err
	}
	servers, ok := g.Zones[project+"|"+name]

	return servers, ok, nil
}

// CreatePublicZone records the zone and assigns it the stand-in's name servers.
func (g *FakeGoogle) CreatePublicZone(_ context.Context, project, name, dnsName, _ string, _ map[string]string) ([]string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("CreatePublicZone", project, name, dnsName); err != nil {
		return nil, err
	}
	if err := g.need("CreatePublicZone", ZonesCreate, "projects/"+project); err != nil {
		return nil, err
	}
	g.Zones[project+"|"+name] = ZoneNameServers

	return ZoneNameServers, nil
}

// Open is the seam's opener: it records the identity and answers the stand-in, which
// checks every call from then on as that identity's.
func (g *FakeGoogle) Open(_ context.Context, identity string) (create.Google, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.Identities = append(g.Identities, identity)
	if identity != "" {
		g.caller = Member(identity)
	}

	return g, nil
}

func (g *FakeGoogle) call(name string, args ...string) error {
	record := name + "(" + strings.Join(args, ", ") + ")"
	g.Calls = append(g.Calls, record)
	if err, ok := g.Refuse[name]; ok {
		return err
	}
	if g.Stop != nil {
		return g.Stop(record)
	}

	return nil
}

// FindFolder answers the folder's id from memory, empty when none was made or when the
// caller may not read the one that was: Google's search answers only the folders its
// caller may read.
func (g *FakeGoogle) FindFolder(_ context.Context, parent, displayName string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("FindFolder", parent, displayName); err != nil {
		return "", err
	}
	id := g.Folders[parent+"|"+displayName]
	if id != "" && !g.may(FoldersGet, "folders/"+id) {
		return "", nil
	}

	return id, nil
}

// CreateFolder makes the folder with the next numeric id, its maker the caller.
func (g *FakeGoogle) CreateFolder(_ context.Context, parent, displayName string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("CreateFolder", parent, displayName); err != nil {
		return "", err
	}
	if err := g.need("CreateFolder", FoldersCreate, parent); err != nil {
		return "", err
	}
	g.next++
	id := strconv.Itoa(900000000000 + g.next)
	g.Folders[parent+"|"+displayName] = id
	g.stand("folders/"+id, parent, g.caller)

	return id, nil
}

// AdministersFolder answers whether the caller may set the folder's policy. Google
// answers the question for any caller, so it is never refused.
func (g *FakeGoogle) AdministersFolder(_ context.Context, folder string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("AdministersFolder", folder); err != nil {
		return false, err
	}

	return g.may(FoldersSetPolicy, "folders/"+folder), nil
}

// GrantFolder records the binding.
func (g *FakeGoogle) GrantFolder(_ context.Context, folder, role, member string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("GrantFolder", folder, role, member); err != nil {
		return err
	}
	if err := g.grant("GrantFolder", "folders/"+folder, FoldersGetPolicy, FoldersSetPolicy, role, member); err != nil {
		return err
	}
	g.Grants = append(g.Grants, "folders/"+folder+" "+role+" "+member)

	return nil
}

// RevokeFolder records the removal.
func (g *FakeGoogle) RevokeFolder(_ context.Context, folder, member string, roles []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("RevokeFolder", folder, member, strings.Join(roles, "+")); err != nil {
		return err
	}
	if err := g.revoke("RevokeFolder", "folders/"+folder, FoldersGetPolicy, FoldersSetPolicy, member, roles); err != nil {
		return err
	}
	g.Revoked = append(g.Revoked, "folders/"+folder+" "+member+" "+strings.Join(roles, "+"))

	return nil
}

// grant is a grant as the seam makes one: the policy is read, and written when the
// member is not in the role yet, each of which the caller must be allowed.
func (g *FakeGoogle) grant(call, resource, read, write, role, member string) error {
	if err := g.need(call, read, resource); err != nil {
		return err
	}
	if g.bound(resource, member, []string{role}) {
		return nil
	}
	if err := g.need(call, write, resource); err != nil {
		return err
	}
	g.bind(resource, role, member)

	return nil
}

// revoke is a removal as the seam makes one: the policy is read, and written when the
// member is in one of the roles, each of which the caller must be allowed. A removal with
// nothing to take off still reads.
func (g *FakeGoogle) revoke(call, resource, read, write, member string, roles []string) error {
	if err := g.need(call, read, resource); err != nil {
		return err
	}
	if !g.bound(resource, member, roles) {
		return nil
	}
	if err := g.need(call, write, resource); err != nil {
		return err
	}
	g.unbind(resource, member, roles)

	return nil
}

// ProjectExists answers from memory; a project the caller may not read is none to it, as
// the seam takes Google's refusal of one.
func (g *FakeGoogle) ProjectExists(_ context.Context, id string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("ProjectExists", id); err != nil {
		return false, err
	}

	return g.Projects[id] && g.may(ProjectsGet, "projects/"+id), nil
}

// FindProject is the one active project under the folder whose id starts with idPrefix, or
// empty; several are an error, as Google's are told apart no better.
func (g *FakeGoogle) FindProject(_ context.Context, folder, idPrefix string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("FindProject", folder, idPrefix); err != nil {
		return "", err
	}
	var found []string
	for id, in := range g.ProjectFolders {
		if in == folder && strings.HasPrefix(id, idPrefix) && g.Projects[id] {
			found = append(found, id)
		}
	}
	sort.Strings(found)
	switch len(found) {
	case 0:
		return "", nil
	case 1:
		return found[0], nil
	default:
		return "", errors.Newf("folder %s holds %d projects named %s<suffix>: %s", folder, len(found), idPrefix, strings.Join(found, ", "))
	}
}

// CreateProject makes the project, numbered in order of creation, its maker the caller.
func (g *FakeGoogle) CreateProject(_ context.Context, id, displayName, folder string, labels map[string]string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	labeled := make([]string, 0, len(keys))
	for _, k := range keys {
		labeled = append(labeled, k+"="+labels[k])
	}
	if err := g.call("CreateProject", id, displayName, "folders/"+folder, strings.Join(labeled, ",")); err != nil {
		return err
	}
	if err := g.need("CreateProject", ProjectsCreate, "folders/"+folder); err != nil {
		return err
	}
	g.Projects[id] = true
	g.Numbers[id] = fmt.Sprintf("1000000%05d", len(g.Projects))
	g.ProjectFolders[id] = folder
	g.stand("projects/"+id, "folders/"+folder, g.caller)

	return nil
}

// ProjectNumber answers the project's number, or an error for a project never made.
func (g *FakeGoogle) ProjectNumber(_ context.Context, id string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("ProjectNumber", id); err != nil {
		return "", err
	}
	if err := g.need("ProjectNumber", ProjectsGet, "projects/"+id); err != nil {
		return "", err
	}
	number, ok := g.Numbers[id]
	if !ok {
		return "", errors.Newf("no project %s", id)
	}

	return number, nil
}

// GrantProject records the binding.
func (g *FakeGoogle) GrantProject(_ context.Context, id, role, member string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("GrantProject", id, role, member); err != nil {
		return err
	}
	if err := g.grant("GrantProject", "projects/"+id, ProjectsGetPolicy, ProjectsSetPolicy, role, member); err != nil {
		return err
	}
	g.Grants = append(g.Grants, "projects/"+id+" "+role+" "+member)

	return nil
}

// RevokeProject records the removal.
func (g *FakeGoogle) RevokeProject(_ context.Context, id, member string, roles []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("RevokeProject", id, member, strings.Join(roles, "+")); err != nil {
		return err
	}
	if err := g.revoke("RevokeProject", "projects/"+id, ProjectsGetPolicy, ProjectsSetPolicy, member, roles); err != nil {
		return err
	}
	g.Revoked = append(g.Revoked, "projects/"+id+" "+member+" "+strings.Join(roles, "+"))

	return nil
}

// GrantBillingAccount records the grant on the billing account.
func (g *FakeGoogle) GrantBillingAccount(_ context.Context, billingAccount, role, member string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("GrantBillingAccount", billingAccount, role, member); err != nil {
		return err
	}
	g.Grants = append(g.Grants, "billingAccounts/"+billingAccount+" "+role+" "+member)

	return nil
}

// LinkBilling records the link.
func (g *FakeGoogle) LinkBilling(_ context.Context, id, billingAccount string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("LinkBilling", id, billingAccount); err != nil {
		return err
	}
	if err := g.need("LinkBilling", ProjectsBilling, "projects/"+id); err != nil {
		return err
	}
	g.Billing[id] = billingAccount

	return nil
}

// ServiceAgent records the agent asked for and answers the email Google would, from
// the project's number.
func (g *FakeGoogle) ServiceAgent(_ context.Context, project, service string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("ServiceAgent", project, service); err != nil {
		return "", err
	}
	number, ok := g.Numbers[project]
	if !ok {
		return "", errors.Newf("no project %s", project)
	}
	g.Agents = append(g.Agents, project+"/"+service)

	return AgentEmail(number, service), nil
}

// AgentEmail is the email of a project's service agent for the service, as Google names
// them: service-<number>@gcp-sa-<service>.iam.gserviceaccount.com.
func AgentEmail(number, service string) string {
	return "service-" + number + "@gcp-sa-" + strings.TrimSuffix(service, ".googleapis.com") + ".iam.gserviceaccount.com"
}

// EnableServices records the APIs.
func (g *FakeGoogle) EnableServices(_ context.Context, id string, services []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("EnableServices", id, strconv.Itoa(len(services))+" services"); err != nil {
		return err
	}
	if err := g.need("EnableServices", ServicesEnable, "projects/"+id); err != nil {
		return err
	}
	g.Enabled[id] = services

	return nil
}

// ServiceAccountExists answers from memory.
func (g *FakeGoogle) ServiceAccountExists(_ context.Context, email string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("ServiceAccountExists", email); err != nil {
		return false, err
	}
	if err := g.need("ServiceAccountExists", AccountsGet, "projects/"+accountProject(email)); err != nil {
		return false, err
	}

	return g.Accounts[email], nil
}

// CreateServiceAccount makes the account.
func (g *FakeGoogle) CreateServiceAccount(_ context.Context, project, accountID, displayName, _ string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("CreateServiceAccount", project, accountID, displayName); err != nil {
		return err
	}
	if err := g.need("CreateServiceAccount", AccountsCreate, "projects/"+project); err != nil {
		return err
	}
	g.Accounts[accountID+"@"+project+".iam.gserviceaccount.com"] = true

	return nil
}

// BucketExists answers from memory.
func (g *FakeGoogle) BucketExists(_ context.Context, name string) (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("BucketExists", name); err != nil {
		return false, err
	}
	if project, placed := g.BucketProjects[name]; placed {
		if err := g.need("BucketExists", BucketsGet, "projects/"+project); err != nil {
			return false, err
		}
	}

	return g.Buckets[name], nil
}

// CreateBucket makes the bucket.
func (g *FakeGoogle) CreateBucket(_ context.Context, project, name, location string, _ map[string]string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("CreateBucket", project, name, location); err != nil {
		return err
	}
	if err := g.need("CreateBucket", BucketsCreate, "projects/"+project); err != nil {
		return err
	}
	g.Buckets[name] = true
	g.BucketProjects[name] = project

	return nil
}

// GrantSecret records the binding.
func (g *FakeGoogle) GrantSecret(_ context.Context, secret, role, member string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.call("GrantSecret", secret, role, member); err != nil {
		return err
	}
	g.Grants = append(g.Grants, secret+" "+role+" "+member)

	return nil
}

// Close releases nothing.
func (*FakeGoogle) Close() error {
	return nil
}

// Made lists what the stand-in holds: folders, projects, accounts and buckets, sorted.
func (g *FakeGoogle) Made() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	made := make([]string, 0, len(g.Folders)+len(g.Projects)+len(g.Accounts)+len(g.Buckets))
	for key, id := range g.Folders {
		made = append(made, "folder "+key+"="+id)
	}
	for id := range g.Projects {
		made = append(made, "project "+id+" ("+g.Numbers[id]+", billing "+g.Billing[id]+", "+strconv.Itoa(len(g.Enabled[id]))+" APIs)")
	}
	for email := range g.Accounts {
		made = append(made, "account "+email)
	}
	for key := range g.Zones {
		made = append(made, "zone "+key)
	}
	for name := range g.Buckets {
		made = append(made, "bucket "+name)
	}
	for account, budgets := range g.SpendBudgets {
		for _, b := range budgets {
			made = append(made, fmt.Sprintf("budget %s|%s=%d %s", account, b.DisplayName, b.Units, strings.Join(b.Ancestors, ",")))
		}
	}
	sort.Strings(made)

	return made
}

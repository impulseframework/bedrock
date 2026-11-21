// iam.go is the stand-in's IAM: who holds which role on what, what each role lets its
// holder do, and the refusal of a call whose caller holds no role that lets it, as Google
// refuses one. Without it a creation run again after its identity's grants were taken off
// would pass every call, and a test of that run would prove nothing.

package createtest

import (
	"slices"
	"sort"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The permissions the creation's calls and its first applies need, in Google's names.
// Those of a project's own services (its billing link, its APIs, its identities, its
// buckets and its zones) come with Owner alone among the roles the stand-in knows.
const (
	FoldersGet        = "resourcemanager.folders.get"
	FoldersCreate     = "resourcemanager.folders.create"
	FoldersGetPolicy  = "resourcemanager.folders.getIamPolicy"
	FoldersSetPolicy  = "resourcemanager.folders.setIamPolicy"
	ProjectsGet       = "resourcemanager.projects.get"
	ProjectsCreate    = "resourcemanager.projects.create"
	ProjectsGetPolicy = "resourcemanager.projects.getIamPolicy"
	ProjectsSetPolicy = "resourcemanager.projects.setIamPolicy"
	ProjectsBilling   = "resourcemanager.projects.createBillingAssignment"
	TagKeysCreate     = "resourcemanager.tagKeys.create"
	ServicesEnable    = "serviceusage.services.enable"
	ServicesList      = "serviceusage.services.list"
	AccountsGet       = "iam.serviceAccounts.get"
	AccountsCreate    = "iam.serviceAccounts.create"
	BucketsGet        = "storage.buckets.get"
	BucketsCreate     = "storage.buckets.create"
	ObjectsGet        = "storage.objects.get"
	ZonesGet          = "dns.managedZones.get"
	ZonesCreate       = "dns.managedZones.create"
)

// The roles the stand-in knows, in Google's names: the two Google gives a folder's maker,
// the one it gives a project's, the two the creation grants its identity on the
// organization's folder, and the one the bootstrap grants it at the organization node.
const (
	FolderAdmin    = "roles/resourcemanager.folderAdmin"
	FolderEditor   = "roles/resourcemanager.folderEditor"
	FolderCreator  = "roles/resourcemanager.folderCreator"
	ProjectCreator = "roles/resourcemanager.projectCreator"
	TagAdmin       = "roles/resourcemanager.tagAdmin"
	Owner          = "roles/owner"

	// everything is Owner's: every permission on the project it is held on.
	everything = "*"
)

// rolePermissions is what each role lets its holder do, of the permissions above, as
// Google's reference of predefined roles lists them. A role the stand-in does not know
// lets its holder do nothing here: the boot identity's roles are never the caller's.
var rolePermissions = map[string][]string{
	Owner:          {everything},
	FolderAdmin:    {FoldersGet, FoldersCreate, FoldersGetPolicy, FoldersSetPolicy, ProjectsGet, ProjectsGetPolicy, ProjectsSetPolicy},
	FolderEditor:   {FoldersGet, FoldersGetPolicy, ProjectsGet},
	FolderCreator:  {FoldersGet, FoldersCreate, ProjectsGet},
	ProjectCreator: {ProjectsCreate},
	TagAdmin:       {TagKeysCreate},
}

// Member is the service account's email as an IAM member.
func Member(email string) string {
	return "serviceAccount:" + email
}

// Bind gives the member the role on the resource (organizations/N, folders/N or
// projects/ID), as a grant that stands before the test starts: the creating identity's
// Folder Creator at the organization node, which the bootstrap grants.
func (g *FakeGoogle) Bind(resource, role, member string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.bind(resource, role, member)
}

// Stand puts the resource (folders/N or projects/ID) under its parent as the member made
// it, with what Google gives a maker: Folder Admin and Folder Editor on a folder, Owner
// on a project. It is how a test starts from an organization half made, and how a
// stand-in of another program (tofu) records what its apply made.
func (g *FakeGoogle) Stand(resource, parent, maker string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.stand(resource, parent, maker)
}

// Check answers Google's refusal when the member holds no role, on the resource or above
// it, that carries the permission, and nil when it does: the question a stand-in of
// another program asks for the calls that program makes as the member.
func (g *FakeGoogle) Check(member, permission, resource string) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	return g.check(member, "Check", permission, resource)
}

// Holds lists what the member holds, each as "resource role", sorted.
func (g *FakeGoogle) Holds(member string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	var held []string
	for resource, roles := range g.Policies {
		for role, members := range roles {
			if slices.Contains(members, member) {
				held = append(held, resource+" "+role)
			}
		}
	}
	sort.Strings(held)

	return held
}

func (g *FakeGoogle) bind(resource, role, member string) {
	if g.Policies[resource] == nil {
		g.Policies[resource] = map[string][]string{}
	}
	if !slices.Contains(g.Policies[resource][role], member) {
		g.Policies[resource][role] = append(g.Policies[resource][role], member)
	}
}

func (g *FakeGoogle) stand(resource, parent, maker string) {
	g.Parents[resource] = parent
	if maker == "" {
		return
	}
	if strings.HasPrefix(resource, "folders/") {
		g.bind(resource, FolderAdmin, maker)
		g.bind(resource, FolderEditor, maker)

		return
	}
	g.bind(resource, Owner, maker)
}

// bound reports whether the member is in one of the roles on the resource itself.
func (g *FakeGoogle) bound(resource, member string, roles []string) bool {
	for _, role := range roles {
		if slices.Contains(g.Policies[resource][role], member) {
			return true
		}
	}

	return false
}

// unbind takes the member out of the roles on the resource.
func (g *FakeGoogle) unbind(resource, member string, roles []string) {
	for _, role := range roles {
		members := g.Policies[resource][role]
		if i := slices.Index(members, member); i >= 0 {
			g.Policies[resource][role] = slices.Delete(members, i, i+1)
		}
	}
}

// allowed reports whether the member holds a role that carries the permission on the
// resource or on anything above it: a grant on a folder reaches everything under it.
func (g *FakeGoogle) allowed(member, permission, resource string) bool {
	for at := resource; at != ""; at = g.Parents[at] {
		for role, members := range g.Policies[at] {
			if !slices.Contains(members, member) {
				continue
			}
			if carried := rolePermissions[role]; slices.Contains(carried, permission) || slices.Contains(carried, everything) {
				return true
			}
		}
	}

	return false
}

// check is Google's refusal of the call when the member may not make it, recorded in
// Denied; nil when it may.
func (g *FakeGoogle) check(member, call, permission, resource string) error {
	if g.allowed(member, permission, resource) {
		return nil
	}
	g.Denied = append(g.Denied, call+": "+member+" lacks "+permission+" on "+resource)

	return status.Errorf(codes.PermissionDenied, "Permission '%s' denied on resource '%s' (or it may not exist).", permission, resource)
}

// need checks the call for the identity the seam was opened as. A seam opened as no
// identity is a person's, whose grants the stand-in does not hold: nothing is refused it.
func (g *FakeGoogle) need(call, permission, resource string) error {
	if g.caller == "" {
		return nil
	}

	return g.check(g.caller, call, permission, resource)
}

// may is need without the refusal: whether the identity the seam was opened as may.
func (g *FakeGoogle) may(permission, resource string) bool {
	return g.caller == "" || g.allowed(g.caller, permission, resource)
}

// accountProject is the project of a service account, from its email.
func accountProject(email string) string {
	_, domain, _ := strings.Cut(email, "@")

	return strings.TrimSuffix(domain, ".iam.gserviceaccount.com")
}

package createtest_test

import (
	"slices"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/impulseframework/bedrock/internal/create/createtest"
)

const (
	node    = "organizations/123456789012"
	creator = "imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com"
	boot    = "rgb-boot-gbl-core-e5f6"
	account = "rgb-boot-gbl-tofu@rgb-boot-gbl-core-e5f6.iam.gserviceaccount.com"
	billing = "012345-6789AB-CDEF01"
)

// refused fails the test unless the error is Google's refusal of a call for a grant its
// caller lacks.
func refused(t *testing.T, call string, err error) {
	t.Helper()
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("%s: error = %v, want Google's PermissionDenied", call, err)
	}
}

// passed fails the test when the call was refused.
func passed(t *testing.T, call string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: error = %v, want it to pass", call, err)
	}
}

// TestRefuses: the stand-in refuses the identity the seam was opened as what Google would
// refuse it, a call that needs a grant the identity does not hold on the resource or above
// it, and passes the call once the grant stands and until it is taken off: through a
// folder's making, a project's, the project's Owner taken off, and everything on the
// folder taken off.
func TestRefuses(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	member := createtest.Member(creator)
	fake := createtest.New()
	fake.Bind(node, createtest.FolderCreator, member)
	g, err := fake.Open(ctx, creator)
	if err != nil {
		t.Fatal(err)
	}

	// Folder Creator at the organization node makes the folder, and Google gives its maker
	// Folder Admin, which creates no project: that takes Project Creator.
	folder, err := g.CreateFolder(ctx, node, "rgb")
	passed(t, "CreateFolder with Folder Creator at the node", err)
	refused(t, "CreateProject without Project Creator", g.CreateProject(ctx, boot, "rgb-boot-gbl-core", folder, nil))
	passed(t, "GrantFolder as the folder's maker", g.GrantFolder(ctx, folder, createtest.ProjectCreator, member))
	passed(t, "CreateProject with Project Creator", g.CreateProject(ctx, boot, "rgb-boot-gbl-core", folder, nil))
	passed(t, "LinkBilling as the project's maker", g.LinkBilling(ctx, boot, billing))
	asked := []string{createtest.FoldersGetPolicy, createtest.FoldersSetPolicy, createtest.ProjectsCreate, createtest.TagKeysCreate}
	if held, err := g.Held(ctx, "folders/"+folder, asked); err != nil || !slices.Equal(held, asked[:3]) {
		t.Errorf("Held on the folder as its maker with Project Creator = %v, %v, want %v: no role it holds carries the fourth", held, err, asked[:3])
	}
	policy := []string{createtest.ProjectsGetPolicy, createtest.ProjectsSetPolicy}
	if held, err := g.Held(ctx, "projects/"+boot, policy); err != nil || !slices.Equal(held, policy) {
		t.Errorf("Held on the project as its maker = %v, %v, want %v", held, err, policy)
	}

	// Owner off the project: what the project holds is refused, and the project itself and
	// its policy still read, by Folder Admin on the folder above it.
	passed(t, "RevokeProject of its own Owner", g.RevokeProject(ctx, boot, member, []string{createtest.Owner}))
	refused(t, "LinkBilling without Owner", g.LinkBilling(ctx, boot, billing))
	refused(t, "EnableServices without Owner", g.EnableServices(ctx, boot, []string{"iam.googleapis.com"}))
	_, err = g.ServiceAccountExists(ctx, account)
	refused(t, "ServiceAccountExists without Owner", err)
	refused(t, "CreateBucket without Owner", g.CreateBucket(ctx, boot, "rgb-boot-gbl-state-e5f6", "us-central1", nil))
	_, _, err = g.PublicZone(ctx, boot, "rgb-net-gbl-dns-apps")
	refused(t, "PublicZone without Owner", err)
	_, err = g.ProjectNumber(ctx, boot)
	passed(t, "ProjectNumber by Folder Admin above", err)
	passed(t, "RevokeProject with nothing to take off, a read", g.RevokeProject(ctx, boot, member, []string{createtest.Owner}))

	// Everything off the folder: its policy and the project's are refused, even to a
	// removal with nothing to take off, and the folder and the project still read, by
	// Folder Creator at the organization node.
	passed(t, "RevokeFolder of its own roles", g.RevokeFolder(ctx, folder, member, []string{createtest.FolderAdmin, createtest.FolderEditor, createtest.ProjectCreator}))
	refused(t, "GrantFolder holding nothing on the folder", g.GrantFolder(ctx, folder, createtest.ProjectCreator, member))
	refused(t, "RevokeFolder holding nothing on the folder", g.RevokeFolder(ctx, folder, member, []string{createtest.FolderAdmin}))
	refused(t, "RevokeProject holding nothing on the folder", g.RevokeProject(ctx, boot, member, []string{createtest.Owner}))
	refused(t, "CreateProject holding nothing on the folder", g.CreateProject(ctx, "rgb-shr-gbl-core-e5f6", "rgb-shr-gbl-core", folder, nil))
	if held, err := g.Held(ctx, "folders/"+folder, asked); err != nil || len(held) != 0 {
		t.Errorf("Held holding nothing on the folder = %v, %v, want none and no refusal", held, err)
	}
	if held, err := g.Held(ctx, "projects/"+boot, policy); err != nil || len(held) != 0 {
		t.Errorf("Held on the project holding nothing on the folder = %v, %v, want none and no refusal", held, err)
	}
	if found, err := g.FindFolder(ctx, node, "rgb"); err != nil || found != folder {
		t.Errorf("FindFolder by Folder Creator at the node = %q, %v, want %q", found, err, folder)
	}
	if exists, err := g.ProjectExists(ctx, boot); err != nil || !exists {
		t.Errorf("ProjectExists by Folder Creator at the node = %t, %v, want true", exists, err)
	}
	if got, want := fake.Holds(member), []string{node + " " + createtest.FolderCreator}; !slices.Equal(got, want) {
		t.Errorf("the identity holds %v, want %v", got, want)
	}
	if len(fake.Denied) != 10 {
		t.Errorf("%d refusals recorded, want the 10 above: %v", len(fake.Denied), fake.Denied)
	}

	// An identity that holds nothing anywhere is found no folder and no project, and makes
	// none.
	stranger, err := fake.Open(ctx, "stranger@imr-root-gbl-core-c3d4.iam.gserviceaccount.com")
	if err != nil {
		t.Fatal(err)
	}
	if found, err := stranger.FindFolder(ctx, node, "rgb"); err != nil || found != "" {
		t.Errorf("FindFolder holding nothing = %q, %v, want none", found, err)
	}
	if exists, err := stranger.ProjectExists(ctx, boot); err != nil || exists {
		t.Errorf("ProjectExists holding nothing = %t, %v, want false", exists, err)
	}
	_, err = stranger.CreateFolder(ctx, node, "other")
	refused(t, "CreateFolder holding nothing", err)

	// A seam never opened is a person's, and is refused nothing.
	person := createtest.New()
	passed(t, "CreateProject on a seam never opened", person.CreateProject(ctx, boot, "rgb-boot-gbl-core", "900000000001", nil))
	passed(t, "LinkBilling on a seam never opened", person.LinkBilling(ctx, boot, billing))
}

// TestHeld: the stand-in answers Held, Google's testIamPermissions, from the roles the
// caller holds on the resource and above it, and refuses it nothing. Each step takes a
// grant off as a person would, by other means than the seam, and reads what the identity
// then holds on the organization's folder, on a folder beneath and on a project.
func TestHeld(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	member := createtest.Member(creator)
	fake := createtest.New()
	fake.Bind(node, createtest.FolderCreator, member)
	g, err := fake.Open(ctx, creator)
	if err != nil {
		t.Fatal(err)
	}
	folder, err := g.CreateFolder(ctx, node, "rgb")
	passed(t, "CreateFolder under the node", err)
	beneath, err := g.CreateFolder(ctx, "folders/"+folder, "tst")
	passed(t, "CreateFolder under the folder", err)
	passed(t, "GrantFolder of Project Creator", g.GrantFolder(ctx, folder, createtest.ProjectCreator, member))
	passed(t, "GrantFolder of Tag Administrator", g.GrantFolder(ctx, folder, createtest.TagAdmin, member))
	passed(t, "CreateProject", g.CreateProject(ctx, boot, "rgb-boot-gbl-core", beneath, nil))

	folders := []string{createtest.FoldersGetPolicy, createtest.FoldersSetPolicy, createtest.ProjectsCreate, createtest.TagKeysCreate}
	projects := []string{createtest.ProjectsGetPolicy, createtest.ProjectsSetPolicy}
	read := folders[:1]
	steps := []struct {
		name         string
		off          []string
		at           string
		wantFolder   []string
		wantBeneath  []string
		wantProject  []string
		wantAtTheEnd bool
	}{
		{name: "a maker's roles and the two it granted itself", wantFolder: folders, wantBeneath: folders, wantProject: projects},
		{name: "Folder Admin off the organization's folder", off: []string{createtest.FolderAdmin}, at: "folders/" + folder, wantFolder: []string{folders[0], folders[2], folders[3]}, wantBeneath: folders, wantProject: projects},
		{name: "Folder Admin off the folder beneath", off: []string{createtest.FolderAdmin}, at: "folders/" + beneath, wantFolder: []string{folders[0], folders[2], folders[3]}, wantBeneath: []string{folders[0], folders[2], folders[3]}, wantProject: projects},
		{name: "Owner off the project", off: []string{createtest.Owner}, at: "projects/" + boot, wantFolder: []string{folders[0], folders[2], folders[3]}, wantBeneath: []string{folders[0], folders[2], folders[3]}},
		{name: "everything off the organization's folder", off: []string{createtest.FolderEditor, createtest.ProjectCreator, createtest.TagAdmin}, at: "folders/" + folder, wantBeneath: read},
		{name: "Folder Editor off the folder beneath: Folder Creator at the node alone", off: []string{createtest.FolderEditor}, at: "folders/" + beneath, wantAtTheEnd: true},
	}
	for _, step := range steps {
		for _, role := range step.off {
			fake.Unbind(step.at, role, member)
		}
		for _, asked := range []struct {
			resource    string
			permissions []string
			want        []string
		}{
			{"folders/" + folder, folders, step.wantFolder},
			{"folders/" + beneath, folders, step.wantBeneath},
			{"projects/" + boot, projects, step.wantProject},
		} {
			if held, err := g.Held(ctx, asked.resource, asked.permissions); err != nil || !slices.Equal(held, asked.want) {
				t.Errorf("%s: Held on %s = %v, %v, want %v", step.name, asked.resource, held, err, asked.want)
			}
		}
		if got, want := fake.Holds(member), []string{node + " " + createtest.FolderCreator}; step.wantAtTheEnd && !slices.Equal(got, want) {
			t.Errorf("%s: the identity holds %v, want %v", step.name, got, want)
		}
	}
	if len(fake.Denied) != 0 {
		t.Errorf("Held was refused: %v", fake.Denied)
	}

	// A seam never opened is a person's, whose grants the stand-in does not hold: it is
	// answered every permission it asks about.
	person := createtest.New()
	if held, err := person.Held(ctx, "folders/"+folder, folders); err != nil || !slices.Equal(held, folders) {
		t.Errorf("Held on a seam never opened = %v, %v, want %v", held, err, folders)
	}
}

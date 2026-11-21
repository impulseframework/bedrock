package create_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/impulseframework/bedrock/internal/create"
	"github.com/impulseframework/bedrock/internal/create/createtest"
	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
	"github.com/impulseframework/bedrock/internal/org"
)

const (
	rootFixture   = "../org/testdata/imr/placement.json"
	owner         = "imp-example"
	rootRepo      = "imr-root-infrastructure"
	baseSHA       = "base1"
	rootCommit    = "push7"
	creatingEmail = "imr-create-gbl-tofu@imr-root-gbl-core-c3d4.iam.gserviceaccount.com"
	bootIdentity  = "serviceAccount:rgb-boot-gbl-tofu@rgb-boot-gbl-core-e5f6.iam.gserviceaccount.com"
	policyAdmin   = "projects/imr-root-gbl-core-c3d4/roles/secretPolicyAdmin"
	// orgNode is the organization node the fixture names, and bootProject rgb's boot
	// project, as Google names the two as resources.
	orgNode     = "organizations/123456789012"
	bootProject = "projects/rgb-boot-gbl-core-e5f6"
)

// record is the hosting organization rgb as a person records it in the root
// organization's repository, filled.
func record(t *testing.T, root *org.Placement) *org.Placement {
	t.Helper()
	h := &org.Placement{
		Prefix: "rgb", AppsDomain: "rgb.impapps.example", Spanner: org.Spanner{Config: "nam10"},
		Regions:        []org.Region{{Name: "us-central1", Code: "uc1"}, {Name: "us-west3", Code: "uw3"}},
		TeamGroups:     map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"},
		ContactDomains: []string{"@imp.example"},
		Projects:       map[string]string{"boot": "rgb-boot-gbl-core-e5f6"},
		Labels:         map[string]string{"bedrock-lab": "true"},
		MonthlyBudget:  500,
	}
	if err := org.Fill(root, h); err != nil {
		t.Fatalf("Fill() error = %v", err)
	}

	return h
}

// checkout is the root organization's repository: its placement and the record of rgb.
func checkout(t *testing.T) (string, *org.Placement) {
	t.Helper()
	root, err := org.ReadPlacement(rootFixture)
	if err != nil {
		t.Fatalf("ReadPlacement() error = %v", err)
	}
	dir := t.TempDir()
	if err := root.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	h := record(t, root)
	path := org.RecordPath(dir, "rgb")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := h.Write(path); err != nil {
		t.Fatal(err)
	}

	return dir, root
}

// fakeRunner is the stand-in for tofu: it records the programs run and answers success,
// unless Google would refuse the identity a program runs as (asIdentity).
type fakeRunner struct {
	mu sync.Mutex
	// google is the stand-in the programs' calls are checked against, and where 1-org's
	// first apply puts the folders and the projects it makes.
	google      *createtest.FakeGoogle
	ran         []deploy.Command
	outputsRead []deploy.Command
	// refuse, when set, answers a command: what it writes and the error, or nil to let it
	// pass; the test's way of failing one apply.
	refuse func(c deploy.Command, out io.Writer) error
	// tainted, when set, is the address of a tainted google_project tofu show -json
	// answers with.
	tainted string
	// organized says 1-org's first apply went through: the folders and the projects stand.
	organized bool
}

func (r *fakeRunner) Run(_ context.Context, c deploy.Command, out io.Writer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, c)
	if r.refuse != nil {
		if err := r.refuse(c, out); err != nil {
			return err
		}
	}
	if err := r.asIdentity(c); err != nil {
		fmt.Fprintf(out, "Error: googleapi: Error 403: %v\n", err)

		return errors.New("exit status 1")
	}

	return nil
}

// asIdentity is what Google answers the program's calls, made as the identity the program
// impersonates: its refusal of the first one the identity may not make, or nil. Every
// program reads the layer's state, in the bucket of the boot project. 1-org's first apply
// makes the organization's four folders and six projects under the organization's folder,
// with the tag keys, as that identity, which Google then holds as their maker; each apply
// of 1-org after it reads what every one of the six projects holds.
func (r *fakeRunner) asIdentity(c deploy.Command) error {
	var member string
	for _, env := range c.Env {
		if email, ok := strings.CutPrefix(env, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT="); ok {
			member = createtest.Member(email)
		}
	}
	if err := r.google.Check(member, createtest.ObjectsGet, bootProject); err != nil {
		return err
	}
	if c.Args[0] != "apply" || filepath.Base(c.Dir) != "1-org" {
		return nil
	}
	ids, _ := orgProjects()
	if r.organized {
		for _, key := range orgProjectKeys {
			if err := r.google.Check(member, createtest.ServicesList, "projects/"+ids[key]); err != nil {
				return err
			}
		}

		return nil
	}
	folder := "folders/" + r.google.Folders[orgNode+"|rgb"]
	for _, permission := range []string{createtest.FoldersCreate, createtest.ProjectsCreate, createtest.TagKeysCreate} {
		if err := r.google.Check(member, permission, folder); err != nil {
			return err
		}
	}
	folders := orgFolders()
	for _, id := range folders {
		r.google.Stand("folders/"+id, folder, member)
	}
	for _, key := range orgProjectKeys {
		in := "shared"
		if slices.Contains([]string{"tst", "stg", "prd"}, key) {
			in = key
		}
		r.google.Stand("projects/"+ids[key], "folders/"+folders[in], member)
	}
	r.organized = true

	return nil
}

// Output answers tofu output -json with 1-org's outputs: six projects and four folders.
func (r *fakeRunner) Output(_ context.Context, c deploy.Command, _ io.Writer) ([]byte, error) {
	if c.Args[0] == "show" && r.tainted != "" {
		data, err := json.Marshal(map[string]any{"values": map[string]any{"root_module": map[string]any{
			"resources":     []any{},
			"child_modules": []any{map[string]any{"address": `module.project["prd"]`, "resources": []any{map[string]any{"address": r.tainted, "type": "google_project", "tainted": true}}}},
		}}})
		if err != nil {
			return nil, fmt.Errorf("json.Marshal(): %w", err)
		}

		return data, nil
	}
	if c.Args[0] != "output" {
		return nil, nil
	}
	r.mu.Lock()
	r.outputsRead = append(r.outputsRead, c)
	r.mu.Unlock()

	data, err := json.Marshal(orgOutputs())
	if err != nil {
		return nil, fmt.Errorf("json.Marshal(): %w", err)
	}

	return data, nil
}

// orgProjectKeys are the six projects 1-org makes, in the order the creation's
// withdrawal takes them.
var orgProjectKeys = []string{"shr", "net", "spn", "tst", "stg", "prd"}

// orgProjects are the ids and the numbers of the projects 1-org makes, by key.
func orgProjects() (ids, numbers map[string]string) {
	ids, numbers = map[string]string{}, map[string]string{}
	for i, key := range orgProjectKeys {
		ids[key] = "rgb-" + key + "-gbl-core-e5f6"
		numbers[key] = "10000000010" + strconv.Itoa(i+1)
	}

	return ids, numbers
}

// orgFolders are the ids of the folders 1-org makes, by key.
func orgFolders() map[string]string {
	return map[string]string{"shared": "900000000011", "tst": "900000000012", "stg": "900000000013", "prd": "900000000014"}
}

// orgOutputs are 1-org's outputs as tofu output -json prints them.
func orgOutputs() map[string]any {
	ids, numbers := orgProjects()
	folders := orgFolders()
	value := func(v any) map[string]any { return map[string]any{"value": v} }

	keys, values := map[string]string{}, map[string]string{}
	for i, env := range []string{"tst", "stg", "prd"} {
		keys[env] = "tagKeys/28100000000" + strconv.Itoa(i+1)
		values[env] = "tagValues/28200000000" + strconv.Itoa(i+1)
	}

	// The layer's other outputs come along too: strings, which the creation leaves alone.
	return map[string]any{"project_ids": value(ids), "project_numbers": value(numbers), "folder_ids": value(folders), "public_invoker_tag_key": value(keys), "public_invoker_tag_value": value(values), "billing_account_id": value("012345-6789AB-CDEF01"), "prefix": value("rgb")}
}

// fakeStore is the root organization's state bucket.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string]string
	// refuse, when set, is what a write answers: the test's way of stopping a creation at
	// its marker.
	refuse error
}

func (s *fakeStore) Write(_ context.Context, _, object string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refuse != nil {
		return s.refuse
	}
	s.objects[object] = string(data)

	return nil
}

func (s *fakeStore) List(context.Context, string, string) ([]string, error) {
	return nil, nil
}

func (s *fakeStore) Read(_ context.Context, _, object string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[object]
	if !ok {
		return nil, os.ErrNotExist
	}

	return []byte(data), nil
}

func (s *fakeStore) Delete(context.Context, string, string) error {
	return nil
}

func (*fakeStore) Close() error {
	return nil
}

type flow struct {
	google *createtest.FakeGoogle
	runner *fakeRunner
	store  *fakeStore
	srv    *githubtest.Server
	repo   *githubtest.Repo
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	srv := githubtest.New(t)
	repo := srv.AddRepo(owner, rootRepo, &githubtest.Repo{
		Refs:     map[string]github.Object{"refs/heads/master": {Type: "commit", SHA: baseSHA}},
		Ancestry: map[string][]string{baseSHA: {baseSHA}},
		Trees:    map[string]string{baseSHA: "tree0"},
		Files:    map[string]string{"tree0:placement.json": "{}", "tree0:README.md": "# imr\n"},
	})

	// The creating identity starts with what the bootstrap grants it at the organization
	// node, Folder Creator, and nothing else: every other grant it uses, it is given as the
	// maker of a folder or a project, or grants itself.
	google := createtest.New()
	google.Bind(orgNode, createtest.FolderCreator, createtest.Member(creatingEmail))

	return &flow{google: google, runner: &fakeRunner{google: google}, store: &fakeStore{objects: map[string]string{}}, srv: srv, repo: repo}
}

// clients are the creation's seams over the stand-ins. A wait ends at once, and the
// hundredth of a run fails: a creation waiting on a refusal that no wait ends would spin
// for its whole three minutes otherwise.
func (f *flow) clients() *create.Clients {
	waits := 0

	return &create.Clients{
		Google: f.google.Open,
		GitHub: func(token string) *github.Client { return github.New(f.srv.URL, token) },
		Exec:   f.runner,
		Store:  f.store,
		Sleep: func(context.Context, time.Duration) error {
			waits++
			if waits >= 100 {
				return errors.New("the creation waited a hundred times")
			}

			return nil
		},
	}
}

// pushed reports whether rgb's repository carries the foundation: placement.json on its
// default branch, which the creation's push puts there.
func (f *flow) pushed() bool {
	var pushed bool
	f.srv.Update(func() {
		repo, ok := f.srv.Repos[owner+"/rgb-infrastructure"]
		if !ok {
			return
		}
		_, pushed = repo.Files[repo.Trees[repo.Refs["refs/heads/master"].SHA]+":placement.json"]
	})

	return pushed
}

func request(root *org.Placement, dir string) *create.Request {
	return &create.Request{Root: root, RootDir: dir, Record: *root.HostingOrganization("rgb"), Token: "test-token", Commit: rootCommit}
}

// TestRun creates rgb from the root organization imr: the seed by API (both folders, the
// boot project with its billing link and APIs, the boot identity, the bucket, the grants
// on the folder and on the four shared secrets), the repository with the rendered
// foundation pushed on its initial commit (the seeded tfvars carrying the folder ids and
// the boot project), the first apply as the creating identity against the new bucket,
// the creating identity's withdrawal, the marker and the hand-off pull request; and a
// second run makes nothing and opens nothing.
func TestRun(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	var out bytes.Buffer
	result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	orgFolder := f.google.Folders["organizations/123456789012|rgb"]
	if orgFolder == "" {
		t.Fatalf("no folder rgb made under the organization; made: %v", f.google.Made())
	}
	tfFolder := f.google.Folders["folders/"+orgFolder+"|terraform"]
	wantMade := []string{
		"account rgb-boot-gbl-tofu@rgb-boot-gbl-core-e5f6.iam.gserviceaccount.com",
		"bucket rgb-boot-gbl-state-e5f6",
		"budget 012345-6789AB-CDEF01|rgb organization=500 folders/" + orgFolder,
		"folder folders/" + orgFolder + "|terraform=" + tfFolder,
		"folder organizations/123456789012|rgb=" + orgFolder,
		"project rgb-boot-gbl-core-e5f6 (100000000001, billing 012345-6789AB-CDEF01, 8 APIs)",
		"zone rgb-net-gbl-core-e5f6|rgb-net-gbl-dns-apps",
	}
	if got := f.google.Made(); !slices.Equal(got, wantMade) {
		t.Errorf("made %v, want %v", got, wantMade)
	}
	if want := []string{creatingEmail}; !slices.Equal(f.google.Identities, want) {
		t.Errorf("Google opened as %v, want %v", f.google.Identities, want)
	}
	wantGrants := []string{
		"folders/" + orgFolder + " roles/resourcemanager.projectCreator serviceAccount:" + creatingEmail,
		"folders/" + orgFolder + " roles/resourcemanager.tagAdmin serviceAccount:" + creatingEmail,
		"folders/" + orgFolder + " roles/cloudbuild.builds.editor " + bootIdentity,
		"folders/" + orgFolder + " roles/serviceusage.serviceUsageAdmin " + bootIdentity,
		"projects/rgb-boot-gbl-core-e5f6 roles/iam.roleAdmin " + bootIdentity,
		"folders/" + orgFolder + " roles/resourcemanager.folderAdmin user:seed@imp.example",
		"projects/imr-root-gbl-core-c3d4/secrets/imr-root-gbl-github-infrastructure-key " + policyAdmin + " " + bootIdentity,
		"projects/imr-root-gbl-core-c3d4/secrets/imr-root-gbl-github-release-key " + policyAdmin + " " + bootIdentity,
		"projects/imr-root-gbl-core-c3d4/secrets/imr-root-gbl-github-token " + policyAdmin + " " + bootIdentity,
	}
	for _, want := range wantGrants {
		if !slices.Contains(f.google.Grants, want) {
			t.Errorf("grants lack %q:\n%s", want, strings.Join(f.google.Grants, "\n"))
		}
	}
	if n := len(f.google.Grants); n != 2+len(org.BootLayerRoles)+len(org.BootProjectRoles)+1+4+2 {
		t.Errorf("%d grants, want %d (the two roles for the first applies, the boot roles on the folder and the project, Folder Admin, four secrets, two billing)", n, 2+len(org.BootLayerRoles)+len(org.BootProjectRoles)+1+4+2)
	}
	creator := " serviceAccount:" + creatingEmail + " roles/resourcemanager.folderAdmin+roles/resourcemanager.folderEditor"
	wantRevoked := []string{
		"folders/900000000014" + creator, "folders/900000000011" + creator, "folders/900000000013" + creator, "folders/900000000012" + creator,
		"projects/rgb-shr-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"projects/rgb-net-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"projects/rgb-spn-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"projects/rgb-tst-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"projects/rgb-stg-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"projects/rgb-prd-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"folders/" + tfFolder + creator,
		"projects/rgb-boot-gbl-core-e5f6 serviceAccount:" + creatingEmail + " roles/owner",
		"folders/" + orgFolder + creator + "+roles/resourcemanager.projectCreator+roles/resourcemanager.tagAdmin",
	}
	if !slices.Equal(f.google.Revoked, wantRevoked) {
		t.Errorf("revoked:\n%s\nwant:\n%s", strings.Join(f.google.Revoked, "\n"), strings.Join(wantRevoked, "\n"))
	}
	// Nothing was refused on the way, and the withdrawal left the creating identity what
	// it started with: its grant at the organization node, and nothing under the folder.
	if len(f.google.Denied) != 0 {
		t.Errorf("Google refused: %v", f.google.Denied)
	}
	if got, want := f.google.Holds(createtest.Member(creatingEmail)), []string{orgNode + " " + createtest.FolderCreator}; !slices.Equal(got, want) {
		t.Errorf("the creating identity holds %v at the end, want %v", got, want)
	}
	// The first applies, as the creating identity, in the rendered 0-bootstrap and 1-org,
	// the second with the infrastructure app's token, and 1-org's outputs read after it.
	if len(f.runner.ran) != 4 {
		t.Fatalf("%d programs ran, want tofu init and apply twice: %v", len(f.runner.ran), f.runner.ran)
	}
	init, apply := f.runner.ran[0], f.runner.ran[1]
	if want := "tofu init -input=false -no-color -backend-config=prefix=0-bootstrap -backend-config=impersonate_service_account=" + creatingEmail; init.String() != want {
		t.Errorf("init = %q, want %q", init.String(), want)
	}
	orgInit, orgApply := f.runner.ran[2], f.runner.ran[3]
	if want := "tofu init -input=false -no-color -backend-config=prefix=1-org -backend-config=impersonate_service_account=" + creatingEmail; orgInit.String() != want || filepath.Base(orgInit.Dir) != "1-org" {
		t.Errorf("1-org init = %q in %s, want %q", orgInit.String(), orgInit.Dir, want)
	}
	if filepath.Base(orgApply.Dir) != "1-org" || !slices.Contains(orgApply.Env, "GITHUB_TOKEN=test-token") || !slices.Contains(orgApply.Env, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT="+creatingEmail) {
		t.Errorf("1-org apply = %q in %s with %v", orgApply.String(), orgApply.Dir, orgApply.Env)
	}
	if len(f.runner.outputsRead) != 1 || f.runner.outputsRead[0].String() != "tofu output -no-color -json" || filepath.Base(f.runner.outputsRead[0].Dir) != "1-org" {
		t.Errorf("outputs read: %v", f.runner.outputsRead)
	}
	if want := "tofu apply -input=false -no-color -auto-approve"; apply.String() != want || filepath.Base(apply.Dir) != "0-bootstrap" || !slices.Contains(apply.Env, "GOOGLE_IMPERSONATE_SERVICE_ACCOUNT="+creatingEmail) {
		t.Errorf("apply = %q in %s with %v", apply.String(), apply.Dir, apply.Env)
	}
	// The repository and its first push.
	if want := []string{owner + "/rgb-infrastructure"}; !slices.Equal(f.srv.Created, want) {
		t.Errorf("repositories created %v, want %v", f.srv.Created, want)
	}
	hosting := f.srv.Repos[owner+"/rgb-infrastructure"]
	tree := hosting.Trees[hosting.Refs["refs/heads/master"].SHA]
	wantFiles := map[string][]string{
		"placement.json":               {`"prefix": "rgb"`, `"boot": "100000000001"`, `"organization": "imr"`, `"stateBucket": "rgb-boot-gbl-state-e5f6"`, `"net": "rgb-net-gbl-core-e5f6"`, `"prd": "100000000106"`},
		"0-bootstrap/cloud-build.tf":   {"100000000104"},
		"0-bootstrap/terraform.tfvars": {`boot_project_id        = "rgb-boot-gbl-core-e5f6"`, `organization_folder_id = "` + orgFolder + `"`, `terraform_folder_id    = "` + tfFolder + `"`},
		"0-bootstrap/initialize.tf":    {`bucket = "rgb-boot-gbl-state-e5f6"`},
		"1-org/terraform.tfvars":       {`boot_project_id    = "rgb-boot-gbl-core-e5f6"`},
		"README.md":                    {"# rgb"},
	}
	for path, wants := range wantFiles {
		content, ok := hosting.Files[tree+":"+path]
		if !ok {
			t.Errorf("the push lacks %s", path)

			continue
		}
		for _, want := range wants {
			if !strings.Contains(content, want) {
				t.Errorf("%s lacks %q", path, want)
			}
		}
	}
	if _, kept := hosting.Files[tree+":README.md"]; !kept {
		t.Error("the initial README is gone from the tree")
	}
	// The apps zone, made in the net project after 1-org and recorded.
	if want := "CreatePublicZone(rgb-net-gbl-core-e5f6, rgb-net-gbl-dns-apps, rgb.impapps.example.)"; !slices.Contains(f.google.Calls, want) {
		t.Errorf("calls lack %q:\n%s", want, strings.Join(f.google.Calls, "\n"))
	}
	if result.Placement.AppsZone == nil || result.Placement.AppsZone.Name != "rgb-net-gbl-dns-apps" || !slices.Equal(result.Placement.AppsZone.NameServers, createtest.ZoneNameServers) {
		t.Errorf("the recorded zone = %+v", result.Placement.AppsZone)
	}
	if !strings.Contains(hosting.Files[tree+":placement.json"], `"appsZone"`) || !strings.Contains(hosting.Files[tree+":2-net/dns.tf"], `id = "projects/${local.project_id}/managedZones/rgb-net-gbl-dns-apps"`) {
		t.Errorf("the push lacks the zone record or 2-net's import of it:\n%s", hosting.Files[tree+":2-net/dns.tf"])
	}
	// The marker and the hand-off.
	var marker create.Marker
	if err := json.Unmarshal([]byte(f.store.objects["applied/created/rgb"]), &marker); err != nil {
		t.Fatalf("no marker: %v; objects %v", err, f.store.objects)
	}
	if marker.OrganizationFolder != orgFolder || marker.BootProject != "rgb-boot-gbl-core-e5f6" || marker.Repository != owner+"/rgb-infrastructure" || marker.CreatedFrom != rootCommit || marker.AppsZone == nil || marker.AppsZone.Name != "rgb-net-gbl-dns-apps" {
		t.Errorf("marker = %+v", marker)
	}
	if marker.Projects["tst"] != "rgb-tst-gbl-core-e5f6" || marker.ProjectNumbers["tst"] != "100000000104" || len(marker.Projects) != 7 {
		t.Errorf("marker's projects = %v, %v", marker.Projects, marker.ProjectNumbers)
	}
	if result.PullRequest != "https://github.com/"+owner+"/"+rootRepo+"/pull/1" || result.Already {
		t.Errorf("result = %+v", result)
	}
	if len(f.repo.PullRequests) != 1 || f.repo.PullRequests[0].Head.Ref != "create/rgb" {
		t.Fatalf("pull requests %v", f.repo.PullRequests)
	}
	handOffTree := f.repo.Trees[f.repo.Refs["refs/heads/create/rgb"].SHA]
	if !strings.Contains(f.repo.Files[handOffTree+":placement.json"], `"folder": "`+orgFolder+`"`) {
		t.Errorf("the hand-off's placement.json lacks the folder:\n%s", f.repo.Files[handOffTree+":placement.json"])
	}
	var handedOff org.Placement
	if err := json.Unmarshal([]byte(f.repo.Files[handOffTree+":placement.json"]), &handedOff); err != nil {
		t.Fatalf("the hand-off's placement.json: %v", err)
	}
	if got := handedOff.Domains["impapps.example"].Labels["rgb"]; got.Organization != "rgb" || !slices.Equal(got.NameServers, createtest.ZoneNameServers) {
		t.Errorf("the hand-off hands the label rgb of impapps.example onward as %+v, want rgb with the zone's name servers", got)
	}
	if got := handedOff.Domains["impapps.example"].Labels["a"]; got.Organization != "imp" {
		t.Errorf("the label a of impapps.example, which was handed onward before, is %+v", got)
	}
	created := f.repo.Files[handOffTree+":organizations/rgb/placement.json"]
	if !strings.Contains(created, `"boot": "100000000001"`) || !strings.Contains(created, `"spn": "rgb-spn-gbl-core-e5f6"`) {
		t.Errorf("the hand-off's record lacks the projects:\n%s", created)
	}
	if readme := f.repo.Files[handOffTree+":README.md"]; !strings.Contains(readme, "4 name(s) are handed") {
		t.Errorf("the hand-off's README.md, rendered from the placement with the label handed onward, says:\n%s", readme)
	}
	if _, journal := f.repo.Files[handOffTree+":JOURNAL.md"]; journal {
		t.Errorf("the hand-off writes JOURNAL.md, a seeded file")
	}
	if body := f.srv.Bodies[0]; !strings.Contains(body, "folder `"+orgFolder+"`") || !strings.Contains(body, "the creating identity's own grants taken off") || !strings.Contains(body, "of its `1-org`") {
		t.Errorf("the hand-off's body:\n%s", body)
	}
	wantOut := []string{
		"=== creation rgb as " + creatingEmail + " ===\n",
		"Created folder rgb under organizations/123456789012: " + orgFolder + ".\n",
		"Created project rgb-boot-gbl-core-e5f6 under folder " + tfFolder + ".\n",
		"Granted the boot identity its 8 roles on folder " + orgFolder + " and 1 on the boot project, and the break-glass account Folder Admin on the folder.\n",
		"Granted the boot identity the policy administration of the 4 shared secrets.\n",
		"Created repository " + owner + "/rgb-infrastructure, private.\n",
		"Renamed the branch main of " + owner + "/rgb-infrastructure to master, the default branch placement.json names.\n",
		"=== first apply of rgb's 0-bootstrap as " + creatingEmail + ", state in gs://rgb-boot-gbl-state-e5f6/0-bootstrap ===\n",
		"=== first apply of rgb's 1-org as " + creatingEmail + ", state in gs://rgb-boot-gbl-state-e5f6/1-org ===\n",
		"Applied rgb's 1-org and recorded its 6 projects, 4 folders and 3 public-invoker tags in the placement (projects, projectNumbers, folders, publicInvokerTags).\n",
		"Pushed the foundation to " + owner + "/rgb-infrastructure at ",
		"Took the creating identity's grants off the 4 folders and 6 projects 1-org made, folder " + tfFolder + ", project rgb-boot-gbl-core-e5f6 and folder " + orgFolder + ".\n",
		"Recorded the creation of rgb in gs://imr-root-gbl-state-c3d4/applied/created/rgb.\n",
		"Made the zone rgb-net-gbl-dns-apps for rgb.impapps.example in rgb-net-gbl-core-e5f6, on " + strings.Join(createtest.ZoneNameServers, ", ") + ".\n",
		"Opened the hand-off pull request of rgb: https://github.com/" + owner + "/" + rootRepo + "/pull/1.\n",
	}
	for _, want := range wantOut {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}

	// A second run: created already, the hand-off open, nothing made.
	calls, grants := len(f.google.Calls), len(f.google.Grants)
	out.Reset()
	again, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() again error = %v; output:\n%s", err, out.String())
	}
	if !again.Already || again.PullRequest != result.PullRequest || len(f.google.Calls) != calls || len(f.google.Grants) != grants || len(f.repo.PullRequests) != 1 || len(f.runner.ran) != 4 {
		t.Errorf("the second run made something: result %+v, %d calls (was %d), %d pull requests, %d programs", again, len(f.google.Calls), calls, len(f.repo.PullRequests), len(f.runner.ran))
	}
	for _, want := range []string{
		"rgb was created on " + marker.CreatedAt + " (folder " + orgFolder + ", boot project rgb-boot-gbl-core-e5f6): nothing to make.\n",
		"The hand-off pull request of rgb is open and up to date: " + result.PullRequest + ".\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if f.repo.Refs["refs/heads/create/rgb"].SHA != f.repo.PullRequests[0].Head.SHA {
		t.Errorf("the branch moved to %s though the hand-off is up to date", f.repo.Refs["refs/heads/create/rgb"].SHA)
	}
}

// TestRunHandOffMoves: the hand-off pull request is open and what it records changed
// since (the root placement moved on); the run moves its branch to a commit with the
// hand-off as it is now, and the pull request stays open.
func TestRunHandOffMoves(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	var out bytes.Buffer
	first, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	was := f.repo.Refs["refs/heads/create/rgb"].SHA
	f.repo.Files[f.repo.Trees[was]+":placement.json"] = "{}"
	out.Reset()
	again, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() again error = %v; output:\n%s", err, out.String())
	}
	moved := f.repo.Refs["refs/heads/create/rgb"].SHA
	if !again.Already || again.PullRequest != first.PullRequest || len(f.repo.PullRequests) != 1 || moved == was || moved == "" {
		t.Errorf("result %+v, pull requests %v, branch %s (was %s)", again, f.repo.PullRequests, moved, was)
	}
	if want := "The hand-off pull request of rgb is open: " + first.PullRequest + "; its branch create/rgb moves to this run's commit " + moved[:7] + ", as what it records changed.\n"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
	if !strings.Contains(f.repo.Files[f.repo.Trees[moved]+":placement.json"], `"folder": "`+first.Marker.OrganizationFolder+`"`) {
		t.Errorf("the moved hand-off's placement.json:\n%s", f.repo.Files[f.repo.Trees[moved]+":placement.json"])
	}
}

// TestRunResumes creates an organization a run left half made: the folders, the project,
// the identity and the repository exist, so none is made again, and the rest follows.
func TestRunResumes(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	// The folders and the project stand as the creating identity made them, so it holds
	// what Google gives their maker.
	member := createtest.Member(creatingEmail)
	f.google.Folders["organizations/123456789012|rgb"] = "900000000007"
	f.google.Stand("folders/900000000007", orgNode, member)
	f.google.Folders["folders/900000000007|terraform"] = "900000000008"
	f.google.Stand("folders/900000000008", "folders/900000000007", member)
	f.google.Projects["rgb-boot-gbl-core-e5f6"], f.google.Numbers["rgb-boot-gbl-core-e5f6"] = true, "100000000042"
	f.google.Stand(bootProject, "folders/900000000008", member)
	f.google.Accounts["rgb-boot-gbl-tofu@rgb-boot-gbl-core-e5f6.iam.gserviceaccount.com"] = true
	f.google.Buckets["rgb-boot-gbl-state-e5f6"], f.google.BucketProjects["rgb-boot-gbl-state-e5f6"] = true, "rgb-boot-gbl-core-e5f6"
	f.google.SpendBudgets["012345-6789AB-CDEF01"] = []org.Budget{org.HostingBudget("rgb", 500, "900000000007")}
	f.google.Zones["rgb-net-gbl-core-e5f6|rgb-net-gbl-dns-apps"] = createtest.ZoneNameServers
	f.srv.AddRepo(owner, "rgb-infrastructure", &githubtest.Repo{
		Refs: map[string]github.Object{"refs/heads/master": {Type: "commit", SHA: "init1"}}, Ancestry: map[string][]string{"init1": {"init1"}},
		Trees: map[string]string{"init1": "treeinit"}, Files: map[string]string{"treeinit:README.md": "# rgb-infrastructure\n"},
	})
	var out bytes.Buffer
	result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	for _, call := range f.google.Calls {
		if strings.HasPrefix(call, "Create") {
			t.Errorf("made again: %s", call)
		}
	}
	if len(f.srv.Created) != 0 {
		t.Errorf("repositories created: %v", f.srv.Created)
	}
	wantOut := []string{
		"Folder rgb exists under organizations/123456789012: 900000000007.\n",
		"The spend budget \"rgb organization\" stands on 012345-6789AB-CDEF01 already; the root organization's apply keeps it.\n",
		"Project rgb-boot-gbl-core-e5f6 exists.\n",
		"Boot identity rgb-boot-gbl-tofu@rgb-boot-gbl-core-e5f6.iam.gserviceaccount.com exists.\n",
		"State bucket rgb-boot-gbl-state-e5f6 exists.\n",
		"Repository " + owner + "/rgb-infrastructure exists.\n",
		"Zone rgb-net-gbl-dns-apps for rgb.impapps.example exists in rgb-net-gbl-core-e5f6, on " + strings.Join(createtest.ZoneNameServers, ", ") + ".\n",
		"Pushed the foundation to " + owner + "/rgb-infrastructure at ",
	}
	for _, want := range wantOut {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if result.Marker.OrganizationFolder != "900000000007" || result.Marker.BootProjectNumber != "100000000042" {
		t.Errorf("marker = %+v", result.Marker)
	}
	if len(f.google.Denied) != 0 {
		t.Errorf("Google refused: %v", f.google.Denied)
	}
}

// errStopped is what a stand-in answers the call a test stops a creation at.
var errStopped = errorString("stopped by the test")

// TestRunAgainAfterPush: a creation that stopped after its push finishes when run again,
// from every point it can stop at: right after the push, after each removal of the
// withdrawal, and after the last, before the marker. The stand-ins refuse what Google
// would refuse the creating identity by then, and the run again is refused nothing: it
// runs no first apply and pushes nothing, takes off what the withdrawal left, records the
// creation and opens the hand-off, and the creating identity holds nothing under the
// folder at the end.
func TestRunAgainAfterPush(t *testing.T) {
	t.Parallel()

	const removals = 13
	for done := 0; done <= removals; done++ {
		t.Run(fmt.Sprintf("stopped after %d of the %d removals", done, removals), func(t *testing.T) {
			t.Parallel()

			f := newFlow(t)
			dir, root := checkout(t)
			if done == removals {
				f.store.refuse = errStopped
			} else {
				revokes := 0
				f.google.Stop = func(call string) error {
					if !strings.HasPrefix(call, "Revoke") {
						return nil
					}
					revokes++
					if revokes > done {
						return errStopped
					}

					return nil
				}
			}
			var out bytes.Buffer
			if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err == nil || !strings.Contains(err.Error(), string(errStopped)) {
				t.Fatalf("Run() error = %v, want the stop; output:\n%s", err, out.String())
			}
			hosting := f.srv.Repos[owner+"/rgb-infrastructure"]
			if !f.pushed() || len(f.google.Revoked) != done || len(f.store.objects) != 0 {
				t.Fatalf("the creation stopped with the foundation pushed: %t, %d removals made (want %d) and the objects %v (want none)", f.pushed(), len(f.google.Revoked), done, f.store.objects)
			}
			head := hosting.Refs["refs/heads/master"].SHA
			orgFolder := f.google.Folders[orgNode+"|rgb"]
			tfFolder := f.google.Folders["folders/"+orgFolder+"|terraform"]

			f.google.Stop, f.store.refuse = nil, nil
			programs, denied := len(f.runner.ran), len(f.google.Denied)
			out.Reset()
			result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
			if err != nil {
				t.Fatalf("Run() again error = %v; Google refused %v; output:\n%s", err, slices.Compact(f.google.Denied[denied:]), out.String())
			}
			if again := f.runner.ran[programs:]; len(again) != 0 {
				t.Errorf("the run again ran %d program(s), want none: the first applies are not run again once the foundation is pushed: %v", len(again), again)
			}
			if refused := f.google.Denied[denied:]; len(refused) != 0 {
				t.Errorf("the run again was refused: %v", refused)
			}
			if got := hosting.Refs["refs/heads/master"].SHA; got != head || strings.Contains(out.String(), "Pushed the foundation") {
				t.Errorf("master of rgb-infrastructure is at %s, was %s: the run again pushes nothing; output:\n%s", got, head, out.String())
			}
			if got, want := f.google.Holds(createtest.Member(creatingEmail)), []string{orgNode + " " + createtest.FolderCreator}; !slices.Equal(got, want) {
				t.Errorf("the creating identity holds %v at the end, want %v", got, want)
			}
			var marker create.Marker
			if err := json.Unmarshal([]byte(f.store.objects["applied/created/rgb"]), &marker); err != nil {
				t.Fatalf("no marker: %v; objects %v", err, f.store.objects)
			}
			if marker.OrganizationFolder != orgFolder || marker.TerraformFolder != tfFolder || marker.BootProjectNumber != "100000000001" || marker.Repository != owner+"/rgb-infrastructure" || marker.CreatedFrom != rootCommit {
				t.Errorf("marker = %+v", marker)
			}
			if len(marker.Projects) != 7 || marker.ProjectNumbers["tst"] != "100000000104" || !maps.Equal(marker.Folders, orgFolders()) || len(marker.PublicInvokerTags) != 3 || marker.AppsZone == nil || marker.AppsZone.Name != "rgb-net-gbl-dns-apps" {
				t.Errorf("the marker records the projects %v, %v, the folders %v, the tags %v and the zone %+v", marker.Projects, marker.ProjectNumbers, marker.Folders, marker.PublicInvokerTags, marker.AppsZone)
			}
			if result.Already || result.PullRequest != "https://github.com/"+owner+"/"+rootRepo+"/pull/1" || len(f.repo.PullRequests) != 1 {
				t.Errorf("result = %+v, pull requests %v", result, f.repo.PullRequests)
			}
			if !maps.Equal(result.Placement.Folders, orgFolders()) || len(result.Placement.PublicInvokerTags) != 3 {
				t.Errorf("the result's placement records the folders %v and the tags %v, which the root organization's policies are set from", result.Placement.Folders, result.Placement.PublicInvokerTags)
			}
			handOffTree := f.repo.Trees[f.repo.Refs["refs/heads/create/rgb"].SHA]
			if created := f.repo.Files[handOffTree+":organizations/rgb/placement.json"]; !strings.Contains(created, `"boot": "100000000001"`) || !strings.Contains(created, `"spn": "rgb-spn-gbl-core-e5f6"`) || !strings.Contains(created, `"appsZone"`) {
				t.Errorf("the hand-off's record lacks the projects or the zone:\n%s", created)
			}
			withdrawal := "Took the creating identity's grants off the 4 folders and 6 projects 1-org made, folder " + tfFolder + ", project rgb-boot-gbl-core-e5f6 and folder " + orgFolder + ".\n"
			if done == removals {
				withdrawal = "The creating identity holds nothing on the 6 folders and 7 projects its withdrawal covers any longer, as Google answers for each: the withdrawal ended in the run that stopped, and nothing is left to take off.\n"
			}
			for _, want := range []string{
				"master of " + owner + "/rgb-infrastructure carries the foundation already (" + head[:7] + ")",
				withdrawal,
				"Recorded the creation of rgb in gs://imr-root-gbl-state-c3d4/applied/created/rgb.\n",
				"Created rgb: folder " + orgFolder + ", boot project rgb-boot-gbl-core-e5f6 (100000000001), repository " + owner + "/rgb-infrastructure at " + head[:7] + "; the creating identity holds nothing under the folder now.\n",
				"Opened the hand-off pull request of rgb: " + result.PullRequest + ".\n",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

// pushedFile changes a file of the foundation on master of rgb's repository, as the
// creation pushed it: a test's way to a push that is not what this creation recorded.
func (f *flow) pushedFile(path string, change func(content string) string) {
	f.srv.Update(func() {
		repo := f.srv.Repos[owner+"/rgb-infrastructure"]
		file := repo.Trees[repo.Refs["refs/heads/master"].SHA] + ":" + path
		repo.Files[file] = change(repo.Files[file])
	})
}

// pushedPlacement changes the pushed placement.json, read as JSON.
func (f *flow) pushedPlacement(t *testing.T, change func(placement map[string]any)) {
	t.Helper()
	f.pushedFile("placement.json", func(content string) string {
		var placement map[string]any
		if err := json.Unmarshal([]byte(content), &placement); err != nil {
			t.Fatal(err)
		}
		change(placement)
		data, err := json.Marshal(placement)
		if err != nil {
			t.Fatal(err)
		}

		return string(data)
	})
}

// object is the JSON object under the key of the pushed placement.
func object(t *testing.T, placement map[string]any, key string) map[string]any {
	t.Helper()
	o, ok := placement[key].(map[string]any)
	if !ok {
		t.Fatalf("the pushed placement's %s is %T, want an object", key, placement[key])
	}

	return o
}

// TestRunAgainAfterPushRefused: a creation run again after its push goes on only from what
// its own push recorded and the folders the creation made. A pushed placement that is not
// the record's (another boot project, a project or a folder of the creation missing), a
// folder that is not found, or a folder found by its name that is not the one the pushed
// 0-bootstrap/terraform.tfvars names stops it with no grant taken off and nothing
// recorded: a withdrawal over less than the creation made, or over another folder, would
// leave the identity holding the rest, under a marker that says it holds nothing.
func TestRunAgainAfterPushRefused(t *testing.T) {
	t.Parallel()

	const differs = "placement.json on master of " + owner + "/rgb-infrastructure carries this organization's state bucket and is not what this creation's push records ("
	tests := []struct {
		name    string
		spoil   func(t *testing.T, f *flow)
		wantErr string
	}{
		{
			name: "the pushed placement lacks the folders",
			spoil: func(t *testing.T, f *flow) {
				t.Helper()
				f.pushedPlacement(t, func(placement map[string]any) { delete(placement, "folders") })
			},
			wantErr: differs + "folders.shared is missing, folders.tst is missing, folders.stg is missing, folders.prd is missing): the creation cannot go on from it",
		},
		{
			name: "the pushed placement lacks one of the four folders",
			spoil: func(t *testing.T, f *flow) {
				t.Helper()
				f.pushedPlacement(t, func(placement map[string]any) { delete(object(t, placement, "folders"), "stg") })
			},
			wantErr: differs + "folders.stg is missing): the creation cannot go on from it",
		},
		{
			name: "the pushed placement names another boot project",
			spoil: func(t *testing.T, f *flow) {
				t.Helper()
				f.pushedPlacement(t, func(placement map[string]any) {
					object(t, placement, "projects")["boot"] = "rgb-boot-gbl-core-0000"
				})
			},
			wantErr: differs + "projects.boot is rgb-boot-gbl-core-0000 where the record names rgb-boot-gbl-core-e5f6): the creation cannot go on from it",
		},
		{
			name: "the pushed placement lacks one of the creation's projects",
			spoil: func(t *testing.T, f *flow) {
				t.Helper()
				f.pushedPlacement(t, func(placement map[string]any) { delete(object(t, placement, "projects"), "net") })
			},
			wantErr: differs + "projects.net is missing): the creation cannot go on from it",
		},
		{
			name: "the pushed placement lacks the boot project's number",
			spoil: func(t *testing.T, f *flow) {
				t.Helper()
				f.pushedPlacement(t, func(placement map[string]any) { delete(object(t, placement, "projectNumbers"), "boot") })
			},
			wantErr: differs + "projectNumbers.boot is missing): the creation cannot go on from it",
		},
		{
			name: "the organization's folder is not found",
			spoil: func(_ *testing.T, f *flow) {
				delete(f.google.Folders, orgNode+"|rgb")
			},
			wantErr: "the creating identity finds no folder rgb under " + orgNode,
		},
		{
			name: "the terraform folder is not found",
			spoil: func(_ *testing.T, f *flow) {
				delete(f.google.Folders, "folders/"+f.google.Folders[orgNode+"|rgb"]+"|terraform")
			},
			wantErr: "the creating identity finds no folder terraform under folders/",
		},
		{
			// Another folder of the organization's name stands under the node, which the
			// identity reads by its Folder Creator there: not the one this creation made.
			name: "the folder found by the organization's name is not the one the push recorded",
			spoil: func(_ *testing.T, f *flow) {
				f.google.Stand("folders/900000000099", orgNode, "")
				f.google.Folders[orgNode+"|rgb"] = "900000000099"
			},
			wantErr: "the creating identity finds folder 900000000099 by the name rgb under " + orgNode + ", and 0-bootstrap/terraform.tfvars on master of " + owner + "/rgb-infrastructure records organization_folder_id 900000000001",
		},
		{
			name: "the terraform folder found by its name is not the one the push recorded",
			spoil: func(_ *testing.T, f *flow) {
				f.pushedFile("0-bootstrap/terraform.tfvars", func(content string) string {
					return strings.Replace(content, `terraform_folder_id    = "900000000002"`, `terraform_folder_id    = "900000000077"`, 1)
				})
			},
			wantErr: "the creating identity finds folder 900000000002 by the name terraform under folders/900000000001, and 0-bootstrap/terraform.tfvars on master of " + owner + "/rgb-infrastructure records terraform_folder_id 900000000077",
		},
		{
			name: "the pushed tfvars names no organization folder",
			spoil: func(_ *testing.T, f *flow) {
				f.pushedFile("0-bootstrap/terraform.tfvars", func(content string) string {
					return strings.Replace(content, `organization_folder_id = "`, `organization_folder = "`, 1)
				})
			},
			wantErr: "0-bootstrap/terraform.tfvars on master of " + owner + "/rgb-infrastructure carries no organization_folder_id",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFlow(t)
			dir, root := checkout(t)
			f.google.Stop = func(call string) error {
				if strings.HasPrefix(call, "Revoke") {
					return errStopped
				}

				return nil
			}
			var out bytes.Buffer
			if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err == nil || !strings.Contains(err.Error(), string(errStopped)) {
				t.Fatalf("Run() error = %v, want the stop; output:\n%s", err, out.String())
			}
			f.google.Stop = nil
			tt.spoil(t, f)
			held := f.google.Holds(createtest.Member(creatingEmail))
			out.Reset()
			_, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Run() again error = %v, want %q; output:\n%s", err, tt.wantErr, out.String())
			}
			if got := f.google.Holds(createtest.Member(creatingEmail)); !slices.Equal(got, held) || len(f.store.objects) != 0 {
				t.Errorf("the refused run left the identity holding %v (it held %v) and recorded %v, want no grant taken off and nothing recorded", got, held, f.store.objects)
			}
		})
	}
}

// stopAfterPush runs rgb's creation and stops it after the given number of its
// withdrawal's removals: the foundation is pushed, and nothing is recorded.
func stopAfterPush(t *testing.T, f *flow, dir string, root *org.Placement, done int) {
	t.Helper()
	revokes := 0
	f.google.Stop = func(call string) error {
		if !strings.HasPrefix(call, "Revoke") {
			return nil
		}
		revokes++
		if revokes > done {
			return errStopped
		}

		return nil
	}
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err == nil || !strings.Contains(err.Error(), string(errStopped)) {
		t.Fatalf("Run() error = %v, want the stop; output:\n%s", err, out.String())
	}
	f.google.Stop = nil
	if !f.pushed() || len(f.google.Revoked) != done || len(f.store.objects) != 0 {
		t.Fatalf("the creation stopped with the foundation pushed: %t, %d removals made (want %d) and the objects %v (want none)", f.pushed(), len(f.google.Revoked), done, f.store.objects)
	}
}

// organizationFolderRoles are the roles the creating identity holds on the organization's
// folder until its withdrawal's last removal: a maker's two, and the two it grants itself.
var organizationFolderRoles = []string{createtest.FolderAdmin, createtest.FolderEditor, createtest.ProjectCreator, createtest.TagAdmin}

// TestRunAgainAfterPushTakenOffByOtherMeans: the run again decides what is left of the
// withdrawal from every folder and project the withdrawal covers, not from the
// organization's folder alone. The identity's roles on that folder come off last, and its
// Folder Admin there reaches everything beneath; when a person took the identity out of
// the folder's policy before the withdrawal ended, the grants beneath still stand, each
// the identity's own to take off (a maker's Folder Admin, a maker's Owner). The run again
// takes them off, reads no policy the identity may no longer read, and the identity holds
// nothing under the folder at the end, as the marker and the closing line say.
func TestRunAgainAfterPushTakenOffByOtherMeans(t *testing.T) {
	t.Parallel()

	const removals = 13
	for _, done := range []int{0, 4, 7, 11} {
		t.Run(fmt.Sprintf("stopped after %d of the %d removals", done, removals), func(t *testing.T) {
			t.Parallel()

			f := newFlow(t)
			dir, root := checkout(t)
			stopAfterPush(t, f, dir, root, done)
			member := createtest.Member(creatingEmail)
			orgFolder := f.google.Folders[orgNode+"|rgb"]
			for _, role := range organizationFolderRoles {
				f.google.Unbind("folders/"+orgFolder, role, member)
			}
			left := removals - 1 - done

			denied := len(f.google.Denied)
			var out bytes.Buffer
			if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
				t.Fatalf("Run() again error = %v; Google refused %v; output:\n%s", err, slices.Compact(f.google.Denied[denied:]), out.String())
			}
			if refused := f.google.Denied[denied:]; len(refused) != 0 {
				t.Errorf("the run again was refused: %v", refused)
			}
			if got, want := f.google.Holds(member), []string{orgNode + " " + createtest.FolderCreator}; !slices.Equal(got, want) {
				t.Errorf("the creating identity holds %v at the end, want %v", got, want)
			}
			if got := len(f.google.Revoked) - done; got != left {
				t.Errorf("the run again made %d removals, want the %d that were left beneath the organization's folder: %v", got, left, f.google.Revoked[done:])
			}
			if f.store.objects["applied/created/rgb"] == "" {
				t.Errorf("no marker: objects %v", f.store.objects)
			}
			for _, want := range []string{
				fmt.Sprintf("Took what was left of the creating identity's grants off %d of the %d folders and projects its withdrawal covers: ", left, removals),
				"; the creating identity holds nothing under the folder now.\n",
			} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output lacks %q:\n%s", want, out.String())
				}
			}
		})
	}
}

// TestRunAgainAfterPushNotItsOwnToTakeOff: Folder Admin alone off the organization's
// folder leaves the identity its Folder Editor, Project Creator and Tag Administrator
// there, none of which sets the folder's policy: they are not its own to take off. The
// run again takes off everything beneath, which is, and then stops with a sentence that
// names the folder and what the identity still holds there, the marker unwritten and no
// line saying it holds nothing. Once a person has taken the three off, the next run
// finishes.
func TestRunAgainAfterPushNotItsOwnToTakeOff(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	stopAfterPush(t, f, dir, root, 0)
	member := createtest.Member(creatingEmail)
	orgFolder := f.google.Folders[orgNode+"|rgb"]
	f.google.Unbind("folders/"+orgFolder, createtest.FolderAdmin, member)

	denied := len(f.google.Denied)
	var out bytes.Buffer
	_, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	wantErr := "still holds resourcemanager.folders.getIamPolicy, resourcemanager.projects.create, resourcemanager.tagKeys.create on folder " + orgFolder + " and may not set the policy there"
	if err == nil || !strings.Contains(err.Error(), wantErr) || !strings.Contains(err.Error(), "the break-glass account "+root.BreakGlassAccount) {
		t.Fatalf("Run() again error = %v, want %q and the break-glass account; output:\n%s", err, wantErr, out.String())
	}
	if refused := f.google.Denied[denied:]; len(refused) != 0 {
		t.Errorf("the run again was refused by Google, where it asks before it writes: %v", refused)
	}
	want := []string{
		"folders/" + orgFolder + " " + createtest.FolderEditor,
		"folders/" + orgFolder + " " + createtest.ProjectCreator,
		"folders/" + orgFolder + " " + createtest.TagAdmin,
		orgNode + " " + createtest.FolderCreator,
	}
	if got := f.google.Holds(member); !slices.Equal(got, want) {
		t.Errorf("the creating identity holds %v after the refused run, want what is not its own to take off and nothing beneath: %v", got, want)
	}
	if len(f.store.objects) != 0 || strings.Contains(out.String(), "holds nothing") {
		t.Errorf("the refused run recorded %v or said the identity holds nothing; output:\n%s", f.store.objects, out.String())
	}

	for _, role := range organizationFolderRoles[1:] {
		f.google.Unbind("folders/"+orgFolder, role, member)
	}
	out.Reset()
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
		t.Fatalf("Run() after a person took the roles off error = %v; output:\n%s", err, out.String())
	}
	if got, want := f.google.Holds(member), []string{orgNode + " " + createtest.FolderCreator}; !slices.Equal(got, want) {
		t.Errorf("the creating identity holds %v at the end, want %v", got, want)
	}
	if f.store.objects["applied/created/rgb"] == "" || !strings.Contains(out.String(), "The creating identity holds nothing on the 6 folders and 7 projects its withdrawal covers any longer, as Google answers for each") {
		t.Errorf("the last run recorded %v; output:\n%s", f.store.objects, out.String())
	}
}

// TestFirstApplyRefused: the stand-in for tofu refuses what Google would refuse the
// creating identity. Once a creation ended, the identity holds nothing under the folder,
// and an apply as it is refused at the layer's state; holding the boot project again and
// none of the projects 1-org made, it is refused at the first of those.
func TestFirstApplyRefused(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	apply := deploy.Command{Dir: filepath.Join(dir, "1-org"), Env: []string{"GOOGLE_IMPERSONATE_SERVICE_ACCOUNT=" + creatingEmail}, Name: "tofu", Args: []string{"apply"}}
	out.Reset()
	if err := f.runner.Run(t.Context(), apply, &out); err == nil || !strings.Contains(out.String(), "Error 403") || !strings.Contains(out.String(), "'storage.objects.get' denied on resource '"+bootProject+"'") {
		t.Errorf("an apply as the creating identity after its withdrawal: error %v, output %q, want Google's refusal of the state's read", err, out.String())
	}
	f.google.Bind(bootProject, createtest.Owner, createtest.Member(creatingEmail))
	out.Reset()
	if err := f.runner.Run(t.Context(), apply, &out); err == nil || !strings.Contains(out.String(), "'serviceusage.services.list' denied on resource 'projects/rgb-shr-gbl-core-e5f6'") {
		t.Errorf("an apply of 1-org as the creating identity without Owner on its projects: error %v, output %q, want Google's refusal at the first project", err, out.String())
	}
}

// TestRunStartsOver creates an organization again in the repository its earlier
// foundation was pushed to, destroyed since (0-bootstrap/README.md, "Destroying the
// organization"): the branch's placement names another state bucket, so it is history
// and not this run's own push, and the new foundation lands on it.
func TestRunStartsOver(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	f.srv.AddRepo(owner, "rgb-infrastructure", &githubtest.Repo{
		Refs: map[string]github.Object{"refs/heads/master": {Type: "commit", SHA: "old1"}}, Ancestry: map[string][]string{"old1": {"old1"}},
		Trees: map[string]string{"old1": "treeold"}, Files: map[string]string{"treeold:placement.json": "{\"prefix\": \"rgb\", \"stateBucket\": \"rgb-boot-gbl-state-0ld1\"}\n", "treeold:README.md": "# rgb-infrastructure\n"},
	})
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if len(f.srv.Created) != 0 {
		t.Errorf("repositories created: %v", f.srv.Created)
	}
	hosting := f.srv.Repos[owner+"/rgb-infrastructure"]
	head := hosting.Refs["refs/heads/master"].SHA
	if head == "old1" || !slices.Contains(hosting.Ancestry[head], "old1") {
		t.Errorf("master is at %s with ancestry %v: the foundation goes on the earlier one", head, hosting.Ancestry[head])
	}
	if placement := hosting.Files[hosting.Trees[head]+":placement.json"]; !strings.Contains(placement, `"stateBucket": "rgb-boot-gbl-state-e5f6"`) {
		t.Errorf("placement.json on master:\n%s", placement)
	}
	wantOut := []string{
		"master of " + owner + "/rgb-infrastructure carries the foundation of an earlier rgb organization (state bucket rgb-boot-gbl-state-0ld1), destroyed since; it stays as history, and this foundation is pushed on it.\n",
		"Pushed the foundation to " + owner + "/rgb-infrastructure at ",
	}
	for _, want := range wantOut {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRunStartsOverMarker: the root's state bucket holds the marker of an earlier rgb,
// destroyed and started over, which names another boot project; the creation goes on
// and leaves its own marker, instead of taking the organization as created.
func TestRunStartsOverMarker(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	f.store.objects["applied/created/rgb"] = `{"prefix": "rgb", "organizationFolder": "900000000001", "bootProject": "rgb-boot-gbl-core-0ld1", "stateBucket": "rgb-boot-gbl-state-0ld1", "createdAt": "2026-11-01T00:00:00Z"}`
	var out bytes.Buffer
	result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if result.Already || result.Marker.BootProject != "rgb-boot-gbl-core-e5f6" {
		t.Errorf("result = %+v", result)
	}
	for _, want := range []string{
		"The marker of rgb in gs://imr-root-gbl-state-c3d4 is an earlier organization's (boot project rgb-boot-gbl-core-0ld1, created on 2026-11-01T00:00:00Z), destroyed since; this creation, with the boot project rgb-boot-gbl-core-e5f6, goes on and replaces it.\n",
		"Recorded the creation of rgb in gs://imr-root-gbl-state-c3d4/applied/created/rgb.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(f.store.objects["applied/created/rgb"], `"bootProject": "rgb-boot-gbl-core-e5f6"`) {
		t.Errorf("the marker after: %s", f.store.objects["applied/created/rgb"])
	}
}

// TestRunStartsOverHandOff: the root repository holds the merged hand-off pull request of
// an earlier rgb, destroyed and started over, from the same branch (its title names the
// earlier boot project); the creation passes it as history and opens its own, instead of
// taking the earlier one as this organization's hand-off and recording nothing.
func TestRunStartsOverHandOff(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	earlier := "https://github.com/" + owner + "/" + rootRepo + "/pull/1"
	f.repo.PullRequests = append(f.repo.PullRequests, github.PullRequest{
		Number: 1, State: "closed", MergedAt: "2026-11-01T01:00:00Z", HTMLURL: earlier,
		Title: "chore: rgb is created (folder 900000000001, boot project rgb-boot-gbl-core-0ld1)",
		Head:  github.PullRequestRef{Ref: "create/rgb", SHA: "gone1"},
		Base:  github.PullRequestRef{Ref: "master", SHA: baseSHA},
	})
	var out bytes.Buffer
	result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	opened := "https://github.com/" + owner + "/" + rootRepo + "/pull/2"
	if result.PullRequest != opened || len(f.repo.PullRequests) != 2 || f.repo.PullRequests[1].State != "open" || f.repo.PullRequests[1].Head.Ref != "create/rgb" {
		t.Errorf("result %+v, pull requests %v", result, f.repo.PullRequests)
	}
	for _, want := range []string{
		"The pull request " + earlier + " from create/rgb is an earlier rgb organization's hand-off, merged, destroyed since; it stays as history, and this creation opens its own.\n",
		"Opened the hand-off pull request of rgb: " + opened + ".\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

// TestRunHandsOffFromMarker: the hand-off of a run that makes nothing (the marker says
// created) carries everything the marker records, the apps zone and the label handed
// onward among them; and a closed unmerged hand-off pull request is history, so this run
// opens another.
func TestRunHandsOffFromMarker(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	var out bytes.Buffer
	first, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	f.repo.PullRequests[0].State = "closed"
	f.repo.PullStates[1] = "closed"
	out.Reset()
	again, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() again error = %v; output:\n%s", err, out.String())
	}
	opened := "https://github.com/" + owner + "/" + rootRepo + "/pull/2"
	if !again.Already || again.PullRequest != opened || len(f.repo.PullRequests) != 2 || f.repo.PullRequests[1].State != "open" {
		t.Errorf("result %+v, pull requests %v", again, f.repo.PullRequests)
	}
	for _, want := range []string{
		"The hand-off pull request of rgb was closed unmerged: " + first.PullRequest + "; it stays as history, and this creation opens another (to end the hand-off, take rgb's record out of placement.json).\n",
		"Opened the hand-off pull request of rgb: " + opened + ".\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	tree := f.repo.Trees[f.repo.Refs["refs/heads/create/rgb"].SHA]
	var handedOff org.Placement
	if err := json.Unmarshal([]byte(f.repo.Files[tree+":placement.json"]), &handedOff); err != nil {
		t.Fatalf("the hand-off's placement.json: %v", err)
	}
	if got := handedOff.Domains["impapps.example"].Labels["rgb"]; got.Organization != "rgb" || !slices.Equal(got.NameServers, createtest.ZoneNameServers) {
		t.Errorf("the hand-off from the marker hands the label rgb onward as %+v, want rgb with the zone's name servers", got)
	}
	created := f.repo.Files[tree+":organizations/rgb/placement.json"]
	if !strings.Contains(created, `"appsZone"`) || !strings.Contains(created, `"rgb-net-gbl-dns-apps"`) || !strings.Contains(created, `"spn": "rgb-spn-gbl-core-e5f6"`) {
		t.Errorf("the hand-off's record from the marker lacks the zone or the projects:\n%s", created)
	}
	if body := f.srv.Bodies[1]; !strings.Contains(body, "`rgb-net-gbl-dns-apps`") || strings.Contains(body, "no zone was recorded") {
		t.Errorf("the hand-off's body from the marker:\n%s", body)
	}
}

// TestRunFirstApplyAgainAfterRace: 1-org's first apply is refused once by Google's race
// (an API enabled moments ago not seen by every service yet); the creation waits and
// runs the apply once more, and goes on.
func TestRunFirstApplyAgainAfterRace(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	refused := false
	f.runner.refuse = func(c deploy.Command, out io.Writer) error {
		if refused || c.Args[0] != "apply" || filepath.Base(c.Dir) != "1-org" {
			return nil
		}
		refused = true
		fmt.Fprintln(out, "Error: Error deleting default network in project rgb-prd-gbl-core-1a2b: googleapi: Error 403: Compute Engine API has not been used in project rgb-prd-gbl-core-1a2b before or it is disabled. ... \"reason\": \"SERVICE_DISABLED\"")

		return errors.New("exit status 1")
	}
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "An API enabled moments ago is not seen by every service yet, which Google says to wait out; the first apply of rgb's 1-org runs once more in 1m30s.\n") {
		t.Errorf("output:\n%s", out.String())
	}
	applies := 0
	for _, c := range f.runner.ran {
		if c.Args[0] == "apply" && filepath.Base(c.Dir) == "1-org" {
			applies++
		}
	}
	if applies != 2 {
		t.Errorf("1-org applied %d time(s), want 2", applies)
	}
}

// TestRunSeededBeforeFirstApply: the request's Seeded hook runs once the seed stands,
// with the organization's folder, and before any first apply, so the root organization's
// apply can set the folder's organization policy that 1-org's first apply runs under.
func TestRunSeededBeforeFirstApply(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	req := request(root, dir)
	var seededWith string
	var appliesBefore int
	req.Seeded = func(_ context.Context, organizationFolder string) error {
		seededWith = organizationFolder
		for _, c := range f.runner.ran {
			if c.Args[0] == "apply" {
				appliesBefore++
			}
		}

		return nil
	}
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), req, &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if want := f.google.Folders["organizations/123456789012|rgb"]; seededWith == "" || seededWith != want {
		t.Errorf("Seeded ran with folder %q, want the organization's folder %q", seededWith, want)
	}
	if appliesBefore != 0 {
		t.Errorf("%d apply(ies) ran before Seeded, want none", appliesBefore)
	}

	// A hook that fails stops the creation before its first applies.
	g := newFlow(t)
	dir, root = checkout(t)
	req = request(root, dir)
	req.Seeded = func(context.Context, string) error { return errors.New("the policy is refused") }
	out.Reset()
	if _, err := create.Run(t.Context(), g.clients(), req, &out); err == nil || !strings.Contains(err.Error(), "after the seed of rgb, before its first applies") || !strings.Contains(err.Error(), "the policy is refused") {
		t.Errorf("Run() error = %v, want the hook's refusal", err)
	}
	for _, c := range g.runner.ran {
		if c.Args[0] == "apply" {
			t.Errorf("an apply ran after the hook refused: %v", c.Args)
		}
	}
}

// TestRunOrganizedBeforePush: the request's Organized hook runs once 1-org's first apply
// has made the folders, with the placement that records them and each environment
// project's public-invoker tag, and before the push, so the root organization's apply can
// set the organization-policy baseline before the new organization's own apply trigger
// applies a layer; a hook that fails stops the creation before its push.
func TestRunOrganizedBeforePush(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	req := request(root, dir)
	var ran int
	var folders map[string]string
	var tags map[string]org.PublicInvokerTag
	var appliedBefore []string
	var pushedBefore bool
	req.Organized = func(_ context.Context, h *org.Placement) error {
		ran++
		folders, tags = maps.Clone(h.Folders), maps.Clone(h.PublicInvokerTags)
		for _, c := range f.runner.ran {
			if c.Args[0] == "apply" {
				appliedBefore = append(appliedBefore, filepath.Base(c.Dir))
			}
		}
		pushedBefore = f.pushed()

		return nil
	}
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), req, &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if ran != 1 {
		t.Fatalf("Organized ran %d time(s), want once", ran)
	}
	if want := []string{"0-bootstrap", "1-org"}; !slices.Equal(appliedBefore, want) {
		t.Errorf("the applies before Organized were %v, want %v", appliedBefore, want)
	}
	if want := map[string]string{"shared": "900000000011", "tst": "900000000012", "stg": "900000000013", "prd": "900000000014"}; !maps.Equal(folders, want) {
		t.Errorf("Organized ran with the folders %v, want the four 1-org made, %v", folders, want)
	}
	if want := (org.PublicInvokerTag{Key: "tagKeys/281000000001", Value: "tagValues/282000000001"}); len(tags) != 3 || tags["tst"] != want {
		t.Errorf("Organized ran with the public-invoker tags %v, want one for each environment, %v for tst", tags, want)
	}
	if pushedBefore {
		t.Error("the foundation was pushed before Organized ran")
	}
	if !f.pushed() {
		t.Error("the creation that finished pushed no foundation")
	}

	// A hook that fails stops the creation before its push.
	g := newFlow(t)
	dir, root = checkout(t)
	req = request(root, dir)
	req.Organized = func(context.Context, *org.Placement) error { return errors.New("the policy is refused") }
	out.Reset()
	if _, err := create.Run(t.Context(), g.clients(), req, &out); err == nil || !strings.Contains(err.Error(), "after 1-org's first apply of rgb, before its push") || !strings.Contains(err.Error(), "the policy is refused") {
		t.Errorf("Run() error = %v, want the hook's refusal", err)
	}
	if g.pushed() {
		t.Error("the foundation was pushed after the hook refused")
	}
}

// TestRunUntaintsProjects: the state of 1-org holds a project made and then errored on
// (tainted); the creation takes the taint off before the apply, so the apply goes on
// with the project instead of replacing it.
func TestRunUntaintsProjects(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	f.runner.tainted = `module.project["prd"].module.project-factory.google_project.main`
	var out bytes.Buffer
	if _, err := create.Run(t.Context(), f.clients(), request(root, dir), &out); err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "The project module.project[\"prd\"].module.project-factory.google_project.main was made and then errored on (Google's race on an API enabled moments ago); its taint comes off, so the apply goes on with it instead of replacing it.\n") {
		t.Errorf("output:\n%s", out.String())
	}
	untaints := 0
	for _, c := range f.runner.ran {
		if c.Args[0] == "untaint" && c.Args[2] == f.runner.tainted {
			untaints++
		}
	}
	if untaints == 0 {
		t.Errorf("no untaint among %v", f.runner.ran)
	}
}

// TestRunHandOffBranchLeft: a run that stopped between the hand-off branch and its pull
// request left the branch; the next run moves the branch to its own commit and opens the
// pull request from it, instead of refusing on the branch that exists.
func TestRunHandOffBranchLeft(t *testing.T) {
	t.Parallel()

	f := newFlow(t)
	dir, root := checkout(t)
	f.repo.Refs["refs/heads/create/rgb"] = github.Object{Type: "commit", SHA: "left9"}
	f.repo.Ancestry["left9"] = []string{"left9", baseSHA}
	f.repo.Trees["left9"] = "treeleft"
	var out bytes.Buffer
	result, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
	if err != nil {
		t.Fatalf("Run() error = %v; output:\n%s", err, out.String())
	}
	moved := f.repo.Refs["refs/heads/create/rgb"].SHA
	if moved == "left9" || moved == "" {
		t.Errorf("the branch stands at %q, want this run's hand-off commit", moved)
	}
	if len(f.repo.PullRequests) != 1 || f.repo.PullRequests[0].Head.Ref != "create/rgb" || result.PullRequest == "" {
		t.Errorf("pull requests %v, result %+v", f.repo.PullRequests, result)
	}
	if want := "Branch create/rgb stood at left9 from an earlier run; moved to the hand-off commit "; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}
}

// TestRunRefusals: a record whose boot project is not the listing's, a record billed to
// another account than the root organization's, and a seam that refuses a step, each stop
// the creation with the step named, and nothing after it runs. A record refused is
// refused before Google is asked anything: no folder is looked for and none is made.
func TestRunRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, f *flow, root *org.Placement, dir string)
		wantErr string
		// asksGoogle says the stop comes after the record is read: a record refused is
		// refused before Google is asked anything.
		asksGoogle bool
	}{
		{
			name: "the listing and the record disagree on the boot project",
			prepare: func(_ *testing.T, _ *flow, root *org.Placement, _ string) {
				root.HostingOrganization("rgb").BootProject = "rgb-boot-gbl-core-0000"
			},
			wantErr: "records boot project rgb-boot-gbl-core-e5f6 and placement.json's hostingOrganizations entry rgb-boot-gbl-core-0000: bedrock org create rgb brings them together",
		},
		{
			name: "the record names another billing account than the root organization's",
			prepare: func(t *testing.T, _ *flow, _ *org.Placement, dir string) {
				t.Helper()
				path := org.RecordPath(dir, "rgb")
				h, err := org.ReadPlacement(path)
				if err != nil {
					t.Fatal(err)
				}
				h.BillingAccount = "ABCDEF-012345-6789AB"
				if err := h.Write(path); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "organizations/rgb/placement.json names the billing account ABCDEF-012345-6789AB (billingAccount), and a hosting organization is billed to its root organization's billing account, 012345-6789AB-CDEF01, the one the creating identity administers: name 012345-6789AB-CDEF01, or take billingAccount out and run bedrock org create rgb again, which fills it in",
		},
		{
			name: "a folder that cannot be made",
			prepare: func(_ *testing.T, f *flow, _ *org.Placement, _ string) {
				f.google.Refuse["CreateFolder"] = errRefused
			},
			wantErr:    "refused by the stand-in",
			asksGoogle: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFlow(t)
			dir, root := checkout(t)
			tt.prepare(t, f, root, dir)
			var out bytes.Buffer
			_, err := create.Run(t.Context(), f.clients(), request(root, dir), &out)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Run() error = %v, want %q; Google was asked %v", err, tt.wantErr, f.google.Calls)
			}
			if !tt.asksGoogle && (len(f.google.Calls) != 0 || len(f.google.Identities) != 0) {
				t.Errorf("Google was opened as %v and asked %v, want a record refused before anything is asked", f.google.Identities, f.google.Calls)
			}
			if len(f.runner.ran) != 0 || len(f.srv.Created) != 0 || len(f.store.objects) != 0 {
				t.Errorf("the creation went on after the refusal: %v, %v, %v", f.runner.ran, f.srv.Created, f.store.objects)
			}
		})
	}
}

var errRefused = errorString("refused by the stand-in")

type errorString string

func (e errorString) Error() string {
	return string(e)
}

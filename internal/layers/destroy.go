// destroy.go is bedrock org destroy: a person deletes the organization's projects from
// their own computer, with their own sign-in, by destroying 1-org and then 0-bootstrap.

package layers

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/render"
)

// What the destroy runs and sets, and what it keeps.
const (
	BootstrapLayer = "0-bootstrap"
	destroyCommand = "destroy"
	// destroyFlag makes a plan the destroy's own: what tofu destroy would delete, read
	// and planned as the destroy does it, with nothing deleted.
	destroyFlag = "-destroy"
	// autoApprove is the flag the destroy's two tofu runs take: the prefix typed back
	// at the command is the approval.
	autoApprove = "-auto-approve"
	// destroyVar is the variable the layers that make projects and folders declare for
	// this command alone (0-bootstrap and 1-org): on its own
	// runs the projects' deletion policy becomes DELETE and the folders' deletion
	// protection comes off, recorded by an apply before the destroy, since the provider
	// reads both from the state.
	destroyVar = "destroy"
	// repositoryType is the resource type of the GitHub repositories 1-org declares, taken
	// out of the state before the destroy so that the repositories outlive the
	// organization; everything else 1-org made on GitHub goes. The public-invoker tag
	// keys and values, and the values' bindings, are taken out the same way: they live in
	// the environment projects and go with them, and Google refuses to delete a value
	// while a Cloud Run service in a project being deleted still binds it, a hold a
	// deleted project keeps for its thirty days, which no number of tries outlasts.
	repositoryType = "github_repository"
	tagKeyType     = "google_tags_tag_key"
	tagValueType   = "google_tags_tag_value"
	tagBindingType = "google_tags_tag_value_iam_member"
	// netProjectAddress is the network project in 1-org's state, which the destroy
	// excludes when the layer registers domains there (they cannot move to another
	// project); organizationFolderAddress is the organization's folder in 0-bootstrap's
	// state, kept with it since the network project's folder stays inside.
	netProjectAddress         = `module.project["net"]`
	organizationFolderAddress = "google_folder.organization"
	// netLayer is the network layer, destroyed on its own inside the network project when
	// the project is kept for its registrations, since the project's deletion would have
	// taken it; registrationType is the resource type of the registrations 2-net declares,
	// taken out of the state before that destroy: a registration cannot move to another
	// project and bedrock never deletes one, so it stays in the kept project, a person's to
	// transfer out or give up.
	netLayer         = "2-net"
	registrationType = "google_clouddomains_registration"
	// What the apply before the destroy targets, by their state addresses: every project
	// (the project-factory module's, under its key), every service of a project (the
	// module's too, its disabling on destroy) and every folder.
	projectSuffix = ".google_project.main"
	serviceMark   = ".google_project_service."
	folderPrefix  = "google_folder."
	// variablesFile is where a layer declares its variables; a checkout rendered by an
	// earlier bedrock declares no destroy variable, and the command refuses it.
	variablesFile = "variables.tf"
	// backendOverrideFile moves 0-bootstrap's state to the working copy before the destroy:
	// the state bucket is in the boot project and goes with it, so the destroy runs on a
	// local copy of the state, in a copy of the layer whose override file replaces the
	// bucket backend (an override's backend block takes precedence over the base's).
	backendOverrideFile = "backend_override.tf"
	backendOverride     = "terraform {\n  backend \"local\" {}\n}\n"
	stateFile           = "terraform.tfstate"
	// destroyAttempts is how many times each layer's destroy is tried: a folder refuses
	// for a while after its last project's deletion, and a tag value while a binding in
	// a project being deleted still holds it, and the next attempt gets past both.
	destroyAttempts = 6
	// DestroyPause is how long the command waits between two attempts.
	DestroyPause = 20 * time.Second
)

// layerFiles are the files a layer's working copy takes, by pattern: the configuration,
// the values and the provider lock. The OpenTofu version pin at the repository root goes
// with them, since a version manager reads it from the directory it runs in.
var layerFiles = []string{"*.tf", "*.tfvars", "*.tfvars.json", lockFile}

// DestroyRequest is what org destroy runs with: the checkout and its placement, where the
// GitHub token for the GitHub provider comes from (asked for only when 1-org's state
// holds something), whether the network project is kept (the layer registers domains
// there; 2-net is then destroyed inside it, on a copy rendered from the placement, once
// 1-org's checks and its plan have passed), what reads the network project and deletes
// its zones when it is not kept, and the pause between two attempts of a destroy. A root
// organization has no layers: bedrock org destroy deletes it by API (internal/root).
type DestroyRequest struct {
	Dir         string
	Placement   *org.Placement
	GithubToken func(ctx context.Context) (string, error)
	KeepNet     bool
	// CheckZones answers whether DeleteZones can do its work, and its refusals are the
	// destroy's preconditions: a placement that records no network project, and a network
	// project the person may not read. It runs as soon as 1-org's state is listed, before
	// anything of 1-org changes: no resource is out of the state yet and no protection is
	// lifted, so a destroy it refuses leaves 1-org as it stood.
	//
	// Both are asked for only when 1-org's state holds the network project and the
	// project is not kept. A project the state does not hold is not this destroy's to
	// delete (a destroy run earlier deleted it, its zones first, or 1-org never made it),
	// so no zone can go with it, and it is not read: Google answers a project that no
	// longer exists as it answers one the caller may not read, and a destroy picked up
	// after the project's thirty days would be refused for good.
	CheckZones func(ctx context.Context) error
	// DeleteZones deletes the network project's zones by API, ahead of the project: a
	// zone that goes with its project holds its name servers until the project purges,
	// thirty days later, one of the five sets Cloud DNS gives a name and its subdomains.
	// It runs right before the destroy that deletes the projects, after everything that
	// can refuse without deleting: the check above, the GitHub token, the state rm's, the
	// plan of the destroy and the apply that records the deletion policies. The plan
	// reads everything the destroy reads (tofu refreshes every resource the state holds,
	// where the apply refreshes only what it targets), so a read GitHub or Google refuses
	// the person stops the run with every zone standing. What only a deletion can show
	// still fails the destroy after it, on every attempt, with the zones gone and the
	// projects standing: a token that reads a ruleset or an Environment and may not
	// delete it, a folder that refuses past the attempts. The run again then finds no
	// zone to delete and goes on to the destroy. With KeepNet neither is asked for:
	// 2-net's destroy deletes the zones inside the kept project, and it runs where the
	// deletion by API would have: after 1-org's state list, the GitHub token, the state
	// rm's and the plan of 1-org's destroy, so a refusal or a failed plan leaves every
	// zone standing there too.
	DeleteZones func(ctx context.Context) error
	Pause       time.Duration
}

// Destroy deletes the organization's projects, as the person running the command: 1-org
// first, its GitHub repositories and its tags taken out of the state, then 0-bootstrap
// on a local copy of its state. In each layer an apply with destroy=true records the
// projects' DELETE policy and lifts the folders' protection, then the destroy runs, tried
// again after a pause while it fails. In 1-org the destroy is planned before that apply,
// and the network project's zones are deleted by API between the apply and the destroy,
// ahead of the project. A layer whose state holds nothing is skipped. With KeepNet the
// network project is excluded from 1-org's destroy and the organization's folder from
// 0-bootstrap's, and both stay for a person; 2-net is destroyed inside the kept project
// between the plan of 1-org's destroy and that apply, so nothing is destroyed, and no
// zone goes, before everything of 1-org that can refuse without deleting has passed.
func Destroy(ctx context.Context, exec deploy.Runner, req *DestroyRequest, out io.Writer) error {
	if exec == nil {
		return errors.New("no program runner is wired")
	}
	if !req.KeepNet && (req.CheckZones == nil || req.DeleteZones == nil) {
		return errors.New("no zone deletion is wired: the destroy deletes the network project's zones ahead of the project")
	}
	for _, layer := range []string{orgLayer, BootstrapLayer} {
		if err := checkDestroyVariable(req.Dir, layer); err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp("", "bedrock-destroy-")
	if err != nil {
		return errors.Wrap(err, "os.MkdirTemp()")
	}
	defer os.RemoveAll(work)
	planned, err := planOrgLayer(ctx, exec, req, work, out)
	if err != nil {
		return err
	}
	if req.KeepNet {
		if err := destroyNetLayer(ctx, exec, req, work, out); err != nil {
			return err
		}
	}
	if err := destroyOrgLayer(ctx, req, planned, out); err != nil {
		return err
	}

	return destroyBootstrapLayer(ctx, exec, req, work, out)
}

// destroyNetLayer destroys 2-net inside the network project kept for its registrations,
// so the project holds the registrations and nothing else after: its zones and records,
// the load balancer and its address, the certificates all go, as they would have with the
// project. It comes after 1-org's checks and the plan of its destroy (planOrgLayer), which
// change nothing in the cloud and leave what 2-net reads of 1-org, its outputs, in the
// state. It runs on a working copy of the layer rendered from the placement with
// zoneReplacement set, which writes the zones without prevent_destroy (2-net/README.md,
// "Making a zone again"), against the layer's state in the bucket, with the registrations
// taken out of the state first: bedrock never deletes a registration, and the domains
// registered there stop resolving until a zone stands elsewhere and each registration is
// pointed at it.
func destroyNetLayer(ctx context.Context, exec deploy.Runner, req *DestroyRequest, work string, out io.Writer) error {
	fmt.Fprintf(out, "=== destroy of %s, inside the network project kept for its registrations ===\n", netLayer)
	if req.Placement == nil {
		return errors.Newf("no placement is wired: %s is rendered from it for its destroy", netLayer)
	}
	dir := filepath.Join(work, netLayer)
	if err := renderNetForDestroy(req.Placement, req.Dir, dir); err != nil {
		return err
	}
	h := &handTofu{exec: exec, layer: netLayer, dir: dir, data: filepath.Join(work, netLayer+".data"), out: out}
	if err := h.init(ctx); err != nil {
		return err
	}
	addresses, err := h.stateList(ctx)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		fmt.Fprintf(out, "%s's state holds nothing: nothing to destroy there.\n", netLayer)

		return nil
	}
	for _, address := range addresses {
		if resourceType(address) != registrationType {
			continue
		}
		if err := h.run(ctx, "state", "rm", address); err != nil {
			return errors.Wrapf(err, "taking %s out of %s's state failed; the log says why", address, netLayer)
		}
		fmt.Fprintf(out, "Took %s out of %s's state: the registration stays in the kept project, a person's to transfer out or give up.\n", address, netLayer)
	}
	if err := h.destroy(ctx, nil, req.Pause); err != nil {
		return err
	}
	fmt.Fprintf(out, "The network project holds the registrations and nothing else now: %s's zones and records, load balancer and certificates are gone, and the domains registered there stop resolving until a zone stands elsewhere and each registration is pointed at it.\n", netLayer)

	return nil
}

// renderNetForDestroy renders 2-net into the directory from the placement with
// zoneReplacement set, so its zones carry no prevent_destroy there and nothing else
// differs from the checkout, and brings the layer's own files from the checkout beside
// the render (the values and the provider lock) with the OpenTofu version pin; the
// backend is the bucket's, as in the checkout, so the copy runs on the layer's state.
func renderNetForDestroy(p *org.Placement, checkout, into string) error {
	unguarded := *p
	unguarded.ZoneReplacement = true
	files, err := org.Render(&unguarded)
	if err != nil {
		return errors.Wrapf(err, "rendering %s for its destroy", netLayer)
	}
	if err := os.MkdirAll(into, 0o750); err != nil {
		return errors.Wrap(err, "os.MkdirAll()")
	}
	prefix := netLayer + "/"
	for _, f := range files {
		name, ok := strings.CutPrefix(f.Path, prefix)
		if !ok || f.Tier != render.Owned || strings.Contains(name, "/") {
			continue
		}
		if err := os.WriteFile(filepath.Join(into, name), f.Content, 0o600); err != nil {
			return errors.Wrapf(err, "os.WriteFile(): %s", f.Path)
		}
	}
	entries, err := os.ReadDir(filepath.Join(checkout, netLayer))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Wrap(err, "os.ReadDir()")
	}
	for _, e := range entries {
		if e.IsDir() || !layerFile(e.Name()) || strings.HasSuffix(e.Name(), ".tf") {
			continue
		}
		if err := copyFile(filepath.Join(checkout, netLayer, e.Name()), into, e.Name()); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(checkout, tofuVersionFile)); err == nil {
		if err := copyFile(filepath.Join(checkout, tofuVersionFile), into, tofuVersionFile); err != nil {
			return err
		}
	}

	return nil
}

// keptOut reports whether a resource of 1-org's state is taken out of it before the
// destroy, with what becomes of it: the GitHub repositories outlive the organization,
// and the tag keys, values and bindings go with the environment projects that hold them.
func keptOut(address string) (string, bool) {
	switch resourceType(address) {
	case repositoryType:
		return "the repository stays", true
	case tagKeyType, tagValueType, tagBindingType:
		return "it goes with the project that holds it", true
	}

	return "", false
}

// tagsNotice is said once when a tag resource was taken out of the state: the run cannot
// tell from the address whether the key or value sits in an environment project, where
// it goes with the project, or at the organization node, from a render before the tags
// moved into the environment projects, where it goes with nothing, so it says what to
// look for and what to do if one is there.
const tagsNotice = "If a tag key or value stands at the organization node (gcloud resource-manager tags keys list --parent organizations/<id>), from a render before the tags moved into the environment projects, it stays behind: delete it by hand (gcloud resource-manager tags values delete, then keys delete) once the projects it was bound to are purged, 30 days after their deletion; a key in an environment project goes with the project.\n"

// checkDestroyVariable refuses a layer that declares no destroy variable: a render of an
// earlier bedrock, whose projects and folders the apply before the destroy could not
// unprotect.
func checkDestroyVariable(dir, layer string) error {
	data, err := os.ReadFile(filepath.Join(dir, layer, variablesFile))
	if err != nil {
		return errors.Wrapf(err, "os.ReadFile(): %s's variables", layer)
	}
	if !strings.Contains(string(data), `variable "`+destroyVar+`"`) {
		return errors.Newf("%s/%s declares no %s variable, which bedrock org destroy sets to record the projects' deletion policy and lift the folders' protection: run bedrock org render with this bedrock first (the variable defaults to false, so no apply is needed before the destroy)", layer, variablesFile, destroyVar)
	}

	return nil
}

// plannedOrg is 1-org's destroy once everything of it that can refuse without deleting
// has passed: the tofu it runs with, the GitHub token in its environment, what its state
// held when it was listed, what its destroy excludes, and whether the run deletes the
// network project's zones by API ahead of the project.
type plannedOrg struct {
	h         *handTofu
	addresses []string
	excluded  []string
	zones     bool
}

// planOrgLayer is the opening of 1-org's destroy in the checkout, on its state in the
// bucket: everything that can refuse without deleting, and what changes least first. The
// check of the network project when its zones are deleted by API (nothing has changed by
// then), the GitHub token, the GitHub repositories and the tags taken out of the state,
// and the plan of the destroy, with the network project excluded when kept. Nothing in
// the cloud has changed when it answers: no protection is lifted and no zone is gone,
// whether the zones go by API or, the project kept, by 2-net's destroy, which runs after
// it. It answers nil for a state that holds nothing.
func planOrgLayer(ctx context.Context, exec deploy.Runner, req *DestroyRequest, work string, out io.Writer) (*plannedOrg, error) {
	layer := orgLayer
	h := &handTofu{exec: exec, layer: layer, dir: filepath.Join(req.Dir, layer), data: filepath.Join(work, layer+".data"), out: out, declaresDestroy: true}
	if req.KeepNet {
		fmt.Fprintf(out, "=== destroy of %s: its checks and its plan first, before %s's destroy deletes a zone ===\n", layer, netLayer)
	} else {
		fmt.Fprintf(out, "=== destroy of %s ===\n", layer)
	}
	if err := h.init(ctx); err != nil {
		return nil, err
	}
	addresses, err := h.stateList(ctx)
	if err != nil {
		return nil, err
	}
	if len(addresses) == 0 {
		fmt.Fprintf(out, "%s's state holds nothing: nothing to destroy there.\n", layer)

		return nil, nil
	}
	zones := !req.KeepNet && holdsNetProject(addresses)
	switch {
	case zones:
		if err := req.CheckZones(ctx); err != nil {
			return nil, err
		}
	case !req.KeepNet:
		fmt.Fprintf(out, "%s's state holds no network project (a destroy run earlier deleted it, its zones first, or %s never made it): this run reads none and deletes no zone by API.\n", layer, layer)
	}
	token, err := req.GithubToken(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "%s's GitHub provider needs a token for the repositories' rulesets and environments: the infrastructure GitHub App's installation token, or an organization owner's", layer)
	}
	h.env = append(h.env, githubEnv+"="+token)
	tags := false
	for _, address := range addresses {
		becomes, ok := keptOut(address)
		if !ok {
			continue
		}
		if err := h.run(ctx, "state", "rm", address); err != nil {
			return nil, errors.Wrapf(err, "taking %s out of %s's state failed; the log says why", address, layer)
		}
		fmt.Fprintf(out, "Took %s out of %s's state: %s.\n", address, layer, becomes)
		tags = tags || resourceType(address) != repositoryType
	}
	if tags {
		fmt.Fprint(out, tagsNotice)
	}
	planned := &plannedOrg{h: h, addresses: addresses, zones: zones}
	var ahead, spared string
	switch {
	case req.KeepNet:
		planned.excluded = append(planned.excluded, netProjectAddress)
		ahead = fmt.Sprintf(" and before %s's destroy deletes a zone", netLayer)
		spared = fmt.Sprintf(" and %s was not destroyed, so no zone was deleted", netLayer)
	case zones:
		ahead, spared = " and before a zone is deleted by API", " and no zone was deleted by API"
	}
	if err := h.planDestroy(ctx, planned.excluded, ahead, spared); err != nil {
		return nil, err
	}

	return planned, nil
}

// destroyOrgLayer is the closing of 1-org's destroy, what changes and deletes: the apply
// that records the deletion policies and lifts the folders' protection, the network
// project's zones deleted by API when the project goes, and the destroy. With the
// network project kept, 2-net's destroy has run between the plan and this, so the lines
// say where the run is again. A state that held nothing was skipped at the plan.
func destroyOrgLayer(ctx context.Context, req *DestroyRequest, planned *plannedOrg, out io.Writer) error {
	if planned == nil {
		return nil
	}
	if req.KeepNet {
		fmt.Fprintf(out, "=== destroy of %s, as planned above ===\n", planned.h.layer)
	}
	if err := planned.h.unprotect(ctx, planned.addresses, planned.excluded); err != nil {
		return err
	}
	if planned.zones {
		if err := req.DeleteZones(ctx); err != nil {
			return err
		}
	}

	return planned.h.destroy(ctx, planned.excluded, req.Pause)
}

// holdsNetProject reports whether 1-org's state holds the network project itself, which
// the layer's destroy then deletes: the project resource under the project's module, and
// not what else the module holds.
func holdsNetProject(addresses []string) bool {
	return slices.ContainsFunc(addresses, func(address string) bool {
		return strings.HasPrefix(address, netProjectAddress+".") && strings.HasSuffix(address, projectSuffix)
	})
}

// destroyVariable is the destroy variable set to true, for a layer that declares it, and
// nothing for one that does not.
func (h *handTofu) destroyVariable() []string {
	if !h.declaresDestroy {
		return nil
	}

	return []string{"-var", destroyVar + "=true"}
}

// destroyBootstrapLayer destroys 0-bootstrap on a working copy of the layer that holds a
// local copy of its state, pulled from the bucket first: the bucket is in the boot project
// and goes with it.
func destroyBootstrapLayer(ctx context.Context, exec deploy.Runner, req *DestroyRequest, work string, out io.Writer) error {
	fmt.Fprintf(out, "=== destroy of %s ===\n", BootstrapLayer)
	bucket := &handTofu{exec: exec, layer: BootstrapLayer, dir: filepath.Join(req.Dir, BootstrapLayer), data: filepath.Join(work, BootstrapLayer+".data"), out: out, declaresDestroy: true}
	if err := bucket.init(ctx); err != nil {
		return err
	}
	state, err := bucket.output(ctx, "state", "pull")
	if err != nil {
		return errors.Wrapf(err, "pulling %s's state failed; the log says why", BootstrapLayer)
	}
	local := &handTofu{exec: exec, layer: BootstrapLayer, dir: filepath.Join(work, BootstrapLayer), out: out, declaresDestroy: true}
	if err := copyLayer(req.Dir, BootstrapLayer, local.dir, state); err != nil {
		return err
	}
	fmt.Fprintf(out, "Copied %s's state and files to %s: the state bucket goes with the boot project, so the destroy runs on the copy.\n", BootstrapLayer, local.dir)
	if err := local.init(ctx); err != nil {
		return err
	}
	addresses, err := local.stateList(ctx)
	if err != nil {
		return err
	}
	if len(addresses) == 0 {
		fmt.Fprintf(out, "%s's state holds nothing: nothing to destroy there.\n", BootstrapLayer)

		return nil
	}
	var excluded []string
	if req.KeepNet {
		excluded = append(excluded, organizationFolderAddress)
	}
	if err := local.unprotect(ctx, addresses, excluded); err != nil {
		return err
	}

	return local.destroy(ctx, excluded, req.Pause)
}

// copyLayer copies the layer's files and the repository's OpenTofu version pin into the
// directory, with the state and the override file that makes the state local.
func copyLayer(dir, layer, into string, state []byte) error {
	if err := os.MkdirAll(into, 0o750); err != nil {
		return errors.Wrap(err, "os.MkdirAll()")
	}
	entries, err := os.ReadDir(filepath.Join(dir, layer))
	if err != nil {
		return errors.Wrap(err, "os.ReadDir()")
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && layerFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	for _, name := range names {
		if err := copyFile(filepath.Join(dir, layer, name), into, name); err != nil {
			return err
		}
	}
	if _, err := os.Stat(filepath.Join(dir, tofuVersionFile)); err == nil {
		if err := copyFile(filepath.Join(dir, tofuVersionFile), into, tofuVersionFile); err != nil {
			return err
		}
	}
	if err := os.WriteFile(filepath.Join(into, backendOverrideFile), []byte(backendOverride), 0o600); err != nil {
		return errors.Wrap(err, "os.WriteFile(): the backend override")
	}
	if err := os.WriteFile(filepath.Join(into, stateFile), state, 0o600); err != nil {
		return errors.Wrap(err, "os.WriteFile(): the state")
	}

	return nil
}

// layerFile reports whether a file of a layer's directory is one the working copy takes.
func layerFile(name string) bool {
	for _, pattern := range layerFiles {
		if ok, _ := filepath.Match(pattern, name); ok {
			return true
		}
	}

	return false
}

// copyFile copies the file into the directory under the name, inside that directory
// whatever the name spells.
func copyFile(from, into, name string) error {
	data, err := os.ReadFile(from)
	if err != nil {
		return errors.Wrap(err, "os.ReadFile()")
	}
	root, err := os.OpenRoot(into)
	if err != nil {
		return errors.Wrap(err, "os.OpenRoot()")
	}
	defer root.Close()
	f, err := root.Create(filepath.Base(name))
	if err != nil {
		return errors.Wrap(err, "os.Root.Create()")
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()

		return errors.Wrap(err, "os.File.Write()")
	}
	if err := f.Close(); err != nil {
		return errors.Wrap(err, "os.File.Close()")
	}

	return nil
}

// resourceType is the type of the resource a state address names: the part before the
// name, after the modules.
func resourceType(address string) string {
	if i := strings.LastIndex(address, "module."); i >= 0 {
		rest := address[i:]
		if _, after, ok := strings.Cut(rest[len("module."):], "."); ok {
			address = after
		}
	}
	typ, _, _ := strings.Cut(address, ".")

	return typ
}

// unprotect applies destroy=true to every project, every project service and every
// folder in the state but the excluded ones: the provider reads a project's deletion
// policy, a service's disabling on destroy and a folder's deletion protection from the
// state, so the destroy needs them recorded first. Nothing else is targeted, so the
// apply changes nothing else.
func (h *handTofu) unprotect(ctx context.Context, addresses, excluded []string) error {
	args := append([]string{applyCommand, noInput, noColor, autoApprove}, h.destroyVariable()...)
	var projects, services, folders int
	for _, address := range addresses {
		switch {
		case strings.HasSuffix(address, projectSuffix):
			projects++
		case strings.Contains(address, serviceMark):
			services++
		case strings.HasPrefix(address, folderPrefix):
			folders++
		default:
			continue
		}
		if slices.ContainsFunc(excluded, func(e string) bool { return strings.HasPrefix(address, e) }) {
			continue
		}
		args = append(args, "-target="+address)
	}
	if projects+services+folders == 0 {
		return nil
	}
	fmt.Fprintf(h.out, "Recording on %s that the destroy deletes %d project(s) with %d service(s) and %d folder(s), disabling no API first.\n", h.layer, projects, services, folders)
	if err := h.run(ctx, args...); err != nil {
		return errors.Wrapf(err, "the apply recording %s's deletion policies failed; the log says why", h.layer)
	}

	return nil
}

// planDestroy plans the layer's destroy with the arguments the destroy runs with, and
// deletes nothing. It runs once the GitHub repositories and the tags are out of the state
// (a repository carries prevent_destroy, and tofu refuses to plan its destruction), so it
// is the plan the destroy itself makes first: every resource the state holds is read,
// with the same GitHub token and sign-in. A read that is refused, as GitHub refuses a
// token that does not reach a repository's rulesets or its Environments, stops the run
// here, where the destroy would have been refused it on every attempt after the zones
// were gone. ahead and spared name what else the plan comes before and a failed plan
// spares, after the protection: the zones this run deletes by API after it, or, the
// network project kept, the zones 2-net's destroy deletes after it.
func (h *handTofu) planDestroy(ctx context.Context, excluded []string, ahead, spared string) error {
	args := make([]string, 0, 6+len(excluded))
	args = append(args, planCommand, destroyFlag, noInput, noColor)
	args = append(args, h.destroyVariable()...)
	for _, address := range excluded {
		args = append(args, "-exclude="+address)
	}
	fmt.Fprintf(h.out, "Planning the destroy of %s first, which deletes nothing: the plan reads everything the destroy reads, with the same GitHub token and sign-in, so a read GitHub or Google refuses stops the destroy here, before the projects' and folders' protection is lifted%s.\n", h.layer, ahead)
	if err := h.run(ctx, args...); err != nil {
		return errors.Wrapf(err, "the plan of %s's destroy failed, so nothing of %s was destroyed: no protection was lifted%s; what was taken out of its state above (the GitHub repositories, the tags) stays out of it, and the destroy run again goes on from there; the log says why", h.layer, h.layer, spared)
	}

	return nil
}

// destroy destroys the layer but the excluded addresses, tried again after the pause
// while it fails, up to the attempts.
func (h *handTofu) destroy(ctx context.Context, excluded []string, pause time.Duration) error {
	args := make([]string, 0, 6+len(excluded))
	args = append(args, destroyCommand, noInput, noColor, autoApprove)
	args = append(args, h.destroyVariable()...)
	for _, address := range excluded {
		args = append(args, "-exclude="+address)
	}
	for attempt := 1; ; attempt++ {
		err := h.run(ctx, args...)
		if err == nil {
			fmt.Fprintf(h.out, "Destroyed %s.\n", h.layer)

			return nil
		}
		if attempt == destroyAttempts {
			return errors.Wrapf(err, "the destroy of %s failed %d times; the log says why", h.layer, attempt)
		}
		fmt.Fprintf(h.out, "The destroy of %s failed (attempt %d of %d): a folder refuses for a while after its last project's deletion, and a tag value while a binding in a project being deleted holds it; trying again in %s.\n", h.layer, attempt, destroyAttempts, pause)
		select {
		case <-ctx.Done():
			return errors.Wrap(ctx.Err(), "waiting to try the destroy again")
		case <-time.After(pause):
		}
	}
}

// resume.go is a creation run again after its push. From the push on the creating
// identity takes its own grants off, one removal after another, and what the steps before
// the push did as that identity (the seed's reads of the boot project, both first
// applies) needs the grants the withdrawal takes: run again, they would be refused, and
// only a person granting the identity by hand would get the creation past them. So a run
// that finds its foundation pushed does none of them again. It reads what the creation
// made from the placement at the commit the creation recorded before its push (the
// record of the push in the root organization's state bucket, whose commit the new
// repository's default branch must hold), which it holds to the organization's record,
// finds the two folders by their names and holds them to the ids the push recorded, takes
// off what the withdrawal left, and ends as every creation does: the marker, and the
// hand-off. A push no record names (a bedrock that recorded none made it) is read at the
// branch's head instead, where a commit after the push may have changed the placement, so
// the folders and projects it records are held to what stands under the organization's
// folder while the identity can still tell. The first applies are the new organization's
// own from the push on, which its apply trigger runs as its own identities.

package create

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/go-playground/errors/v5"
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// seededTfvars is the pushed file that names the two folders the creation's seed made.
const seededTfvars = bootstrapLayer + "/terraform.tfvars"

// The answers of a comparison that say the branch holds the commit: it is the branch's
// head, or the branch is ahead of it.
const (
	statusIdentical = "identical"
	statusAhead     = "ahead"
)

// foundation is the creation's push as a run again reads it: the commit, the placement at
// that commit, which records what the creation made before its push, and whether the
// record of the push names the commit. One no record names is the branch's head.
type foundation struct {
	commit   string
	recorded bool
	placed   *org.Placement
}

// where names the place a file of the push is read at, for the sentences that name it:
// the commit the creation recorded, or the branch.
func (p *foundation) where(h *org.Placement) string {
	if p.recorded {
		return "at " + short(p.commit) + " of " + h.GithubOrganization + "/" + h.SourceRepo
	}

	return "on " + h.GithubDefaultBranch + " of " + h.GithubOrganization + "/" + h.SourceRepo
}

// pushed reads whether an earlier run of this creation pushed the foundation, and answers
// the push; none when nothing was pushed.
//
// The creation records its foundation commit before the branch moves to it (recordPush).
// A branch that holds the commit recorded carries the push, and the placement is read at
// that commit: a commit after it changes nothing of what the run again reads. A branch
// that does not hold it carries no push of this creation when it carries no placement of
// this organization either: the run stopped between the record and the branch's move, and
// a repository that does not stand yet, or one without the branch the placement names,
// says the same. One that does carry the placement was rewritten since the push, or
// carries a file that is not the creation's, and is refused: nothing on it is the
// creation's own word for what it made.
//
// A push no record names (a bedrock that recorded none made it; an earlier organization's
// record names none of this creation's) is known by the placement on the branch
// (Foundation) and read at the branch's head, which may be a later commit that changed
// the placement: the run again holds it to the organization's record (differs) and to
// what Google answers under the organization's folder (beneath).
func (f *creation) pushed(ctx context.Context, h *org.Placement) (*foundation, error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	commit, err := f.pushRecorded(ctx)
	if err != nil {
		return nil, err
	}
	held, err := f.holds(ctx, h, commit)
	if err != nil {
		return nil, err
	}
	push := &foundation{commit: commit, recorded: true}
	if !held {
		head, carries, _, err := Foundation(ctx, f.gh, h)
		switch {
		case github.NotFound(err), github.Moved(err):
			return nil, nil
		case err != nil:
			return nil, err
		case !carries:
			return nil, nil
		case commit != "":
			record := "gs://" + f.req.Root.StateBucket + "/" + pushObject(h.Prefix)

			return nil, errors.Newf("%s of %s/%s carries this organization's %s and does not hold %s, the foundation commit this creation recorded before its push (%s): the branch was rewritten since, or what it carries is not this creation's push, so nothing on it is taken as what the creation made, nothing is taken off and nothing is recorded: the branch is put back so that it holds the commit, and this creation run again; when the commit is gone for good, the record is deleted (gcloud storage rm %s), and the run again then reads what the branch carries and holds it to what Google answers under the organization's folder", branch, owner, repo, org.PlacementFile, short(commit), record, record)
		}
		push = &foundation{commit: head}
	}
	data, err := f.gh.Contents(ctx, owner, repo, org.PlacementFile, push.commit)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s at %s of %s/%s", org.PlacementFile, short(push.commit), owner, repo)
	}
	push.placed = &org.Placement{}
	if err := json.Unmarshal(data, push.placed); err != nil {
		return nil, errors.Wrapf(err, "json.Unmarshal(): %s %s", org.PlacementFile, push.where(h))
	}

	return push, nil
}

// pushRecorded reads the commit this creation recorded before its push, from the root
// organization's state bucket; none when no record stands, or when the record is an
// earlier organization's by this prefix, destroyed and started over, which names another
// boot project: that organization's commit is in the branch's history when the new one
// starts over in the same repository, and is not this creation's push.
func (f *creation) pushRecorded(ctx context.Context) (string, error) {
	object := pushObject(f.req.Record.Prefix)
	data, err := f.clients.Store.Read(ctx, f.req.Root.StateBucket, object)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var record pushRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return "", errors.Wrapf(err, "json.Unmarshal(): the record of the push gs://%s/%s", f.req.Root.StateBucket, object)
	}
	if record.BootProject != f.req.Record.BootProject {
		return "", nil
	}

	return record.Commit, nil
}

// holds reports whether the new repository's default branch holds the commit: it is the
// branch's head, or behind it. No commit is held by none; neither is one the repository
// does not know, nor any by a repository or a branch that does not stand.
func (f *creation) holds(ctx context.Context, h *org.Placement, commit string) (bool, error) {
	if commit == "" {
		return false, nil
	}
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	comparison, err := f.gh.Compare(ctx, owner, repo, commit, branch)
	switch {
	case github.NotFound(err), github.Moved(err):
		return false, nil
	case err != nil:
		return false, errors.Wrapf(err, "comparing %s with %s of %s/%s", short(commit), branch, owner, repo)
	}

	return comparison.Status == statusIdentical || comparison.Status == statusAhead, nil
}

// resume finishes a creation that stopped after its push. The pushed placement is held to
// the organization's record first (differs), and what it records goes into the
// placement: the projects, the folders, the tags and the zone. The organization's folder
// and its terraform folder are found by their names, by the identity's Folder Creator at
// the organization node, which the withdrawal does not touch, and held to the ids the
// push recorded (seeded). A placement read at the branch's head, for a push no record
// names, is held to what stands under the organization's folder (beneath); one read at
// the commit the creation recorded is what the creation made. What the withdrawal left is
// taken off (withdrawLeft), and the marker and the hand-off follow.
func (f *creation) resume(ctx context.Context, g Google, h *org.Placement, push *foundation) (*Result, error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	record, where := "gs://"+f.req.Root.StateBucket+"/"+pushObject(h.Prefix), push.where(h)
	if differences := differs(h, push.placed); len(differences) > 0 {
		if push.recorded {
			return nil, errors.Newf("%s %s, which %s names as this creation's push, is not what this creation's push records (%s): the creation cannot go on from it", org.PlacementFile, where, record, strings.Join(differences, ", "))
		}

		return nil, errors.Newf("%s %s carries this organization's state bucket and is not what this creation's push records (%s): the creation cannot go on from it", org.PlacementFile, where, strings.Join(differences, ", "))
	}
	placed := push.placed
	h.Projects, h.ProjectNumbers = placed.Projects, placed.ProjectNumbers
	h.Folders, h.PublicInvokerTags, h.AppsZone = placed.Folders, placed.PublicInvokerTags, placed.AppsZone
	if push.recorded {
		fmt.Fprintf(f.out, "%s of %s/%s holds the foundation commit this creation recorded before its push (%s, %s): an earlier run of this creation pushed it and stopped. Nothing before the push is done again (the first applies are the organization's own from that push on, which its apply trigger runs), and the creation goes on from there: what it made is read at that commit, whatever the branch has taken since.\n", branch, owner, repo, short(push.commit), record)
	} else {
		fmt.Fprintf(f.out, "%s of %s/%s carries the foundation already (%s): an earlier run of this creation pushed it and stopped. Nothing before the push is done again (the first applies are the organization's own from that push on, which its apply trigger runs), and the creation goes on from there.\n", branch, owner, repo, short(push.commit))
	}
	recorded, err := f.seeded(ctx, h, push)
	if err != nil {
		return nil, err
	}
	seed := &org.Seed{}
	if seed.OrganizationFolder, err = f.found(ctx, g, where, "organizations/"+h.OrganizationID, h.Prefix, organizationFolderVariable, recorded.OrganizationFolder); err != nil {
		return nil, err
	}
	if seed.TerraformFolder, err = f.found(ctx, g, where, "folders/"+seed.OrganizationFolder, terraformFolderName, terraformFolderVariable, recorded.TerraformFolder); err != nil {
		return nil, err
	}
	if !push.recorded {
		if err := f.beneath(ctx, g, seed, h); err != nil {
			return nil, err
		}
	}
	if err := f.withdrawLeft(ctx, g, seed, h); err != nil {
		return nil, err
	}

	return f.conclude(ctx, h, seed, owner+"/"+repo, push.commit)
}

// differs is what the pushed placement lacks or names otherwise than the record, each as
// its place in the file: the boot project, which the record names; its number; each
// project 1-org makes; and each of the four folders 1-org makes. A run again goes on from
// its own push alone: a project or a folder the withdrawal passed over would stand under
// a marker that says the identity holds nothing, and another boot project would go into
// the marker, which later runs read as an earlier organization's.
func differs(h, placed *org.Placement) []string {
	var differences []string
	for _, key := range h.ProjectKeys() {
		switch {
		case placed.Projects[key] == "":
			differences = append(differences, "projects."+key+" is missing")
		case key == bootKey && placed.Projects[key] != h.Projects[key]:
			differences = append(differences, "projects."+key+" is "+placed.Projects[key]+" where the record names "+h.Projects[key])
		}
	}
	if placed.ProjectNumbers[bootKey] == "" {
		differences = append(differences, "projectNumbers."+bootKey+" is missing")
	}
	for _, key := range append([]string{sharedFolderKey}, org.Environments...) {
		if placed.Folders[key] == "" {
			differences = append(differences, "folders."+key+" is missing")
		}
	}

	return differences
}

// seeded reads the two folders the push recorded: the pushed 0-bootstrap/terraform.tfvars
// names the organization's folder and its terraform folder by their ids, as the creation's
// seed made them. It is read where the placement was: at the commit the creation
// recorded, or at the branch's head. A file that names neither is not this creation's
// push.
func (f *creation) seeded(ctx context.Context, h *org.Placement, push *foundation) (*org.Seed, error) {
	owner, repo, where := h.GithubOrganization, h.SourceRepo, push.where(h)
	src, err := f.gh.Contents(ctx, owner, repo, seededTfvars, push.commit)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s at %s of %s/%s", seededTfvars, short(push.commit), owner, repo)
	}
	file, diags := hclsyntax.ParseConfig(src, seededTfvars, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, errors.Wrapf(diags, "hclsyntax.ParseConfig(): %s %s", seededTfvars, where)
	}
	attributes, diags := file.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, errors.Wrapf(diags, "hcl.Body.JustAttributes(): %s %s", seededTfvars, where)
	}
	recorded := &org.Seed{}
	for _, folder := range []struct {
		variable string
		id       *string
	}{{organizationFolderVariable, &recorded.OrganizationFolder}, {terraformFolderVariable, &recorded.TerraformFolder}} {
		attribute, ok := attributes[folder.variable]
		if !ok {
			return nil, errors.Newf("%s %s carries no %s, which the creation's push records: the creation cannot go on from it", seededTfvars, where, folder.variable)
		}
		value, diags := attribute.Expr.Value(nil)
		if diags.HasErrors() || value.IsNull() || value.Type() != cty.String {
			return nil, errors.Newf("%s in %s %s is not a folder's id as the creation's push records it: the creation cannot go on from it", folder.variable, seededTfvars, where)
		}
		*folder.id = value.AsString()
	}

	return recorded, nil
}

// found is the folder named under the parent, which the creation made before its push,
// when it is the one the push recorded under the variable, in the file read at the place
// where names. One that is not there stops the creation, which makes nothing again after
// its push; so does one that is another folder of the name, whose policy is not this
// creation's to change.
func (f *creation) found(ctx context.Context, g Google, where, parent, name, variable, recorded string) (string, error) {
	id, err := g.FindFolder(ctx, parent, name)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.Newf("the foundation of %s is pushed, and the creating identity finds no folder %s under %s, which this creation made before its push: the creation goes on from the push only with the folders it made", f.req.Record.Prefix, name, parent)
	}
	if id != recorded {
		return "", errors.Newf("the creating identity finds folder %s by the name %s under %s, and %s %s records %s %s: the folder found is not the one this creation made, so nothing is taken off it and nothing is recorded", id, name, parent, seededTfvars, where, variable, recorded)
	}

	return id, nil
}

// asks are the permissions the run again asks Google about for the removal: reading and
// setting the policy, and, on the organization's folder, what tells the two roles there
// that carry neither.
func (r removal) asks() []string {
	if r.project != "" {
		return []string{projectGetPolicyPermission, projectSetPolicyPermission}
	}
	asks := []string{folderGetPolicyPermission, folderSetPolicyPermission}
	if slices.Contains(r.roles, bootProjectRoleCreator) {
		asks = append(asks, projectCreatePermission, tagKeyCreatePermission)
	}

	return asks
}

// takes reports whether the removal is the caller's own to make, from what it holds: the
// permission that sets the policy.
func (r removal) takes(held []string) bool {
	if r.project != "" {
		return slices.Contains(held, projectSetPolicyPermission)
	}

	return slices.Contains(held, folderSetPolicyPermission)
}

// beneath refuses a placement on the branch that is not the one this creation pushed, as
// far as Google can tell. It is asked for a push no record names alone: the run again
// then reads the placement at the branch's head, and a commit after the push may have
// changed a folder's or a project's id there. Asked about an id that is not the
// creation's, Google answers that the identity holds nothing; the withdrawal would pass
// over what the creation made, and the marker and the closing line would say the identity
// holds nothing while its grants stand there.
//
// While the identity may still set the policy of the organization's folder, what it holds
// there (its Folder Admin) reaches every folder and project beneath, so every one the
// creation made answers. One the placement records that answers nothing is then not
// under the folder, and the run stops before anything is taken off. Once the identity may
// not set that policy (the withdrawal's last removal was made, or a person took its
// Folder Admin off), nothing tells an id changed since from a folder or a project the
// withdrawal is done with, and the placement is taken as it stands.
func (f *creation) beneath(ctx context.Context, g Google, seed *org.Seed, h *org.Placement) error {
	all := removals(seed, h, h.Folders)
	organization := all[len(all)-1]
	held, err := g.Held(ctx, organization.resource(), organization.asks())
	if err != nil {
		return err
	}
	if !organization.takes(held) {
		return nil
	}
	places := map[string]string{}
	for key, id := range h.Folders {
		places["folder "+id] = "folders." + key
	}
	for key, id := range h.Projects {
		places["project "+id] = "projects." + key
	}
	var silent []string
	for _, r := range all[:len(all)-1] {
		held, err := g.Held(ctx, r.resource(), r.asks())
		if err != nil {
			return err
		}
		if len(held) > 0 {
			continue
		}
		name := r.String()
		if place, ok := places[name]; ok {
			name += " (" + place + ")"
		}
		silent = append(silent, name)
	}
	if len(silent) == 0 {
		return nil
	}

	return errors.Newf("the creating identity %s may still set the policy of the organization's folder %s, so what it holds there reaches every folder and project beneath it, and Google answers that it holds nothing on %s, which %s on %s of %s/%s records: what the file records there is not under the organization's folder, so a commit after this creation's push changed the file, or what the creation made was moved or deleted since; a withdrawal that passed over it would record that the identity holds nothing while a grant of its own may still stand, so nothing is taken off and nothing is recorded: a file changed since is put back as the creation's push left it, and this creation run again", f.req.Root.CreatingIdentity(), seed.OrganizationFolder, strings.Join(silent, ", "), org.PlacementFile, h.GithubDefaultBranch, h.GithubOrganization, h.SourceRepo)
}

// withdrawLeft takes off what the withdrawal left, deciding at every folder and project
// the withdrawal covers, in its order, from what Google answers the identity holds there
// (Held, which Google answers a caller that holds nothing). The placement it is given
// records what the creation made: the one at the commit the creation recorded before its
// push, or, for a push no record names, the one on the branch held to what stands under
// the organization's folder first (beneath).
//
// Where it holds none of the permissions asked, no role of the withdrawal stands there for
// it, and nothing is read: the identity may read no policy there any longer. Where it may
// set the policy, the removal is made again; one made already reads the policy and writes
// nothing. While the withdrawal's own order holds, those are the only two answers: the
// identity's roles on the organization's folder come off last, in one write, and its
// Folder Admin there reaches every folder and project beneath.
//
// Where it holds something and may not set the policy, that order was broken by other
// means than this creation (a person took its Folder Admin off), and what it still holds
// there is not its own to take off. The run then stops, after everything that is its own
// is off, with nothing recorded, and names every folder and project that still answers,
// the organization's folder first: a person who took the identity out of its roles on
// that folder alone would be refused again at the next one. A role on a folder answers
// for the folders and projects beneath it too, so one named may carry no role of its own,
// which the refusal says.
func (f *creation) withdrawLeft(ctx context.Context, g Google, seed *org.Seed, h *org.Placement) error {
	member := f.req.Root.CreatingIdentityMember()
	all := removals(seed, h, h.Folders)
	var took, stands []string
	for _, r := range all {
		held, err := g.Held(ctx, r.resource(), r.asks())
		if err != nil {
			return err
		}
		switch {
		case len(held) == 0:
			// No role of the withdrawal stands here for the identity.
		case r.takes(held):
			if err := r.take(ctx, g, member); err != nil {
				return err
			}
			took = append(took, r.String())
		case r.folder == seed.OrganizationFolder:
			stands = slices.Insert(stands, 0, strings.Join(held, ", ")+" on "+r.String())
		default:
			stands = append(stands, strings.Join(held, ", ")+" on "+r.String())
		}
	}
	standing := strings.Join(stands, "; ")
	switch {
	case len(took) == len(all):
		f.withdrew(seed, h, h.Folders, all)
	case len(took) > 0:
		fmt.Fprintf(f.out, "Took what was left of the creating identity's grants off %d of the %d folders and projects its withdrawal covers: %s.\n", len(took), len(all), strings.Join(took, ", "))
	case standing == "":
		fmt.Fprintf(f.out, "The creating identity holds nothing on the %d folders and %d projects its withdrawal covers any longer, as Google answers for each: the withdrawal ended in the run that stopped, and nothing is left to take off.\n", len(h.Folders)+2, len(all)-len(h.Folders)-2)
	}
	if standing != "" {
		return errors.Newf("the creating identity %s still holds %s and may not set the policy there, so the roles it holds there are not its own to take off, which this creation's withdrawal never leaves (a grant of the identity's went by other means): whoever administers folder %s, as the break-glass account %s does, takes the identity out of its roles on every folder and project named here (a role on a folder answers for the folders and projects beneath it too, so one named here may carry no role of its own), and the next run of this creation then finishes; nothing is recorded until then", f.req.Root.CreatingIdentity(), standing, seed.OrganizationFolder, h.BreakGlassAccount)
	}

	return nil
}

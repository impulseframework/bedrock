// resume.go is a creation run again after its push. From the push on the creating
// identity takes its own grants off, one removal after another, and what the steps before
// the push did as that identity (the seed's reads of the boot project, both first
// applies) needs the grants the withdrawal takes: run again, they would be refused, and
// only a person granting the identity by hand would get the creation past them. So a run
// that finds the foundation on the new repository's default branch does none of them
// again. It reads what the creation made from the placement on the branch, which it holds
// to the record, finds the two folders by their names and holds them to the ids the push
// recorded, holds the folders and projects the placement records to what stands under
// the organization's folder while the identity can still tell, takes off what the
// withdrawal left, and ends as every creation does: the marker, and the hand-off. The
// first applies are the new organization's own from the push on, which its apply trigger
// runs as its own identities.

package create

import (
	"context"
	"encoding/json"
	"fmt"
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

// pushed reads whether an earlier run of this creation pushed the foundation: the new
// repository's default branch carries this organization's placement (Foundation). It
// answers the branch's head and the placement on it, which records what the creation made
// before its push; no placement when nothing was pushed, which a repository that does not
// stand yet, or one without the branch the placement names, says too. The record names no
// commit as the creation's push, so the head may be a later commit that changed the
// placement: the run again holds it to the record (differs) and to what Google answers
// under the organization's folder (beneath).
func (f *creation) pushed(ctx context.Context, h *org.Placement) (head string, placed *org.Placement, err error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	head, carries, _, err := Foundation(ctx, f.gh, h)
	switch {
	case github.NotFound(err), github.Moved(err):
		return "", nil, nil
	case err != nil:
		return "", nil, err
	case !carries:
		return "", nil, nil
	}
	data, err := f.gh.Contents(ctx, owner, repo, org.PlacementFile, head)
	if err != nil {
		return "", nil, errors.Wrapf(err, "reading %s at %s of %s/%s", org.PlacementFile, short(head), owner, repo)
	}
	placed = &org.Placement{}
	if err := json.Unmarshal(data, placed); err != nil {
		return "", nil, errors.Wrapf(err, "json.Unmarshal(): %s on %s of %s/%s", org.PlacementFile, branch, owner, repo)
	}

	return head, placed, nil
}

// resume finishes a creation that stopped after its push. The pushed placement is held to
// the record first (differs), and what it records goes into the placement: the projects,
// the folders, the tags and the zone. The organization's folder and its terraform folder
// are found by their names, by the identity's Folder Creator at the organization node,
// which the withdrawal does not touch, and held to the ids the push recorded (seeded).
// What the withdrawal left is taken off (withdrawLeft), from a placement whose folders
// and projects stand under the organization's folder (beneath), and the marker and the
// hand-off follow.
func (f *creation) resume(ctx context.Context, g Google, h *org.Placement, head string, placed *org.Placement) (*Result, error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	if differences := differs(h, placed); len(differences) > 0 {
		return nil, errors.Newf("%s on %s of %s/%s carries this organization's state bucket and is not what this creation's push records (%s): the creation cannot go on from it", org.PlacementFile, branch, owner, repo, strings.Join(differences, ", "))
	}
	h.Projects, h.ProjectNumbers = placed.Projects, placed.ProjectNumbers
	h.Folders, h.PublicInvokerTags, h.AppsZone = placed.Folders, placed.PublicInvokerTags, placed.AppsZone
	fmt.Fprintf(f.out, "%s of %s/%s carries the foundation already (%s): an earlier run of this creation pushed it and stopped. Nothing before the push is done again (the first applies are the organization's own from that push on, which its apply trigger runs), and the creation goes on from there.\n", branch, owner, repo, short(head))
	recorded, err := f.seeded(ctx, h, head)
	if err != nil {
		return nil, err
	}
	seed := &org.Seed{}
	if seed.OrganizationFolder, err = f.found(ctx, g, h, "organizations/"+h.OrganizationID, h.Prefix, organizationFolderVariable, recorded.OrganizationFolder); err != nil {
		return nil, err
	}
	if seed.TerraformFolder, err = f.found(ctx, g, h, "folders/"+seed.OrganizationFolder, terraformFolderName, terraformFolderVariable, recorded.TerraformFolder); err != nil {
		return nil, err
	}
	if err := f.withdrawLeft(ctx, g, seed, h); err != nil {
		return nil, err
	}

	return f.conclude(ctx, h, seed, owner+"/"+repo, head)
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
// seed made them. A file that names neither is not this creation's push.
func (f *creation) seeded(ctx context.Context, h *org.Placement, head string) (*org.Seed, error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	src, err := f.gh.Contents(ctx, owner, repo, seededTfvars, head)
	if err != nil {
		return nil, errors.Wrapf(err, "reading %s at %s of %s/%s", seededTfvars, short(head), owner, repo)
	}
	file, diags := hclsyntax.ParseConfig(src, seededTfvars, hcl.InitialPos)
	if diags.HasErrors() {
		return nil, errors.Wrapf(diags, "hclsyntax.ParseConfig(): %s on %s of %s/%s", seededTfvars, branch, owner, repo)
	}
	attributes, diags := file.Body.JustAttributes()
	if diags.HasErrors() {
		return nil, errors.Wrapf(diags, "hcl.Body.JustAttributes(): %s on %s of %s/%s", seededTfvars, branch, owner, repo)
	}
	recorded := &org.Seed{}
	for _, folder := range []struct {
		variable string
		id       *string
	}{{organizationFolderVariable, &recorded.OrganizationFolder}, {terraformFolderVariable, &recorded.TerraformFolder}} {
		attribute, ok := attributes[folder.variable]
		if !ok {
			return nil, errors.Newf("%s on %s of %s/%s carries no %s, which the creation's push records: the creation cannot go on from it", seededTfvars, branch, owner, repo, folder.variable)
		}
		value, diags := attribute.Expr.Value(nil)
		if diags.HasErrors() || value.IsNull() || value.Type() != cty.String {
			return nil, errors.Newf("%s in %s on %s of %s/%s is not a folder's id as the creation's push records it: the creation cannot go on from it", folder.variable, seededTfvars, branch, owner, repo)
		}
		*folder.id = value.AsString()
	}

	return recorded, nil
}

// found is the folder named under the parent, which the creation made before its push,
// when it is the one the push recorded under the variable. One that is not there stops
// the creation, which makes nothing again after its push; so does one that is another
// folder of the name, whose policy is not this creation's to change.
func (f *creation) found(ctx context.Context, g Google, h *org.Placement, parent, name, variable, recorded string) (string, error) {
	id, err := g.FindFolder(ctx, parent, name)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.Newf("the foundation of %s is pushed, and the creating identity finds no folder %s under %s, which this creation made before its push: the creation goes on from the push only with the folders it made", f.req.Record.Prefix, name, parent)
	}
	if id != recorded {
		return "", errors.Newf("the creating identity finds folder %s by the name %s under %s, and %s on %s of %s/%s records %s %s: the folder found is not the one this creation made, so nothing is taken off it and nothing is recorded", id, name, parent, seededTfvars, h.GithubDefaultBranch, h.GithubOrganization, h.SourceRepo, variable, recorded)
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
// far as Google can tell. The record names no commit as the creation's push, so the run
// again reads the placement at the branch's head, and a commit after the push may have
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
func (f *creation) beneath(ctx context.Context, g Google, seed *org.Seed, h *org.Placement, all []removal) error {
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
// (Held, which Google answers a caller that holds nothing). It asks first whether what
// the placement records is what the creation made (beneath), and takes nothing off a
// placement that is not.
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
	if err := f.beneath(ctx, g, seed, h, all); err != nil {
		return err
	}
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

// resume.go is a creation run again after its push. From the push on the creating
// identity takes its own grants off, one removal after another, and what the steps before
// the push did as that identity (the seed's reads of the boot project, both first
// applies) needs the grants the withdrawal takes: run again, they would be refused, and
// only a person granting the identity by hand would get the creation past them. So a run
// that finds the foundation on the new repository's default branch does none of them
// again. It reads what the creation made from the pushed placement, finds the two folders
// by their names, takes off what the withdrawal left, and ends as every creation does:
// the marker, and the hand-off. The first applies are the new organization's own from the
// push on, which its apply trigger runs as its own identities.

package create

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
)

// pushed reads whether an earlier run of this creation pushed the foundation: the new
// repository's default branch carries this organization's placement (Foundation). It
// answers the branch's head and the placement on it, which records what the creation made
// before its push; no placement when nothing was pushed, which a repository that does not
// stand yet, or one without the branch the placement names, says too.
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

// resume finishes a creation that stopped after its push: what the pushed placement
// records goes into the placement (the projects, the folders, the tags and the zone), the
// organization's folder and its terraform folder are found by their names, the withdrawal
// runs when it has not ended, and the marker and the hand-off follow.
//
// The withdrawal takes the creating identity's roles on the organization's folder off
// last, in one write, and its Folder Admin there reaches every folder and project under
// it. So while the identity may still set that folder's policy, the withdrawal is not
// over, and every removal of it is the identity's to make again: one made already reads
// the policy and writes nothing. Once it may not, the last removal went through, and with
// it every one before. Google answers that one question for a caller that holds nothing
// (AdministersFolder), and the two folders are found by the identity's Folder Creator at
// the organization node, which the withdrawal does not touch.
func (f *creation) resume(ctx context.Context, g Google, h *org.Placement, head string, placed *org.Placement) (*Result, error) {
	owner, repo, branch := h.GithubOrganization, h.SourceRepo, h.GithubDefaultBranch
	if placed.ProjectNumbers[bootKey] == "" || len(placed.Folders) == 0 {
		return nil, errors.Newf("%s on %s of %s/%s carries this organization's state bucket and lacks the boot project's number (projectNumbers.%s) or the folders (folders), which the creation's push records: the creation cannot go on from it", org.PlacementFile, branch, owner, repo, bootKey)
	}
	h.Projects, h.ProjectNumbers = placed.Projects, placed.ProjectNumbers
	h.Folders, h.PublicInvokerTags, h.AppsZone = placed.Folders, placed.PublicInvokerTags, placed.AppsZone
	fmt.Fprintf(f.out, "%s of %s/%s carries the foundation already (%s): an earlier run of this creation pushed it and stopped. Nothing before the push is done again (the first applies are the organization's own from that push on, which its apply trigger runs), and the creation goes on from there.\n", branch, owner, repo, short(head))
	seed := &org.Seed{}
	var err error
	if seed.OrganizationFolder, err = f.found(ctx, g, "organizations/"+h.OrganizationID, h.Prefix); err != nil {
		return nil, err
	}
	if seed.TerraformFolder, err = f.found(ctx, g, "folders/"+seed.OrganizationFolder, terraformFolderName); err != nil {
		return nil, err
	}
	administers, err := g.AdministersFolder(ctx, seed.OrganizationFolder)
	if err != nil {
		return nil, err
	}
	if administers {
		if err := f.withdraw(ctx, g, seed, h, h.Folders); err != nil {
			return nil, err
		}
	} else {
		fmt.Fprintf(f.out, "The creating identity administers folder %s no longer: its withdrawal ended in the run that stopped, and nothing under the folder is left to take off.\n", seed.OrganizationFolder)
	}

	return f.conclude(ctx, h, seed, owner+"/"+repo, head)
}

// found is the folder named under the parent, which the creation made before its push;
// one that is not there stops the creation, which makes nothing again after its push.
func (f *creation) found(ctx context.Context, g Google, parent, name string) (string, error) {
	id, err := g.FindFolder(ctx, parent, name)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.Newf("the foundation of %s is pushed, and the creating identity finds no folder %s under %s, which this creation made before its push: the creation goes on from the push only with the folders it made", f.req.Record.Prefix, name, parent)
	}

	return id, nil
}

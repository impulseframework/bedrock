// orglibrary.go is org library: the libraries a root organization keeps, by command.
// library add lists a new one in the placement, so nobody edits placement.json by hand to
// get a library's repository: the entry goes in, the placement is held to the rules a hand
// edit is held to, and the change is left in the working tree for a pull request, whose
// merge is what makes the repository.

package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"
	"github.com/spf13/cobra"

	"github.com/impulseframework/bedrock/internal/org"
)

const orgLibraryAddCmd = "org library add"

// newOrgLibrary is org library.
func newOrgLibrary(d deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "library",
		Short: "The libraries a root organization keeps: repositories of its GitHub organization that deploy nowhere",
		Long: `library is a root organization's list of libraries: repositories of its GitHub organization
that deploy nowhere, each an entry under libraries in placement.json. The engine keeps every
listed library on GitHub (the repository, its visibility, its merge settings, the ruleset on
its default branch, the version tags' ruleset and the release app's credentials), and a
library comes onto the list by pull request like every other change to the organization.
library add writes the entry. Taking a library off the list, and changing an entry's checks or
visibility, are edits of placement.json by hand.`,
	}
	cmd.AddCommand(newOrgLibraryAdd(d))

	return cmd
}

// libraryAddFlags are library add's flags.
type libraryAddFlags struct {
	dir             string
	public, adopted bool
	checks          []string
}

// newOrgLibraryAdd is org library add.
func newOrgLibraryAdd(d deps) *cobra.Command {
	f := &libraryAddFlags{}
	cmd := &cobra.Command{
		Use:   "add <repository>",
		Short: "List a new library in the root organization's placement, for a pull request whose merge makes its repository",
		Long: `add lists a library in a root organization's placement.json, run in a clone of the root
organization's repository, the one that holds placement.json. It never runs in the library's
own repository, which does not exist yet: the merge of this change is what makes it.

It takes the repository's name. The repository is private unless --public is given. --check
names a check a pull request into the library's default branch must pass, by the name a pull
request shows it under (a job's name, or for a job of a reusable workflow the calling job's
id, a slash and the called job's name); give it once for each check, or leave it out and add
the checks to the entry once the library's own workflows exist. --adopted is for a
repository that already stands in the GitHub organization, one transferred into it: the
engine brings its settings and its ruleset to the placement and reports the rulesets it
carries.

It adds the entry, holds the placement to the rules a hand edit is held to (a name GitHub
takes, a library listed once, never the organization's own repository, a root organization
alone), renders, writes the journal line and leaves the working tree for a pull request. It
reaches nothing on GitHub and opens no pull request: the pull request's plan says what the
merge will do, and the merge's apply makes the repository with its merge settings and its
ruleset, adds it to the version tags' ruleset and lets it read the release app's
credentials. It gives no team access to the new repository; that stays a step by hand on
GitHub.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return d.addLibrary(cmd, f, args[0])
		},
	}
	cmd.Flags().StringVar(&f.dir, "dir", "", "the repository root (default: found from the working directory)")
	cmd.Flags().BoolVar(&f.public, "public", false, "the repository is public (private without it)")
	cmd.Flags().StringArrayVar(&f.checks, "check", nil, "a check a pull request into the library's default branch must pass, by the name a pull request shows it under (repeatable)")
	cmd.Flags().BoolVar(&f.adopted, "adopted", false, "the repository already stands in the GitHub organization (one transferred into it)")

	return cmd
}

// addLibrary lists the library in the root organization's placement, renders and leaves
// the pull request.
func (d deps) addLibrary(cmd *cobra.Command, f *libraryAddFlags, repository string) error {
	out := cmd.OutOrStdout()
	dir, p, err := d.orgRepository(f.dir)
	if err != nil {
		return err
	}
	if p == nil {
		return errors.New("not in a root organization's repository (no placement.json naming an organization at the repository's root): library add runs in a clone of the root organization's repository, the one that holds placement.json, and never in the library's own, which the merge of this change makes; run it there, or pass --dir")
	}
	if !p.IsRoot() {
		return errors.Newf("the %s organization is a hosting organization, and a library is a project of the root organization, which owns the GitHub organization: run bedrock %s in the root organization's repository", p.Prefix, orgLibraryAddCmd)
	}
	path := orgPlacementPath(dir, "")
	if err := d.orgPinned(p, path); err != nil {
		return err
	}
	if listed := libraryNamed(p, repository); listed != nil {
		return errors.Newf("%s is already a library of the %s organization (libraries in %s): nothing is added; an entry's checks and visibility are changed in placement.json by hand", listed.Repository, p.Prefix, path)
	}
	p.Libraries = append(p.Libraries, org.Library{Repository: repository, Public: f.public, Checks: f.checks, Adopted: f.adopted})
	if err := p.Validate(); err != nil {
		return errors.Wrapf(err, "%s with %s listed", path, repository)
	}
	if err := p.Write(path); err != nil {
		return err
	}
	files, err := org.Render(p)
	if err != nil {
		return err
	}
	written, err := org.Write(p, files, dir)
	if err != nil {
		return err
	}
	entry := librarySummary(p.Library(repository))
	fmt.Fprintf(out, "Listed %s as a library of the %s organization in %s (%s) and rendered into %s: %d owned file(s) written.\n", repository, p.Prefix, path, entry, dir, written.Owned)
	line := fmt.Sprintf("- %s: bedrock %s %s: listed as a library (%s); its repository is made by this pull request's merge.\n", time.Now().UTC().Format("2006-01-02 15:04Z"), orgLibraryAddCmd, repository, entry)
	if err := appendJournalLine(dir, line); err != nil {
		return err
	}
	fmt.Fprintf(out, "A line went into %s.\n", orgJournal)
	fmt.Fprint(out, libraryAddNotes(p, repository))
	fmt.Fprint(out, triggerNotice(p))

	return nil
}

// libraryNamed is the listed library whose repository has the name in any case, nil
// when the placement lists none: GitHub takes a repository's name in any case, and the
// placement's own rule on a library listed twice reads it the same way.
func libraryNamed(p *org.Placement, repository string) *org.Library {
	for i := range p.Libraries {
		if strings.EqualFold(p.Libraries[i].Repository, repository) {
			return &p.Libraries[i]
		}
	}

	return nil
}

// librarySummary says what an entry holds, for the output and the journal line.
func librarySummary(l *org.Library) string {
	parts := []string{"private"}
	if l.Public {
		parts[0] = "public"
	}
	if l.Adopted {
		parts = append(parts, "adopted")
	}
	switch len(l.Checks) {
	case 0:
		parts = append(parts, "no required check yet")
	case 1:
		parts = append(parts, "required check "+l.Checks[0])
	default:
		parts = append(parts, "required checks "+strings.Join(l.Checks, ", "))
	}

	return strings.Join(parts, ", ")
}

// libraryAddNotes says what follows library add, each where it happens, and what the
// entry still lacks.
func libraryAddNotes(p *org.Placement, repository string) string {
	l := p.Library(repository)
	made := fmt.Sprintf("makes %s/%s with its merge settings and its ruleset", p.GithubOrganization, repository)
	if l.Adopted {
		made = fmt.Sprintf("brings %s/%s, which stands already, to the placement's merge settings and ruleset, and reports the rulesets it carries beside the engine's own", p.GithubOrganization, repository)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `
Next, each where it happens:
  1. In a terminal, in this clone: commit placement.json, %s and the rendered files on a
     branch, push it and open the pull request. Nothing has reached GitHub yet.
  2. On GitHub, on that pull request: read its plan, which says what the merge will do, then
     merge it. The apply %s,
     adds it to the version tags' ruleset and lets it read the release app's credentials.
  3. On GitHub, once the repository stands: give the teams that work on it their access, by
     hand. bedrock gives no team access to a library.
`, orgJournal, made)
	if len(l.Checks) == 0 {
		fmt.Fprintf(&b, `  4. In this clone, once the library's own workflows exist: name the checks a pull request
     must pass in its entry (checks in placement.json), by pull request. Until then a pull
     request into %s waits for no check.
`, repository)
	}
	if !p.HasCloud() && p.GithubReleaseAppID == "" {
		b.WriteString("\nplacement.json records no release app (githubReleaseAppId), the one actor that makes a library's release tags: until it records one, the plan notes that the libraries' version tags are not ruled.\n")
	}

	return b.String()
}

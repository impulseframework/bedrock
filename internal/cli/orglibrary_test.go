package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/org"
)

// asRepository marks the directory a repository, so the commands find it from a working
// directory inside it, and answers a directory two levels below its root to run from.
func asRepository(t *testing.T, dir string) (inside string) {
	t.Helper()

	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	inside = filepath.Join(dir, "docs", "notes")
	if err := os.MkdirAll(inside, 0o750); err != nil {
		t.Fatal(err)
	}

	return inside
}

// TestOrgLibraryAdd: library add lists a library in a root organization's placement, run
// anywhere inside the root organization's repository, renders, writes the journal line and
// says what follows, each where it happens. The entry carries what the flags say, a check
// keeps its name whatever it holds, and the repository checks clean afterwards.
func TestOrgLibraryAdd(t *testing.T) {
	t.Parallel()

	const noReleaseApp = "placement.json records no release app (githubReleaseAppId)"
	tests := []struct {
		name string
		// repo builds the root organization's repository and answers its root.
		repo func(t *testing.T) string
		args []string
		// want is the entry the placement holds afterwards, and before the libraries it
		// held, which stay first and in their order.
		want    org.Library
		before  []string
		wantOut []string
		notOut  []string
	}{
		{
			name: "a root organization without a cloud: a private library with no check yet",
			repo: noCloudRepo, args: []string{"impulse"},
			want: org.Library{Repository: "impulse"}, before: []string{"workflows"},
			wantOut: []string{
				"Listed impulse as a library of the imf organization in", "(private, no required check yet)", "owned file(s) written.",
				"A line went into JOURNAL.md.", "Next, each where it happens:",
				"commit placement.json, JOURNAL.md and the rendered files on a\n     branch, push it and open the pull request. Nothing has reached GitHub yet.",
				"The apply makes imp-example/impulse with its merge settings and its ruleset,",
				"bedrock gives no team access to a library.",
				"  4. In this clone, once the library's own workflows exist: name the checks a pull request",
				noReleaseApp,
			},
		},
		{
			name: "a public library with its checks: a check keeps its name, a comma in it included",
			repo: noCloudRepo, args: []string{"bedrock", "--public", "--check", "golang-ci / Run tests and builds", "--check", "lint, format"},
			want:    org.Library{Repository: "bedrock", Public: true, Checks: []string{"golang-ci / Run tests and builds", "lint, format"}},
			before:  []string{"workflows"},
			wantOut: []string{"(public, required checks golang-ci / Run tests and builds, lint, format)", "The apply makes imp-example/bedrock"},
			notOut:  []string{"  4. In this clone"},
		},
		{
			name: "a repository that already stands in the organization",
			repo: noCloudRepo, args: []string{"legacy", "--adopted", "--check", "test"},
			want: org.Library{Repository: "legacy", Adopted: true, Checks: []string{"test"}}, before: []string{"workflows"},
			wantOut: []string{
				"(private, adopted, required check test)",
				"The apply brings imp-example/legacy, which stands already, to the placement's merge settings and ruleset, and reports the rulesets it carries beside the engine's own,",
			},
		},
		{
			name: "a root organization with a cloud: its first library",
			repo: rootRepo, args: []string{"impulse", "--public"},
			want:    org.Library{Repository: "impulse", Public: true},
			wantOut: []string{"Listed impulse as a library of the imr organization in", "(public, no required check yet)"},
			notOut:  []string{noReleaseApp},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := tt.repo(t)
			inside := asRepository(t, dir)
			out, err := execute(orgDeps(inside), "", append([]string{"org", "library", "add"}, tt.args...)...)
			if err != nil {
				t.Fatalf("library add error = %v\n%s", err, out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out, want) {
					t.Errorf("output lacks %q:\n%s", want, out)
				}
			}
			for _, not := range tt.notOut {
				if strings.Contains(out, not) {
					t.Errorf("output carries %q:\n%s", not, out)
				}
			}

			p, err := org.ReadPlacement(filepath.Join(dir, orgPlacementFile))
			if err != nil {
				t.Fatalf("ReadPlacement() after: %v", err)
			}
			if names := p.LibraryNames(); !slices.Equal(names, append(slices.Clone(tt.before), tt.want.Repository)) {
				t.Errorf("libraries = %v, want %v and then %s", names, tt.before, tt.want.Repository)
			}
			got := p.Library(tt.want.Repository)
			if got == nil || got.Repository != tt.want.Repository || got.Public != tt.want.Public || got.Adopted != tt.want.Adopted || !slices.Equal(got.Checks, tt.want.Checks) {
				t.Errorf("the entry = %+v, want %+v", got, tt.want)
			}

			journal, err := os.ReadFile(filepath.Join(dir, orgJournal))
			if err != nil {
				t.Fatalf("the journal: %v", err)
			}
			if line := "bedrock org library add " + tt.want.Repository + ": listed as a library ("; strings.Count(string(journal), line) != 1 {
				t.Errorf("the journal carries the line %q %d time(s), want once:\n%s", line, strings.Count(string(journal), line), journal)
			}
			if _, err := os.Stat(filepath.Join(inside, orgPlacementFile)); err == nil {
				t.Error("a placement.json was written in the working directory, below the repository's root")
			}
			if code, out := runOrg(t, "org", "check", "--dir", dir); code != 0 {
				t.Errorf("org check after library add = %d:\n%s", code, out)
			}
		})
	}
}

// TestOrgLibraryAdd_refused: library add holds the placement to the rules a hand edit is
// held to and says where it runs when it is run anywhere else. A refused run changes
// nothing: the placement is byte for byte what it was, and no journal line is written.
func TestOrgLibraryAdd_refused(t *testing.T) {
	t.Parallel()

	empty := func(t *testing.T) string {
		t.Helper()

		return t.TempDir()
	}
	application := func(t *testing.T) string {
		t.Helper()

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, orgPlacementFile), []byte(`{"code": "quill"}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		return dir
	}
	const elsewhere = "not in a root organization's repository (no placement.json naming an organization at the repository's root): library add runs in a clone of the root organization's repository"
	tests := []struct {
		name    string
		repo    func(t *testing.T) string
		args    []string
		wantErr string
	}{
		{name: "a repository that holds no placement", repo: empty, args: []string{"impulse"}, wantErr: elsewhere},
		{name: "a repository whose placement names no organization", repo: application, args: []string{"impulse"}, wantErr: elsewhere},
		{
			name: "a hosting organization's repository", repo: orgRepo, args: []string{"impulse"},
			wantErr: "the imp organization is a hosting organization, and a library is a project of the root organization, which owns the GitHub organization: run bedrock org library add in the root organization's repository",
		},
		{name: "a library the placement lists", repo: noCloudRepo, args: []string{"workflows"}, wantErr: "workflows is already a library of the imf organization (libraries in"},
		{name: "a library the placement lists, in another case", repo: noCloudRepo, args: []string{"WorkFlows"}, wantErr: "workflows is already a library of the imf organization"},
		{name: "a name GitHub does not take", repo: noCloudRepo, args: []string{"my library"}, wantErr: `repository "my library" is not a repository's name (letters, digits, hyphens, underscores and periods, at most a hundred)`},
		{name: "the organization's own repository", repo: noCloudRepo, args: []string{"imf-root-infrastructure"}, wantErr: "is this organization's own repository (sourceRepo), which holds the placement: a library is another repository"},
		{name: "a check that is blank", repo: noCloudRepo, args: []string{"impulse", "--check", " "}, wantErr: "is blank or carries a space at an end"},
		{name: "a check named twice", repo: noCloudRepo, args: []string{"impulse", "--check", "test", "--check", "test"}, wantErr: `names the check "test" twice`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			dir := tt.repo(t)
			inside := asRepository(t, dir)
			before, _ := os.ReadFile(filepath.Join(dir, orgPlacementFile))
			out, err := execute(orgDeps(inside), "", append([]string{"org", "library", "add"}, tt.args...)...)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("library add error = %v, want one containing %q\n%s", err, tt.wantErr, out)
			}
			if after, _ := os.ReadFile(filepath.Join(dir, orgPlacementFile)); !bytes.Equal(before, after) {
				t.Errorf("a refused library add changed placement.json:\n%s", after)
			}
			if _, err := os.Stat(filepath.Join(dir, orgJournal)); err == nil {
				t.Error("a refused library add wrote the journal")
			}
		})
	}

	t.Run("outside any repository, it says where to run it", func(t *testing.T) {
		t.Parallel()

		_, err := execute(orgDeps(t.TempDir()), "", "org", "library", "add", "impulse")
		if err == nil || !strings.Contains(err.Error(), elsewhere) {
			t.Errorf("library add error = %v, want one containing %q", err, elsewhere)
		}
	})

	t.Run("it takes one repository", func(t *testing.T) {
		t.Parallel()

		for _, args := range [][]string{{}, {"impulse", "bedrock"}} {
			if _, err := execute(orgDeps(t.TempDir()), "", append([]string{"org", "library", "add"}, args...)...); err == nil || !strings.Contains(err.Error(), "accepts 1 arg(s)") {
				t.Errorf("library add %v error = %v, want cobra's refusal of the argument count", args, err)
			}
		}
	})
}

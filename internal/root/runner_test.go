package root_test

import (
	"slices"
	"testing"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// The sentences of the runner tests: the runner the bootstrap's apply made and its removal.
const (
	removesEightCore = "GitHub: removes the larger runner imf-8core (8-core) from the group bedrock-applications: "
	renamed          = "placement.json no longer names it (ciLargeRunner); a job running on it finishes"
	unsized          = "placement.json sizes no larger runner (ciLargeRunnerSize); a job running on it finishes"
)

// TestEngineRetiresRunners: a runner in bedrock's group that the placement does not name
// is removed by the apply, and the plan names it: the runner of the earlier name after a
// rename, which is how a runner changes size, every runner once the placement sizes none,
// and one a person made in the group. A runner the placement names stays, sized or not, a
// runner in another group is not bedrock's, and one GitHub is removing already is left to
// it.
func TestEngineRetiresRunners(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// place changes the placement after the bootstrap's apply, which made imf-8core.
		place func(p *org.Placement)
		// stands adds what a person made on GitHub after the bootstrap's apply.
		stands func(w *githubWorld, group int64)
		// want are the sentences the check says of the larger runner and its variable.
		want []string
		// wantRunners are the hosted runners after the apply, by name.
		wantRunners []string
	}{
		{
			name: "a rename makes the new runner and removes the one of the earlier name",
			place: func(p *org.Placement) {
				p.CiLargeRunner, p.CiLargeRunnerSize = "imf-16core", "16-core"
			},
			want: []string{
				"GitHub: sets the organization variable CI_LARGE_RUNNER to imf-16core (was imf-8core)",
				"GitHub: makes the larger runner imf-16core (16-core, at most 4 at once) in the group bedrock-applications",
				removesEightCore + renamed,
			},
			wantRunners: []string{"imf-16core"},
		},
		{
			name: "a placement that sizes no runner removes the variable and every runner of the group",
			place: func(p *org.Placement) {
				p.CiLargeRunner, p.CiLargeRunnerSize, p.CiLargeRunnerMaximum = "", "", 0
			},
			want: []string{
				"GitHub: removes the organization variable CI_LARGE_RUNNER (the workflows' jobs run on GitHub's standard runner)",
				removesEightCore + unsized,
			},
		},
		{
			name: "a runner the placement names without a size stays",
			place: func(p *org.Placement) {
				p.CiLargeRunnerSize, p.CiLargeRunnerMaximum = "", 0
			},
			wantRunners: []string{"imf-8core"},
		},
		{
			name: "a runner made by hand in the group goes, one in another group and one being removed stay",
			stands: func(w *githubWorld, group int64) {
				owner := w.p.GithubOrganization
				w.srv.Update(func() {
					w.srv.RunnerGroups[owner] = append(w.srv.RunnerGroups[owner], &github.RunnerGroup{ID: 9100, Name: "applications", Visibility: github.VisibilityAll})
					w.srv.HostedRunners[owner] = append(w.srv.HostedRunners[owner],
						&github.HostedRunner{ID: 9101, Name: "hand-made", RunnerGroupID: group, MachineSize: &github.MachineSize{ID: "4-core"}, Status: "Ready"},
						&github.HostedRunner{ID: 9102, Name: "theirs", RunnerGroupID: 9100, MachineSize: &github.MachineSize{ID: "4-core"}, Status: "Ready"},
						&github.HostedRunner{ID: 9103, Name: "going", RunnerGroupID: group, MachineSize: &github.MachineSize{ID: "4-core"}, Status: "Deleting"},
					)
				})
			},
			want:        []string{"GitHub: removes the larger runner hand-made (4-core) from the group bedrock-applications: " + renamed},
			wantRunners: []string{"imf-8core", "theirs", "going"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := libraryPlacement(t)
			p.CiLargeRunner, p.CiLargeRunnerSize, p.CiLargeRunnerMaximum = "imf-8core", "8-core", 4
			if err := p.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			w := bootstrapped(t, p)
			groups := w.srv.RunnerGroups[p.GithubOrganization]
			if len(groups) != 1 || groups[0].Name != "bedrock-applications" {
				t.Fatalf("the runner groups after the bootstrap's apply = %+v, want bedrock-applications alone", groups)
			}
			if tt.place != nil {
				tt.place(p)
				if err := p.Validate(); err != nil {
					t.Fatalf("Validate() error = %v", err)
				}
			}
			if tt.stands != nil {
				tt.stands(w, groups[0].ID)
			}

			report, _ := w.run(t, root.Check, nil)
			got := slices.Concat(mentioning(whats(report), "CI_LARGE_RUNNER"), mentioning(whats(report), "the larger runner"))
			sameSentences(t, "what the check says of the larger runner", got, tt.want)

			if applied, out := w.run(t, root.Apply, nil); len(applied.Findings) != 0 || applied.Applied != len(applied.Changes) {
				t.Fatalf("the apply made %d of %d changes, with the findings %q:\n%s", applied.Applied, len(applied.Changes), applied.Findings, out)
			}
			var names []string
			for _, r := range w.srv.HostedRunners[p.GithubOrganization] {
				names = append(names, r.Name)
			}
			sameSentences(t, "the hosted runners after the apply", names, tt.wantRunners)
			if groups := w.srv.RunnerGroups[p.GithubOrganization]; !slices.ContainsFunc(groups, func(g *github.RunnerGroup) bool { return g.Name == "bedrock-applications" }) {
				t.Errorf("the runner groups after the apply = %+v, want bedrock-applications kept", groups)
			}
			w.clean(t)
		})
	}
}

// TestEngineRunnerSizeIsANewName: a runner whose size differs from the placement's is a
// finding that says how the size changes, a new name, and asks nobody to delete a runner.
func TestEngineRunnerSizeIsANewName(t *testing.T) {
	t.Parallel()

	p := libraryPlacement(t)
	p.CiLargeRunner, p.CiLargeRunnerSize, p.CiLargeRunnerMaximum = "imf-8core", "8-core", 4
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	w := bootstrapped(t, p)
	p.CiLargeRunnerSize = "16-core"

	report, _ := w.run(t, root.Check, nil)
	want := []string{"the larger runner imf-8core is a 8-core, and placement.json sizes it 16-core (ciLargeRunnerSize): a runner's size cannot change in place; give the runner a new name in placement.json (ciLargeRunner), and the apply makes the new one and removes imf-8core"}
	sameSentences(t, "the findings", report.Findings, want)
}

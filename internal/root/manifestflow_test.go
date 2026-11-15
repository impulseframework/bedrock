package root_test

import (
	"context"
	"html"
	"regexp"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/root"
)

// TestFlowRefusesAnAppOwnedElsewhere: GitHub makes an app under the account the browser
// is signed in as when that account may not make one for the organization, and such an
// app can never be installed there. The page takes the app GitHub answers only when the
// organization owns it. An app another account owns is not taken: the page says whose it
// is, where to delete it and that the browser must be signed in as an owner, and it offers
// Create again. The page says up front that the browser is signed in as an owner.
func TestFlowRefusesAnAppOwnedElsewhere(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		owner     github.Account
		wantTaken bool
		wantSaid  []string
	}{
		{
			name:      "an app the organization owns is taken",
			owner:     github.Account{Login: "imp-example", Type: "Organization"},
			wantTaken: true,
		},
		{
			name:  "an app under the person's own account is refused, and the page says where to delete it",
			owner: github.Account{Login: "gale-reyes", Type: "User"},
			wantSaid: []string{
				"under the account gale-reyes, not the imp-example organization, so it can never be installed there",
				"the browser was signed in as an account that is not an owner of imp-example",
				"Delete it (https://github.com/settings/apps/imp-example-infrastructure, Advanced, Delete GitHub App), since GitHub keeps its name while it exists",
				"sign GitHub in, in this browser, as an owner of imp-example; then Create it again here.",
			},
		},
		{
			name:  "an app under another organization is refused, with that organization's settings page",
			owner: github.Account{Login: "other-org", Type: "Organization"},
			wantSaid: []string{
				"under the account other-org, not the imp-example organization",
				"Delete it (https://github.com/organizations/other-org/settings/apps/imp-example-infrastructure, Advanced, Delete GitHub App)",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			manifests := githubRootDraft().AppManifests()[:1]
			made := &github.App{ID: 7001, Slug: manifests[0].Name, Name: manifests[0].Name, Owner: tt.owner, PEM: "a key"}
			convert := func(context.Context, string) (*github.App, error) {
				return made, nil
			}
			// The page asks GitHub nothing itself: the installations are looked for by Wait,
			// which this test does not run.
			gh := deploy.GitHubFunc(func(string) *github.Client {
				t.Error("the page asked GitHub for something")

				return nil
			})
			flow, err := root.StartFlow(t.Context(), manifests, "imp-example", "https://github.com/imp-example", convert, gh)
			if err != nil {
				t.Fatalf("StartFlow() error = %v", err)
			}
			defer flow.Close()

			before := get(t, flow.URL()+"/")
			if !strings.Contains(before, "Sign GitHub in, in this browser, as an owner of <b>imp-example</b> before you start") {
				t.Errorf("the page does not say up front that the browser is signed in as an owner:\n%s", before)
			}
			state := regexp.MustCompile(`state=([0-9a-f]+)"`).FindStringSubmatch(before)
			if state == nil {
				t.Fatalf("the page has no form:\n%s", before)
			}
			get(t, flow.URL()+"/made?code=a-code&state="+state[1])

			after := html.UnescapeString(get(t, flow.URL()+"/"))
			taken := strings.Contains(after, "Made: App 7001")
			if taken != tt.wantTaken {
				t.Errorf("the page took App 7001 = %v, want %v:\n%s", taken, tt.wantTaken, after)
			}
			if offered := strings.Contains(after, "Create "+manifests[0].Name+" on GitHub"); offered == tt.wantTaken {
				t.Errorf("the page offers Create again = %v, want %v", offered, !tt.wantTaken)
			}
			for _, want := range tt.wantSaid {
				if !strings.Contains(after, want) {
					t.Errorf("the page lacks %q:\n%s", want, after)
				}
			}
			if !tt.wantTaken && !strings.Contains(flow.Pending(), "(to make)") {
				t.Errorf("Pending() = %q, want the app still to make", flow.Pending())
			}
		})
	}
}

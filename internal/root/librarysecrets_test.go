package root_test

import (
	"bytes"
	"context"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/github/githubtest"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// TestEngineReleaseKeyMissing: without a cloud a run that holds no key cannot seal the
// release app's, so an organization without the secret is a finding that names the
// command a person seals a key with; the client ID, which the app's record gives, is set
// when its secret is missing too. A run handed a key seals it for the libraries alone
// and sets the client ID again beside it, and the check after it is clean.
func TestEngineReleaseKeyMissing(t *testing.T) {
	t.Parallel()

	finding := "the organization secret RELEASE_PLEASE_APP_PEM does not exist, and a library's release workflow mints the release app's token from it: generate a private key on the app's settings page and seal it with bedrock secret add github-release-key --from-file <the file>"
	setsClientID := "libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read"
	tests := []struct {
		name        string
		gone        []string
		wantChanges []string
	}{
		{name: "the key's secret is gone", gone: []string{"RELEASE_PLEASE_APP_PEM"}},
		{name: "both secrets are gone", gone: []string{"RELEASE_PLEASE_APP_PEM", "RELEASE_PLEASE_APP_CLIENT_ID"}, wantChanges: []string{setsClientID}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			for _, name := range tt.gone {
				delete(w.srv.OrgSecrets["imf-example"], name)
			}
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the findings of a check that holds no key", report.Findings, []string{finding})
			sameSentences(t, "the changes of a check that holds no key", whats(report), tt.wantChanges)

			// The workflow's apply holds no key either: it sets the client ID and still
			// says the key is missing.
			report, _ = w.run(t, root.Apply, nil)
			sameSentences(t, "the findings of an apply that holds no key", report.Findings, []string{finding})
			if _, sealed := w.srv.OrgSecrets["imf-example"]["RELEASE_PLEASE_APP_PEM"]; sealed {
				t.Fatal("an apply that holds no key made the key's secret")
			}
			ids := w.libraryIDs(t, "workflows", "impulse", "notes")
			if client := w.orgSecret(t, "RELEASE_PLEASE_APP_CLIENT_ID"); client.Visibility != "selected" || !slices.Equal(client.RepositoryIDs, ids) || string(client.Value) != clientID(org.ReleaseApp) {
				t.Errorf("RELEASE_PLEASE_APP_CLIENT_ID: visibility %q, read by %v, holding %q; want selected, %v, %q", client.Visibility, client.RepositoryIDs, client.Value, ids, clientID(org.ReleaseApp))
			}

			// bedrock secret add hands the run a key generated anew.
			anew := []byte("a private key generated anew on the release app's settings page")
			report, out := w.run(t, root.Apply, map[org.AppRole][]byte{org.ReleaseApp: anew})
			sameSentences(t, "the changes of the apply handed a key", whats(report), []string{
				"libraries: seals the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (workflows, impulse, notes)",
				setsClientID,
			})
			if key := w.orgSecret(t, "RELEASE_PLEASE_APP_PEM"); key.Visibility != "selected" || !slices.Equal(key.RepositoryIDs, ids) || !bytes.Equal(key.Value, anew) || len(report.Findings) != 0 {
				t.Errorf("RELEASE_PLEASE_APP_PEM: visibility %q, read by %v, holding the key handed: %t, with the findings %q; want selected, %v, true and none:\n%s", key.Visibility, key.RepositoryIDs, bytes.Equal(key.Value, anew), report.Findings, ids, out)
			}
			w.clean(t)
		})
	}
}

// TestEngineReleaseSecretReaders: the repositories that read the release app's secrets
// are the libraries and no other. A repository on the key's list that the placement does
// not list is a finding, said once, for the key, and a change whose apply sets the list
// to the libraries; a library a list lacks is a change that adds it. The apply rewrites
// the lists alone: neither secret's value is sealed again, and the repository taken off
// stands as it was.
func TestEngineReleaseSecretReaders(t *testing.T) {
	t.Parallel()

	const key, client = "RELEASE_PLEASE_APP_PEM", "RELEASE_PLEASE_APP_CLIENT_ID"
	reads := func(name string) string {
		return "imf-example/" + name + " reads the release app's key (the organization secret RELEASE_PLEASE_APP_PEM) and placement.json lists no such library: a library taken off the list is archived by a person, never deleted here, and the apply takes the key from it"
	}
	brings := func(secret, what string) string {
		return "libraries: brings the repositories that read the organization secret " + secret + " to the libraries: " + what
	}
	// scratch is a repository of the organization that was never a library.
	scratch := func(w *githubWorld) int64 {
		return w.srv.AddRepo("imf-example", "scratch", &githubtest.Repo{Private: true}).ID
	}
	tests := []struct {
		name         string
		seed         func(t *testing.T, w *githubWorld)
		wantFindings []string
		wantChanges  []string
		wantReaders  []string
	}{
		{
			name:         "a library taken off the list",
			seed:         func(_ *testing.T, w *githubWorld) { w.p.Libraries = w.p.Libraries[:2] },
			wantFindings: []string{reads("notes")},
			wantChanges: []string{
				`libraries: brings the imf-example organization's ruleset "version tags" to the placement: tags and repositories`,
				brings(key, "takes notes off"), brings(client, "takes notes off"),
			},
			wantReaders: []string{"workflows", "impulse"},
		},
		{
			name: "a repository that was never a library, on the key's list",
			seed: func(t *testing.T, w *githubWorld) {
				t.Helper()

				secret := w.orgSecret(t, key)
				secret.RepositoryIDs = append(secret.RepositoryIDs, scratch(w))
			},
			wantFindings: []string{reads("scratch")},
			wantChanges:  []string{brings(key, "takes scratch off")},
			wantReaders:  []string{"workflows", "impulse", "notes"},
		},
		{
			name: "a repository that was never a library, on the client ID's list",
			seed: func(t *testing.T, w *githubWorld) {
				t.Helper()

				secret := w.orgSecret(t, client)
				secret.RepositoryIDs = append(secret.RepositoryIDs, scratch(w))
			},
			wantChanges: []string{brings(client, "takes scratch off")},
			wantReaders: []string{"workflows", "impulse", "notes"},
		},
		{
			name: "a library the key's list lacks",
			seed: func(t *testing.T, w *githubWorld) {
				t.Helper()

				w.orgSecret(t, key).RepositoryIDs = w.libraryIDs(t, "workflows", "notes")
			},
			wantChanges: []string{brings(key, "adds impulse")},
			wantReaders: []string{"workflows", "impulse", "notes"},
		},
		{
			name: "a library lacking and two strangers on the key's list",
			seed: func(t *testing.T, w *githubWorld) {
				t.Helper()

				old := w.srv.AddRepo("imf-example", "an-old-library", &githubtest.Repo{}).ID
				w.orgSecret(t, key).RepositoryIDs = append(w.libraryIDs(t, "workflows", "notes"), scratch(w), old)
			},
			wantFindings: []string{reads("scratch"), reads("an-old-library")},
			wantChanges:  []string{brings(key, "adds impulse; takes an-old-library, scratch off")},
			wantReaders:  []string{"workflows", "impulse", "notes"},
		},
		{
			name: "a list that names no repository",
			seed: func(t *testing.T, w *githubWorld) {
				t.Helper()

				w.orgSecret(t, key).RepositoryIDs = []int64{}
			},
			wantChanges: []string{brings(key, "adds workflows, impulse, notes")},
			wantReaders: []string{"workflows", "impulse", "notes"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			sealedAt := w.orgSecret(t, key).UpdatedAt
			tt.seed(t, w)
			before := w.state(t)
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the findings of the check", report.Findings, tt.wantFindings)
			sameSentences(t, "the changes of the check", whats(report), tt.wantChanges)
			if diff := cmp.Diff(before, w.state(t)); diff != "" {
				t.Errorf("the check changed what GitHub holds (-before +after):\n%s", diff)
			}

			report, out := w.run(t, root.Apply, nil)
			if report.Applied != len(tt.wantChanges) {
				t.Errorf("the apply made %d change(s), want %d:\n%s", report.Applied, len(tt.wantChanges), out)
			}
			ids := w.libraryIDs(t, tt.wantReaders...)
			for _, name := range []string{key, client} {
				if got := w.orgSecret(t, name).RepositoryIDs; !slices.Equal(got, ids) {
					t.Errorf("after the apply %s is read by %v, want by %v (%v)", name, got, tt.wantReaders, ids)
				}
			}
			if secret := w.orgSecret(t, key); !bytes.Equal(secret.Value, w.keys[org.ReleaseApp]) || !secret.UpdatedAt.Equal(sealedAt) {
				t.Error("the apply sealed the release app's key again; it sets the list of its readers alone")
			}
			if repo := w.repo(t, "notes"); repo.Archived || len(repo.Rulesets) != 1 {
				t.Errorf("notes: archived %t, %d ruleset(s); a repository taken off the list stands as it was", repo.Archived, len(repo.Rulesets))
			}
			w.clean(t)
		})
	}
}

// TestEngineReleaseSecretReadByEveryRepository: a secret every repository of the
// organization reads, or every private one, cannot be narrowed in place, since GitHub
// takes a secret's visibility with its value alone. The key, whose value a run of the
// workflow does not hold, is a finding that says to seal it again; the client ID, which
// the app's record gives, is set again for the libraries alone. A run handed a key seals
// it for the libraries alone.
func TestEngineReleaseSecretReadByEveryRepository(t *testing.T) {
	t.Parallel()

	for _, visibility := range []string{"all", "private"} {
		t.Run(visibility, func(t *testing.T) {
			t.Parallel()

			w := bootstrapped(t, libraryPlacement(t))
			key, client := w.orgSecret(t, "RELEASE_PLEASE_APP_PEM"), w.orgSecret(t, "RELEASE_PLEASE_APP_CLIENT_ID")
			key.Visibility, key.RepositoryIDs = visibility, nil
			client.Visibility, client.RepositoryIDs = visibility, nil
			finding := "the organization secret RELEASE_PLEASE_APP_PEM is read by " + visibility + " repositories of imf-example, not by the libraries alone: seal it again, which limits it to them (bedrock secret add github-release-key --from-file <the file>)"
			setsClientID := "libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read"
			report, _ := w.run(t, root.Check, nil)
			sameSentences(t, "the findings of the check", report.Findings, []string{finding})
			sameSentences(t, "the changes of the check", whats(report), []string{setsClientID})

			// The workflow's apply sets the client ID again, and leaves the key it cannot
			// seal as it is.
			report, _ = w.run(t, root.Apply, nil)
			sameSentences(t, "the findings of an apply that holds no key", report.Findings, []string{finding})
			ids := w.libraryIDs(t, "workflows", "impulse", "notes")
			client = w.orgSecret(t, "RELEASE_PLEASE_APP_CLIENT_ID")
			if client.Visibility != "selected" || !slices.Equal(client.RepositoryIDs, ids) || string(client.Value) != clientID(org.ReleaseApp) {
				t.Errorf("RELEASE_PLEASE_APP_CLIENT_ID: visibility %q, read by %v, holding %q; want selected, %v, %q", client.Visibility, client.RepositoryIDs, client.Value, ids, clientID(org.ReleaseApp))
			}
			if key = w.orgSecret(t, "RELEASE_PLEASE_APP_PEM"); key.Visibility != visibility || !bytes.Equal(key.Value, w.keys[org.ReleaseApp]) {
				t.Errorf("an apply that holds no key wrote the key's secret: visibility %q", key.Visibility)
			}
			report, _ = w.run(t, root.Check, nil)
			sameSentences(t, "the findings of the check after that apply", report.Findings, []string{finding})
			sameSentences(t, "the changes of the check after that apply", whats(report), nil)

			// bedrock secret add hands the run a key generated anew.
			anew := []byte("a private key generated anew on the release app's settings page")
			w.run(t, root.Apply, map[org.AppRole][]byte{org.ReleaseApp: anew})
			if key = w.orgSecret(t, "RELEASE_PLEASE_APP_PEM"); key.Visibility != "selected" || !slices.Equal(key.RepositoryIDs, ids) || !bytes.Equal(key.Value, anew) {
				t.Errorf("RELEASE_PLEASE_APP_PEM after the apply handed a key: visibility %q, read by %v, holding the key handed: %t; want selected, %v, true", key.Visibility, key.RepositoryIDs, bytes.Equal(key.Value, anew), ids)
			}
			w.clean(t)
		})
	}
}

// withLibrary lists a library in the placement of the root organization with a cloud,
// and brings GitHub's stand-in along: the three apps hold what a root organization that
// keeps libraries declares, GitHub Actions' own app is there to report the library's
// checks, and the release app answers by its slug with its client ID.
func withLibrary(t *testing.T, w *world) {
	t.Helper()

	w.p.Libraries = []org.Library{{Repository: "shared-types", Checks: []string{"ci"}}}
	if err := w.p.Validate(); err != nil {
		t.Fatalf("the root organization with a cloud and a library: Validate() error = %v", err)
	}
	for _, m := range w.p.AppManifests() {
		w.srv.Apps[w.p.AppID(m.Role)].Record.Permissions = m.Permissions
	}
	w.srv.PublicApps["github-actions"] = github.App{ID: actionsAppID, Slug: "github-actions", Name: "GitHub Actions"}
	release := w.srv.Apps[w.p.GithubReleaseAppID].Record
	release.ClientID = clientID(org.ReleaseApp)
	w.srv.PublicApps[w.p.ReleaseAppSlug()] = release
}

// TestEngineLibrariesWithCloud: a root organization with a cloud that lists a library
// keeps it beside its zones and triggers, in the same run. The library is made with its
// rule; the release app's key is sealed into the organization secret from the Secret
// Manager version the placement pins, and an organization variable records that version,
// since the secret says nothing of what it holds; the run after it is clean; moving the
// pin seals the new version and records it; and a secret deleted on GitHub is sealed
// again from the version pinned. The root repository gains no environment and keeps the
// plan trigger's check: its engine runs in Cloud Build, not in a workflow. As the engine
// stands, a key every repository reads is a finding for a person here too.
func TestEngineLibrariesWithCloud(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	withLibrary(t, w)
	ctx := context.Background()
	owner := w.p.GithubOrganization
	secret := "projects/" + w.p.Projects[org.RootProjectKey] + "/secrets/imr-root-gbl-github-release-key"
	// The placement pins version 2 of the key; version 1 is a key from before it.
	w.fake.Payloads[secret+"/versions/1"] = []byte("the release app's key before the one pinned")
	report, out := w.run(t, root.Apply, true)
	if len(report.Findings) != 0 || report.Applied != len(report.Changes) {
		t.Fatalf("the bootstrap's apply made %d of %d changes, with the findings %q:\n%s", report.Applied, len(report.Changes), report.Findings, out)
	}
	sameSentences(t, "the changes of the libraries section", mentioning(whats(report), "libraries: "), []string{
		"libraries: creates the private repository imp-example/shared-types, with an initial commit on master",
		"libraries: sets the merge settings of imp-example/shared-types: " + mergeSentence,
		"libraries: rules the default branch of imp-example/shared-types: pull requests alone with 1 approving review(s), squash merges, the checks ci required, the infrastructure app bypassing",
		`libraries: rules the version tags of shared-types in the imp-example organization (ruleset "version tags"): the release app alone creates, moves or deletes a tag named v* or */v*`,
		"libraries: seals version 2 of the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (shared-types)",
		"libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read",
		"libraries: sets the organization variable RELEASE_APP_LOGIN to imp-example-release[bot], the author a library's release workflow accepts a release from",
	})
	containsAll(t, whats(report), "GitHub: rules the default branch of imp-example/imr-root-infrastructure: pull requests alone with 1 approving review(s), squash merges, the plan trigger's build required, the infrastructure app bypassing")

	library, ok := w.srv.Repos[owner+"/shared-types"]
	if !ok {
		t.Fatal("the apply did not create the library")
	}
	rules := rulesetNamed(library.Rulesets, "default branch")
	if !library.Private || rules == nil || len(rules.Rules) != 4 || !cmp.Equal(rules.Rules[3].Parameters["required_status_checks"], []any{map[string]any{"context": "ci", "integration_id": float64(actionsAppID)}}) {
		t.Errorf("the library is private: %t; its ruleset on the default branch = %+v, want one that requires ci as GitHub Actions reports it", library.Private, rules)
	}
	pinned := w.fake.Payloads[secret+"/versions/2"]
	sealed := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"]
	if sealed == nil || len(pinned) == 0 || !bytes.Equal(sealed.Value, pinned) {
		t.Fatal("RELEASE_PLEASE_APP_PEM does not hold version 2 of the key in Secret Manager, the version the placement pins")
	}
	if sealed.Visibility != "selected" || !slices.Equal(sealed.RepositoryIDs, []int64{library.ID}) {
		t.Errorf("RELEASE_PLEASE_APP_PEM: visibility %q, read by %v; want selected and the library alone (%d)", sealed.Visibility, sealed.RepositoryIDs, library.ID)
	}
	// The key is read by the number pinned, not through the alias, which a person moves.
	if read := "AccessSecretVersion(" + secret + "/versions/2)"; !slices.Contains(w.fake.Calls, read) {
		t.Errorf("the apply did not read the version pinned (%s) among its calls", read)
	}
	if client := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_CLIENT_ID"]; client == nil || string(client.Value) != clientID(org.ReleaseApp) || !slices.Equal(client.RepositoryIDs, []int64{library.ID}) {
		t.Error("RELEASE_PLEASE_APP_CLIENT_ID does not hold the release app's client ID for the library alone")
	}
	wantVersion := github.Variable{Name: "RELEASE_PLEASE_APP_PEM_VERSION", Value: "2", Visibility: "private"}
	if diff := cmp.Diff(wantVersion, w.srv.Variables[owner]["RELEASE_PLEASE_APP_PEM_VERSION"]); diff != "" {
		t.Errorf("the variable that records the version sealed (-want +got):\n%s", diff)
	}
	if got := w.srv.Variables[owner]["RELEASE_APP_LOGIN"].Value; got != "imp-example-release[bot]" {
		t.Errorf("RELEASE_APP_LOGIN = %q", got)
	}
	rootRepo := w.srv.Repos[owner+"/"+w.p.SourceRepo]
	if len(rootRepo.Environments) != 0 {
		t.Errorf("the root repository of an organization with a cloud gained %d environment(s)", len(rootRepo.Environments))
	}
	if rs := rulesetNamed(rootRepo.Rulesets, "default branch"); rs == nil || len(rs.Rules) != 4 || !cmp.Equal(rs.Rules[3].Parameters["required_status_checks"], []any{map[string]any{"context": "imr-root-uc1-plan (imr-root-gbl-core-c3d4)", "integration_id": float64(10529)}}) {
		t.Errorf("the root repository's ruleset = %+v, want the plan trigger's check as Google's Cloud Build app reports it", rs)
	}

	// The run after it changes nothing and seals nothing.
	clean, out := w.run(t, root.Check, false)
	if !clean.Clean() {
		t.Errorf("the check after the apply is not clean:\n%s\nchanges: %q\nfindings: %q", out, whats(clean), clean.Findings)
	}
	again, _ := w.run(t, root.Apply, false)
	if !again.Clean() || !w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"].UpdatedAt.Equal(sealed.UpdatedAt) {
		t.Errorf("an apply again changes %q, or sealed the key again", whats(again))
	}

	// A key generated anew is version 3; pinning it seals it and records it.
	anew, public := keyPair(t)
	version, err := w.fake.AddSecretVersion(ctx, secret, anew)
	if err != nil {
		t.Fatal(err)
	}
	w.srv.Apps[w.p.GithubReleaseAppID].Key = public
	if err := w.p.PinKeyVersion(org.ReleaseKey, version); err != nil {
		t.Fatal(err)
	}
	moved := []string{
		"secrets: moves the alias current of imr-root-gbl-github-release-key from version 2 to version 3",
		"libraries: seals version 3 of the release app's key into the organization secret RELEASE_PLEASE_APP_PEM, which the libraries alone read (shared-types)",
		"libraries: sets the organization secret RELEASE_PLEASE_APP_CLIENT_ID to the release app's client ID, which the libraries alone read",
	}
	report, _ = w.run(t, root.Check, false)
	sameSentences(t, "the changes of the check after the pin moved", whats(report), moved)
	if !bytes.Equal(w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"].Value, pinned) || w.srv.Variables[owner]["RELEASE_PLEASE_APP_PEM_VERSION"].Value != "2" {
		t.Error("the check sealed the key or recorded the version; the apply alone does")
	}
	report, out = w.run(t, root.Apply, false)
	sameSentences(t, "the changes of the apply after the pin moved", whats(report), moved)
	if got := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"]; !bytes.Equal(got.Value, anew) || got.Visibility != "selected" || !slices.Equal(got.RepositoryIDs, []int64{library.ID}) {
		t.Errorf("after the pin moved RELEASE_PLEASE_APP_PEM holds version 3: %t, visibility %q, read by %v:\n%s", bytes.Equal(got.Value, anew), got.Visibility, got.RepositoryIDs, out)
	}
	wantVersion.Value = "3"
	if diff := cmp.Diff(wantVersion, w.srv.Variables[owner]["RELEASE_PLEASE_APP_PEM_VERSION"]); diff != "" {
		t.Errorf("the variable that records the version sealed, after the pin moved (-want +got):\n%s", diff)
	}
	if clean, out := w.run(t, root.Check, false); !clean.Clean() {
		t.Errorf("the check after the pin moved is not clean:\n%s\nchanges: %q\nfindings: %q", out, whats(clean), clean.Findings)
	}

	// The secret deleted on GitHub is sealed again from the version pinned, though the
	// variable still records that version.
	delete(w.srv.OrgSecrets[owner], "RELEASE_PLEASE_APP_PEM")
	report, _ = w.run(t, root.Apply, false)
	sameSentences(t, "the changes of the apply after the secret was deleted", whats(report), moved[1:])
	if got := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"]; got == nil || !bytes.Equal(got.Value, anew) {
		t.Error("the deleted secret was not sealed again from the version pinned")
	}

	// A key every repository reads is sealed again from the version pinned, which is the
	// one way GitHub narrows a secret: with a cloud the engine holds the key, so this is
	// no finding for a person.
	w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"].Visibility = "all"
	report, _ = w.run(t, root.Apply, false)
	sameSentences(t, "the findings about a key every repository reads", report.Findings, nil)
	sameSentences(t, "the changes about a key every repository reads", whats(report), moved[1:])
	if got := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"]; got.Visibility != github.VisibilitySelected || !bytes.Equal(got.Value, anew) {
		t.Errorf("the key every repository read was not sealed again for the libraries alone: visibility %q", got.Visibility)
	}
	if clean, out := w.run(t, root.Check, false); !clean.Clean() {
		t.Errorf("the check after the key was narrowed is not clean:\n%s\nchanges: %q\nfindings: %q", out, whats(clean), clean.Findings)
	}
}

// TestEngineLibrariesWithCloudUnsealed: with a cloud, a run that reads no GitHub leaves
// the libraries unchecked and says so; and while the placement pins no version of the
// release app's key, none is sealed for the libraries, which is a note, never a finding
// that sends a person to seal a key by hand: the key's home is Secret Manager.
func TestEngineLibrariesWithCloudUnsealed(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	withLibrary(t, w)
	var out bytes.Buffer
	report, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: root.Check, Commit: "abc1234"}, &out)
	if err != nil {
		t.Fatalf("Run() without a token: error = %v\n%s", err, out.String())
	}
	sameSentences(t, "the notes of a run without a token", mentioning(report.Notes, "no token"), []string{
		"GitHub was not checked: no token (GITHUB_TOKEN, or the infrastructure app's in a trigger's run)",
		"the libraries were not checked: no token (GITHUB_TOKEN, or the infrastructure app's in a trigger's run)",
	})
	if got := mentioning(whats(report), "libraries: "); len(got) != 0 {
		t.Errorf("a run without a token lists changes to the libraries: %q", got)
	}

	w.p.GithubReleaseKeyVersion = ""
	report, _ = w.run(t, root.Apply, true)
	sameSentences(t, "the notes about the key's pin", mentioning(report.Notes, "pins no version of it"), []string{
		"the release app's key is not sealed for the libraries: placement.json pins no version of it (github-release-key)",
	})
	if got := mentioning(report.Findings, "RELEASE_PLEASE_APP_PEM"); len(got) != 0 {
		t.Errorf("an unpinned key is a finding: %q", got)
	}
	owner := w.p.GithubOrganization
	if _, sealed := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_PEM"]; sealed {
		t.Error("the key was sealed though the placement pins no version of it")
	}
	if _, recorded := w.srv.Variables[owner]["RELEASE_PLEASE_APP_PEM_VERSION"]; recorded {
		t.Error("a version was recorded though none was sealed")
	}
	if client := w.srv.OrgSecrets[owner]["RELEASE_PLEASE_APP_CLIENT_ID"]; client == nil || string(client.Value) != clientID(org.ReleaseApp) {
		t.Error("RELEASE_PLEASE_APP_CLIENT_ID does not hold the release app's client ID")
	}
}

// TestEngineReleaseSecretReadersWithoutTheApp: when the last library and the release
// app's record leave the placement together, the libraries that read the key are still
// taken off both secrets. A sealing needs the app; the narrowing of who reads a secret
// that stands does not.
func TestEngineReleaseSecretReadersWithoutTheApp(t *testing.T) {
	t.Parallel()

	w := bootstrapped(t, libraryPlacement(t))
	w.p.Libraries = nil
	w.p.GithubReleaseAppID, w.p.GithubReleaseAppSlug = "", ""
	if err := w.p.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	report, out := w.run(t, root.Apply, nil)
	if got := mentioning(whats(report), "takes impulse, notes, workflows off"); len(got) != 2 {
		t.Errorf("the apply takes the former libraries off %d secret(s), want both:\n%s", len(got), out)
	}
	for _, name := range []string{"RELEASE_PLEASE_APP_PEM", "RELEASE_PLEASE_APP_CLIENT_ID"} {
		if got := w.orgSecret(t, name).RepositoryIDs; len(got) != 0 {
			t.Errorf("after the apply %s is read by %v, want by no repository", name, got)
		}
	}
}

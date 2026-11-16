package deploy

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/errors/v5"
	"github.com/google/go-cmp/cmp"
)

// The versioned application secret the tests declare in harbor's code: its container in
// stg, and the JSON the plan step leaves the migrate step for a retirement over versions
// 1 and 2.
const (
	versionedContainer = "projects/p-stg/secrets/imp-stg-gbl-quill-secret-key"
	retireVersionsJSON = `{"APP_SECRET_KEY_1":"` + versionedContainer + `/versions/1","APP_SECRET_KEY_2":"` + versionedContainer + `/versions/2"}`
)

// versionedConfig is harbor's data-level configuration with an application secret
// declared secret:"versioned" on the line after its cookie key, for a checkout whose code
// declares a versioned secret.
func versionedConfig(t *testing.T) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "derive", "testdata", "harbor", configPath))
	if err != nil {
		t.Fatal(err)
	}
	const cookie = "`env:\"APP_COOKIE_KEY\" secret:\"true\"`\n"
	if !strings.Contains(string(data), cookie) {
		t.Fatalf("harbor's %s lacks %s", configPath, cookie)
	}

	return strings.Replace(string(data), cookie, cookie+"\tSecretKey string `env:\"APP_SECRET_KEY\" secret:\"versioned\"`\n", 1)
}

// retireSubs is stg's tag build with the records bucket and a retirement of the version
// of the variable, asked for by octocat.
func retireSubs(variable, version string) map[string]string {
	subs := tagSubs()
	subs[recordsBucket] = "records"
	subs[tagSub] = "v1.2.3"
	subs[retireVariableSub], subs[retireVersionSub], subs[requesterSub] = variable, version, "octocat"

	return subs
}

// liveHolding is stg's live record of v1.2.3 naming the versions of the application
// secret its revisions hold; none names none, as a record written before the secret was
// versioned does.
func liveHolding(versions ...string) map[string]string {
	record := Record{App: "quill", Env: stgEnvironment, Version: "v1.2.3", Status: Live, Timestamp: "2026-09-28T05:30:00Z", Build: "b-5"}
	if len(versions) > 0 {
		record.VersionedSecrets = map[string][]string{"APP_SECRET_KEY": versions}
	}
	data, _ := json.Marshal(record)

	return map[string]string{"gs://records/quill/stg/3c4d/releases/v1.2.3/b-5.json": string(data)}
}

func TestRetirementOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		subs    map[string]string
		want    *Retirement
		wantErr string
	}{
		{name: "a build that carries none", subs: map[string]string{requesterSub: "octocat"}},
		{
			name: "the variable, the version and who asked",
			subs: map[string]string{retireVariableSub: "APP_SECRET_KEY", retireVersionSub: "12", requesterSub: "octocat"},
			want: &Retirement{Variable: "APP_SECRET_KEY", Version: "12", Requester: "octocat"},
		},
		{
			name:    "a version without a variable",
			subs:    map[string]string{retireVersionSub: "1", requesterSub: "octocat"},
			wantErr: `_RETIRE_SECRET="" with _RETIRE_VERSION="1": a retirement names the versioned secret's variable and the version to retire, both`,
		},
		{
			name:    "a variable of the wrong shape",
			subs:    map[string]string{retireVariableSub: "secret-key", retireVersionSub: "1", requesterSub: "octocat"},
			wantErr: `_RETIRE_SECRET "secret-key" is not a secret's variable: upper snake case under the APP_ prefix, as the code declares it (APP_SECRET_KEY)`,
		},
		{
			name:    "a version that is not a number",
			subs:    map[string]string{retireVariableSub: "APP_SECRET_KEY", retireVersionSub: "latest", requesterSub: "octocat"},
			wantErr: `_RETIRE_VERSION "latest" is not a secret version: a positive integer, as Secret Manager numbers them`,
		},
		{
			name:    "version zero",
			subs:    map[string]string{retireVariableSub: "APP_SECRET_KEY", retireVersionSub: "0", requesterSub: "octocat"},
			wantErr: `_RETIRE_VERSION "0" is not a secret version`,
		},
		{
			name:    "nobody asked",
			subs:    map[string]string{retireVariableSub: "APP_SECRET_KEY", retireVersionSub: "1"},
			wantErr: "_RETIRE_SECRET=APP_SECRET_KEY names no requester (_REQUESTER): a retirement says who asked for it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := retirementOf(tt.subs)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("retirementOf() error = %v, wantErr %q", err, tt.wantErr)
				}

				return
			}
			if err != nil {
				t.Fatalf("retirementOf() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("retirementOf() (-want +got):\n%s", diff)
			}
		})
	}
}

// TestPlanVersionedSecrets: the tag build's plan step over a checkout whose code declares
// a versioned secret. It lists the container's enabled versions as the apply identity,
// tells the plan every one and leaves them for the record; an empty container gets its
// first version as a generated secret's does. With a retirement it refuses, before the
// plan and with nothing changed, a version that is not enabled, the newest, any version
// while the live release does not hold the newest, a run with no live release and a
// variable that is not versioned; a retirement that may run tells the plan every enabled version but
// the one retiring and leaves the migrate step all of them.
func TestPlanVersionedSecrets(t *testing.T) {
	t.Parallel()

	const (
		initLine  = "tofu init -input=false -no-color -backend-config=prefix=3-app/quill/stg -backend-config=impersonate_service_account=quill-apply@p-stg.iam.gserviceaccount.com"
		planLine  = "tofu plan -input=false -no-color -out=WS/stack.plan -var environment=stg -var database_generation=1"
		showLine  = "tofu show -json WS/stack.plan"
		stateLine = "tofu show -json"
	)
	// both are versions 1 and 2 enabled and version 3 disabled, beside the pinned secrets.
	both := func() map[string]string {
		pins := enabledPins()
		pins[versionedContainer+"/versions/1"], pins[versionedContainer+"/versions/2"], pins[versionedContainer+"/versions/3"] = enabledState, enabledState, "DISABLED"

		return pins
	}
	// three are versions 1, 2 and 3 enabled: a rotation after the one that added version 2.
	three := func() map[string]string {
		pins := both()
		pins[versionedContainer+"/versions/3"] = enabledState

		return pins
	}
	tests := []struct {
		name string
		subs map[string]string
		// states are the secret versions that exist; containers the ones with no version;
		// objects the records bucket's objects before the run.
		states     map[string]string
		containers map[string]map[string]string
		objects    map[string]string
		wantOut    []string
		wantTofu   []string
		// wantFacts are facts the step left (an empty value: not left); wantSecrets what the
		// step did to Secret Manager.
		wantFacts   map[string]string
		wantSecrets []string
		wantErr     string
	}{
		{
			name:      "every enabled version is listed as the apply identity, told to the plan and left for the record; a disabled one is not",
			subs:      tagSubs(),
			states:    both(),
			wantOut:   []string{"APP_SECRET_KEY: versions 1 and 2 of imp-stg-gbl-quill-secret-key are enabled, and a process receives every one (a versioned secret: APP_SECRET_KEY_1, APP_SECRET_KEY_2; the highest number is the current version)."},
			wantTofu:  []string{initLine, stateLine, planLine + ` -var versioned_secret_versions={APP_SECRET_KEY=["1", "2"]}`, showLine},
			wantFacts: map[string]string{versionedFact: "APP_SECRET_KEY=1 2", generatedFact: "", retireVersionsFact: ""},
		},
		{
			name:       "an empty container gets its first version as the apply identity",
			subs:       tagSubs(),
			states:     enabledPins(),
			containers: map[string]map[string]string{versionedContainer: {}},
			wantOut:    []string{"APP_SECRET_KEY: added version 1 of imp-stg-gbl-quill-secret-key as quill-apply@p-stg.iam.gserviceaccount.com (32 random bytes, base64, never printed): its first version, which a process receives as APP_SECRET_KEY_1; a rotation (bedrock secret rotate stg APP_SECRET_KEY) adds the next."},
			wantTofu:   []string{initLine, stateLine, planLine + ` -var versioned_secret_versions={APP_SECRET_KEY=["1"]}`, showLine},
			wantFacts:  map[string]string{versionedFact: "APP_SECRET_KEY=1"},
			wantSecrets: []string{
				"grant " + versionedContainer + " roles/secretmanager.secretVersionAdder serviceAccount:quill-apply@p-stg.iam.gserviceaccount.com",
				"add " + versionedContainer + "/versions/1 (32 random bytes, base64)",
			},
		},
		{
			name:    "a retirement tells the plan every enabled version but the one retiring, and leaves the migrate step all of them",
			subs:    retireSubs("APP_SECRET_KEY", "1"),
			states:  both(),
			objects: liveHolding("1", "2"),
			wantOut: []string{
				"APP_SECRET_KEY: versions 1 and 2 of imp-stg-gbl-quill-secret-key are enabled",
				"Retirement: version 1 of APP_SECRET_KEY is taken out of use, asked for by octocat: the migrate command rewrites every stored value that names it and confirms none is left, the revision deploys without it, and the operations workflow then disables it in Secret Manager. The plan hands the revision version 2 of imp-stg-gbl-quill-secret-key (APP_SECRET_KEY_2) and not version 1; the migrate command still receives it, to open what it rewrites.",
			},
			wantTofu:    []string{initLine, stateLine, planLine + ` -var versioned_secret_versions={APP_SECRET_KEY=["2"]}`, showLine},
			wantFacts:   map[string]string{versionedFact: "APP_SECRET_KEY=2", retireVersionsFact: retireVersionsJSON},
			wantSecrets: []string{},
		},
		{
			name:     "a version that is not enabled is refused before the plan",
			subs:     retireSubs("APP_SECRET_KEY", "3"),
			states:   both(),
			objects:  liveHolding("1", "2"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: version 3 of imp-stg-gbl-quill-secret-key is not enabled in stg (versions 1 and 2 of it are enabled): no process holds a version that is not enabled, so there is nothing to retire, and nothing was changed. Name an enabled version other than the newest",
		},
		{
			name:     "a version that does not exist is refused the same way",
			subs:     retireSubs("APP_SECRET_KEY", "9"),
			states:   both(),
			objects:  liveHolding("1", "2"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: version 9 of imp-stg-gbl-quill-secret-key is not enabled in stg (versions 1 and 2 of it are enabled)",
		},
		{
			name:     "the newest enabled version is refused, naming the rotation that comes first",
			subs:     retireSubs("APP_SECRET_KEY", "2"),
			states:   both(),
			objects:  liveHolding("1", "2"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: version 2 of imp-stg-gbl-quill-secret-key is its newest enabled version in stg, the one every value is written under: retiring it would leave stg writing under a version it no longer holds, and nothing was changed. Rotate first (bedrock secret rotate stg APP_SECRET_KEY), which adds the next version and runs the live release on it, then retire 2",
		},
		{
			name:     "the only enabled version is the newest",
			subs:     retireSubs("APP_SECRET_KEY", "1"),
			states:   map[string]string{cookieVersion: enabledState, dbVersion: enabledState, versionedContainer + "/versions/1": enabledState},
			objects:  liveHolding("1"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: version 1 of imp-stg-gbl-quill-secret-key is its newest enabled version in stg",
		},
		{
			name:     "a live release that does not hold the newest version is refused: a rotation whose run did not deploy left it writing under the version retiring",
			subs:     retireSubs("APP_SECRET_KEY", "1"),
			states:   both(),
			objects:  liveHolding("1"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: the release live in stg (v1.2.3) does not hold version 2 of APP_SECRET_KEY, the newest enabled (its record names version 1): this run rewrites every value under version 2, which the revision serving now could not open, and that revision may still write under version 1. Nothing was changed. Run the live release again first (bedrock rerun stg v1.2.3), so that it takes up version 2, then retire 1",
		},
		{
			name:     "a live release that holds a newer version, and not the newest, is refused: it could not open what the rewrite writes",
			subs:     retireSubs("APP_SECRET_KEY", "1"),
			states:   three(),
			objects:  liveHolding("1", "2"),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: the release live in stg (v1.2.3) does not hold version 3 of APP_SECRET_KEY, the newest enabled (its record names versions 1 and 2): this run rewrites every value under version 3, which the revision serving now could not open",
		},
		{
			name:     "a live release whose record names no version of the secret is refused the same way",
			subs:     retireSubs("APP_SECRET_KEY", "1"),
			states:   both(),
			objects:  liveHolding(),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: the release live in stg (v1.2.3) does not hold version 2 of APP_SECRET_KEY, the newest enabled (its record names none)",
		},
		{
			name:     "a retirement with no release live is refused",
			subs:     retireSubs("APP_SECRET_KEY", "1"),
			states:   both(),
			wantTofu: []string{initLine},
			wantErr:  "Build REJECTED: no release is live in stg by its deployment records: a version is retired by the live release, whose migrate command rewrites the values that name it, and nothing was changed. Release first, then retire version 1 of APP_SECRET_KEY",
		},
		{
			name:     "a variable the release does not declare versioned is refused, naming the ones it does",
			subs:     retireSubs("APP_COOKIE_KEY", "1"),
			states:   both(),
			objects:  liveHolding("1", "2"),
			wantTofu: []string{initLine},
			wantErr:  `Build REJECTED: APP_COOKIE_KEY is not a secret this release declares secret:"versioned" (declared so: APP_SECRET_KEY): a version is retired of a secret whose every enabled version the service holds, and nothing was changed`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := map[string]string{
				EnvironmentFile: "export SKIP_DEPLOY=\"\"\n", BuildFile: buildFor(t, tt.subs),
				placementPath: testPlacement(""), tfvarsPath: pinnedTfvars(allPinned, allPinned, allPinned), configPath: versionedConfig(t),
			}
			w := applicationWorkspace(t, files)
			run := &fakeRunner{outputs: map[string]string{"tofu show": stackPlanJSON, "tofu show -json": emptyStateJSON}}
			secrets := &fakeSecrets{states: tt.states, containers: tt.containers}
			store := &memoryStore{objects: withStacks(tt.objects)}
			store.objects[stackKey("records", "quill", "stg")] = stackJSON("3c4d")
			clients := &Clients{Exec: run, SecretsAs: secrets.openAs, SpannerAs: (&fakeSpanner{}).open, Storage: store.open, Sleep: noSleep, Now: func() time.Time { return time.Date(2026, 10, 5, 4, 30, 0, 0, time.UTC) }}
			var out strings.Builder
			err := PlanEnvironmentStack(t.Context(), clients, w, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("PlanEnvironmentStack() error = %v, want %q; output:\n%s", err, tt.wantErr, out.String())
				}
			} else if err != nil {
				t.Fatalf("PlanEnvironmentStack() error = %v; output:\n%s", err, out.String())
			}
			containsAll(t, out.String(), tt.wantOut...)
			lines := strings.ReplaceAll(strings.Join(run.lines(), "\n"), string(w), "WS")
			if diff := cmp.Diff(strings.Join(tt.wantTofu, "\n"), lines); diff != "" {
				t.Errorf("tofu (-want +got):\n%s", diff)
			}
			env, err := w.Environment()
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantErr != "" {
				for _, fact := range []string{stackPlanFact, versionedFact, retireVersionsFact} {
					if env[fact] != "" {
						t.Errorf("a refused run left %s = %q", fact, env[fact])
					}
				}
				if len(secrets.events) > 0 {
					t.Errorf("a refused run changed Secret Manager: %v", secrets.events)
				}
			}
			for fact, want := range tt.wantFacts {
				if env[fact] != want {
					t.Errorf("%s = %q, want %q", fact, env[fact], want)
				}
			}
			if tt.wantSecrets != nil {
				if diff := cmp.Diff(tt.wantSecrets, append([]string{}, secrets.events...)); diff != "" {
					t.Errorf("Secret Manager (-want +got):\n%s", diff)
				}
			}
		})
	}
}

// TestMigrateRetirement: the migrate step of a build that carries a retirement. After the
// migrations it runs the release's migrate command once more with -retire-secret and the
// variable the retiring version is handed over under, with every enabled version of the
// secret in its environment beside the stack's variables, each read as the deploy
// identity; an exit 0 leaves the retirement for the record, and any other stops the
// build with nothing left. A build that would skip the migrations, one whose plan step
// left no versions, and a retirement beside a migration operation stop before any
// revision could deploy without the version.
func TestMigrateRetirement(t *testing.T) {
	t.Parallel()

	const (
		versionOne = "projects/tst-project/secrets/imp-tst-gbl-harbor-secret-key/versions/1"
		versionTwo = "projects/tst-project/secrets/imp-tst-gbl-harbor-secret-key/versions/2"
	)
	retiring := "export " + retireVersionsFact + "=" + doubleQuote(`{"APP_SECRET_KEY_1":"`+versionOne+`","APP_SECRET_KEY_2":"`+versionTwo+`"}`) + "\n"
	// handed is the environment of a retire build whose stack hands the migrate command
	// the version that stays, as a versioned secret at a level the command constructs.
	handed := strings.Replace(migrateEnvironment, `export MIGRATE_SECRETS="{}"`, "export MIGRATE_SECRETS="+doubleQuote(`{"APP_SECRET_KEY_2":"`+versionTwo+`"}`), 1) + retiring
	build := func(extra map[string]string) string {
		subs := map[string]string{"_PROJECT": "tst-project", "_ENV": "tst", "COMMIT_SHA": "deadbeef", "REPO_NAME": "harbor", "_PR_NUMBER": "", requesterSub: "octocat", retireVariableSub: "APP_SECRET_KEY", retireVersionSub: "1"}
		for name, value := range extra {
			subs[name] = value
		}

		return buildFor(t, subs)
	}
	payloads := map[string]string{versionOne: "key-one", versionTwo: "key-two"}
	baseVars := []string{"APP_SERVICE_NAME=harbor-migrate", "GOOGLE_CLOUD_SPANNER_DATABASE_NAME=imp-tst-gbl-harbor-db", "GOOGLE_CLOUD_SPANNER_PROJECT=tst-project"}
	// withKeys are the variables of a run that received the versions: sorted by name, the
	// secret's before the service's name.
	withKeys := func(keys ...string) []string {
		return append(append([]string{}, keys...), baseVars...)
	}
	tests := []struct {
		name  string
		env   string
		build string
		// fail fails the command's runs by their first argument, or every run without one.
		fail    map[string]error
		secrets *fakeSecrets
		wantOut []string
		// wantRuns are the command's runs, each its arguments and then its variables.
		wantRuns  [][]string
		wantFacts map[string]string
		wantErr   string
	}{
		{
			name:    "the migrations run as always, then the command once more with the flag and every enabled version",
			env:     handed,
			build:   build(nil),
			secrets: &fakeSecrets{payloads: payloads},
			wantOut: []string{
				"=== Running the migrate command ===",
				"=== Retirement, asked for by octocat: running the migrate command with -retire-secret APP_SECRET_KEY_1 ===",
				"It runs with every enabled version of APP_SECRET_KEY, read as the deploy identity (APP_SECRET_KEY_1, APP_SECRET_KEY_2; the values are not printed), rewrites every stored value that names version 1 under the newest, and confirms none is left by exiting 0.",
				"No stored value names version 1 of APP_SECRET_KEY any more, by the migrate command's word",
				"the operations workflow disables it in Secret Manager once the build has succeeded; it is never destroyed here.",
			},
			wantRuns: [][]string{
				append([]string{"|"}, withKeys("APP_SECRET_KEY_2=key-two")...),
				append([]string{retireArg, "APP_SECRET_KEY_1", "|"}, withKeys("APP_SECRET_KEY_1=key-one", "APP_SECRET_KEY_2=key-two")...),
			},
			wantFacts: map[string]string{retiredVariableFact: "APP_SECRET_KEY", retiredVersionFact: "1"},
		},
		{
			name:    "a versioned secret the migrate command does not otherwise read still reaches the rewrite",
			env:     migrateEnvironment + retiring,
			build:   build(nil),
			secrets: &fakeSecrets{payloads: payloads},
			wantRuns: [][]string{
				append([]string{"|"}, baseVars...),
				append([]string{retireArg, "APP_SECRET_KEY_1", "|"}, withKeys("APP_SECRET_KEY_1=key-one", "APP_SECRET_KEY_2=key-two")...),
			},
			wantFacts: map[string]string{retiredVariableFact: "APP_SECRET_KEY", retiredVersionFact: "1"},
		},
		{
			name:    "a command that does not confirm stops the build, and nothing is left for the record",
			env:     handed,
			build:   build(nil),
			fail:    map[string]error{retireArg: errors.New("exit status 1")},
			secrets: &fakeSecrets{payloads: payloads},
			wantOut: []string{"=== Retirement, asked for by octocat: running the migrate command with -retire-secret APP_SECRET_KEY_1 ==="},
			wantRuns: [][]string{
				append([]string{"|"}, withKeys("APP_SECRET_KEY_2=key-two")...),
				append([]string{retireArg, "APP_SECRET_KEY_1", "|"}, withKeys("APP_SECRET_KEY_1=key-one", "APP_SECRET_KEY_2=key-two")...),
			},
			wantFacts: map[string]string{retiredVariableFact: "", retiredVersionFact: ""},
			wantErr:   "the release's migrate command did not confirm that no stored value names version 1 of APP_SECRET_KEY (-retire-secret APP_SECRET_KEY_1, after 0s; its lines are above: exit status 1). Nothing is retired: the version stays enabled, this run deploys no revision, and the next release hands the version to the service as before. A release whose migrate command does not know -retire-secret was built before the framework took it: release on a framework that has it, then retire",
		},
		{
			name:      "migrations that fail stop the build before the rewrite",
			env:       handed,
			build:     build(nil),
			fail:      map[string]error{"": errors.New("exit status 1")},
			secrets:   &fakeSecrets{payloads: payloads},
			wantRuns:  [][]string{append([]string{"|"}, withKeys("APP_SECRET_KEY_2=key-two")...)},
			wantFacts: map[string]string{retiredVariableFact: ""},
			wantErr:   "the migration failed after 0s",
		},
		{
			name:      "a version the deploy identity cannot read stops the build before the rewrite",
			env:       migrateEnvironment + retiring,
			build:     build(nil),
			secrets:   &fakeSecrets{payloads: map[string]string{versionTwo: "key-two"}},
			wantRuns:  [][]string{append([]string{"|"}, baseVars...)},
			wantFacts: map[string]string{retiredVariableFact: ""},
			wantErr:   versionOne + ": the retirement's APP_SECRET_KEY_1",
		},
		{
			name:      "a build whose plan step left no versions rewrites nothing and stops",
			env:       migrateEnvironment,
			build:     build(nil),
			wantRuns:  [][]string{append([]string{"|"}, baseVars...)},
			wantFacts: map[string]string{retiredVariableFact: ""},
			wantErr:   "environment.sh names no versions for the retirement of APP_SECRET_KEY (RETIRE_SECRET_VERSIONS): the stack's plan step leaves them, and without them nothing is rewritten, so the run stops before a revision without version 1 deploys",
		},
		{
			name:     "a build that runs no migrations and carries a retirement stops, where it would otherwise skip",
			env:      strings.Replace(handed, `RUN_MIGRATIONS="true"`, `RUN_MIGRATIONS="false"`, 1),
			build:    build(nil),
			wantRuns: [][]string{},
			wantErr:  "this build runs no migrations, and it carries a retirement (_RETIRE_SECRET=APP_SECRET_KEY, _RETIRE_VERSION=1): the migrate command is what rewrites the values that name the version, so the run stops here, before a revision without the version deploys",
		},
		{
			name:     "a retirement beside a migration operation is refused before anything runs",
			env:      handed,
			build:    build(map[string]string{migrateActionSub: actionRerun}),
			wantRuns: [][]string{},
			wantErr:  "_RETIRE_SECRET=APP_SECRET_KEY with _MIGRATE_ACTION=rerun: the run that retires a version does nothing else",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := workspaceFiles(t, map[string]string{EnvironmentFile: tt.env, BuildFile: tt.build})
			program := migrateProgramFile(t)
			var out strings.Builder
			run := &fakeRunner{effect: func(c Command) error {
				first := ""
				if len(c.Args) > 0 {
					first = c.Args[0]
				}

				return tt.fail[first]
			}}
			clients := &Clients{Exec: run, Sleep: func(context.Context, time.Duration) error { return nil }}
			if tt.secrets != nil {
				clients.Secrets = tt.secrets.open
			}
			err := Migrate(t.Context(), clients, w, program, "", false, &out)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Migrate() error = %v, wantErr %q; output:\n%s", err, tt.wantErr, out.String())
				}
			} else if err != nil {
				t.Fatalf("Migrate() error = %v; output:\n%s", err, out.String())
			}
			at := 0
			for _, want := range tt.wantOut {
				i := strings.Index(out.String()[at:], want)
				if i < 0 {
					t.Errorf("output lacks %q in its turn:\n%s", want, out.String())

					continue
				}
				at += i + len(want)
			}
			for _, value := range payloads {
				if strings.Contains(out.String(), value) {
					t.Errorf("the output prints a version's value (%s):\n%s", value, out.String())
				}
			}
			got := [][]string{}
			for _, c := range run.ran {
				if c.Name == program {
					got = append(got, append(append(append([]string{}, c.Args...), "|"), c.Env...))
				}
			}
			if diff := cmp.Diff(tt.wantRuns, got); diff != "" {
				t.Errorf("the command's runs, arguments | variables (-want +got):\n%s", diff)
			}
			env, err := w.Environment()
			if err != nil {
				t.Fatal(err)
			}
			for name, want := range tt.wantFacts {
				if env[name] != want {
					t.Errorf("fact %s = %q, want %q", name, env[name], want)
				}
			}
		})
	}
}

// TestRecordVersionedSecrets: the deployment record names the versions of each versioned
// secret the run's revisions receive, oldest first, and a retirement the run made: the
// variable, the version and who asked. A run that listed none and retired nothing names
// neither.
func TestRecordVersionedSecrets(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	rerun := `{"id": "b-1", "substitutions": {"_APP": "harbor", "_ENV": "tst", "_RECORDS_BUCKET": "records", "COMMIT_SHA": "deadbeef", "_REQUESTER": "octocat", "_RETIRE_SECRET": "APP_SECRET_KEY", "_RETIRE_VERSION": "1"}}`
	tests := []struct {
		name          string
		env           string
		build         string
		wantVersioned map[string][]string
		wantRetired   *RetiredSecret
	}{
		{name: "a run that listed none", env: liveEnvironment, build: buildJSON},
		{
			name:          "the versions the revisions receive, by variable",
			env:           liveEnvironment + "export VERSIONED_SECRETS=\"APP_LEDGER_KEY=4,APP_SECRET_KEY=1 2 11\"\n",
			build:         buildJSON,
			wantVersioned: map[string][]string{"APP_LEDGER_KEY": {"4"}, "APP_SECRET_KEY": {"1", "2", "11"}},
		},
		{
			name:          "a retirement the application confirmed, beside the versions that stay",
			env:           liveEnvironment + "export VERSIONED_SECRETS=\"APP_SECRET_KEY=2\"\nexport RETIRED_SECRET=\"APP_SECRET_KEY\"\nexport RETIRED_VERSION=\"1\"\n",
			build:         rerun,
			wantVersioned: map[string][]string{"APP_SECRET_KEY": {"2"}},
			wantRetired:   &RetiredSecret{Variable: "APP_SECRET_KEY", Version: "1", Requester: "octocat"},
		},
		{
			name:          "a retirement the build carried and the migrate step did not confirm is not recorded",
			env:           liveEnvironment + "export VERSIONED_SECRETS=\"APP_SECRET_KEY=2\"\n",
			build:         rerun,
			wantVersioned: map[string][]string{"APP_SECRET_KEY": {"2"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &memoryStore{objects: map[string]string{stackKey("records", "harbor", "tst"): stackJSON("1a2b")}}
			req, err := NewRecordRequest(t.Context(), store.open, workspaceFiles(t, map[string]string{EnvironmentFile: tt.env, BuildFile: tt.build, RevisionsFile: revisionsLines}), now)
			if err != nil {
				t.Fatalf("NewRecordRequest() error = %v", err)
			}
			if diff := cmp.Diff(tt.wantVersioned, req.Record.VersionedSecrets); diff != "" {
				t.Errorf("VersionedSecrets (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tt.wantRetired, req.Record.RetiredSecret); diff != "" {
				t.Errorf("RetiredSecret (-want +got):\n%s", diff)
			}
			data, err := json.Marshal(req.Record)
			if err != nil {
				t.Fatal(err)
			}
			if has := strings.Contains(string(data), `"versionedSecrets"`); has != (tt.wantVersioned != nil) {
				t.Errorf("the record's JSON carries versionedSecrets: %t", has)
			}
			if has := strings.Contains(string(data), `"retiredSecret"`); has != (tt.wantRetired != nil) {
				t.Errorf("the record's JSON carries retiredSecret: %t", has)
			}
		})
	}
}

// TestVersionedPinsWithoutSeed: a plan that seeds nothing (a pull request's, or an
// environment's first apply) lists a versioned secret's enabled versions the same way,
// and leaves one with no enabled version out, saying the next release build adds it.
func TestVersionedPinsWithoutSeed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		states  map[string]string
		want    map[string][]string
		wantOut string
	}{
		{
			name:    "the enabled versions, oldest first",
			states:  map[string]string{versionedContainer + "/versions/2": enabledState, versionedContainer + "/versions/10": enabledState, versionedContainer + "/versions/1": "DESTROYED"},
			want:    map[string][]string{"APP_SECRET_KEY": {"2", "10"}},
			wantOut: "APP_SECRET_KEY: versions 2 and 10 of imp-stg-gbl-quill-secret-key are enabled",
		},
		{
			name:    "no enabled version yet",
			states:  map[string]string{versionedContainer + "/versions/1": "DISABLED"},
			want:    map[string][]string{},
			wantOut: "APP_SECRET_KEY: imp-stg-gbl-quill-secret-key has no enabled version yet (a versioned secret, whose first version stg's next release build adds): the plan mounts nothing for it.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := applicationWorkspace(t, map[string]string{placementPath: testPlacement(""), configPath: versionedConfig(t)})
			p, err := checkoutPlacement(w, "the test reads it")
			if err != nil {
				t.Fatal(err)
			}
			m, err := checkoutModel(w, p)
			if err != nil {
				t.Fatal(err)
			}
			secrets := &fakeSecrets{states: tt.states}
			var out strings.Builder
			pins, err := generatedPins(t.Context(), &Clients{SecretsAs: secrets.openAs}, "quill-plan@p-stg.iam.gserviceaccount.com", true, m, "quill", stgEnvironment, "p-stg", false, &out)
			if err != nil {
				t.Fatalf("generatedPins() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, pins.Versioned); diff != "" {
				t.Errorf("the versioned secrets' versions (-want +got):\n%s", diff)
			}
			if len(pins.Generated) != 0 {
				t.Errorf("a versioned secret was pinned as a generated one: %v", pins.Generated)
			}
			containsAll(t, out.String(), tt.wantOut)
			if len(secrets.events) > 0 {
				t.Errorf("a plan that seeds nothing changed Secret Manager: %v", secrets.events)
			}
		})
	}
}

// TestTeardownVersionedPins: a teardown's plan is told the versioned secrets' versions the
// live release runs, read from its record, as it is told the generated secrets' pins, so
// that the apply before the destroy changes nothing else.
func TestTeardownVersionedPins(t *testing.T) {
	t.Parallel()

	record, err := json.Marshal(Record{App: "harbor", Version: "v0.1.4", Status: Live, Timestamp: "2026-10-14T20:00:00Z", GeneratedSecrets: map[string]string{"APP_COOKIE_KEY": "3"}, VersionedSecrets: map[string][]string{"APP_SECRET_KEY": {"1", "2"}}})
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{objects: withStacks(map[string]string{"gs://records/" + releaseObject("harbor", tstEnvironment, "1a2b", "v0.1.4", "b1"): string(record)})}
	teardown := &EnvironmentTeardown{req: &TeardownRequest{App: "harbor", Env: tstEnvironment}, records: "records"}
	if err := teardown.readRecords(t.Context(), store); err != nil {
		t.Fatalf("readRecords() error = %v", err)
	}
	want := []string{"-var", "environment=tst", "-var", "database_generation=1", "-var", `generated_secret_versions={APP_COOKIE_KEY="3"}`, "-var", `versioned_secret_versions={APP_SECRET_KEY=["1", "2"]}`, "-var", "teardown=true"}
	if diff := cmp.Diff(want, teardown.vars()); diff != "" {
		t.Errorf("vars() (-want +got):\n%s", diff)
	}
}

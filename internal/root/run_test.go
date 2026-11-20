package root_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/create"
	"github.com/impulseframework/bedrock/internal/deploy"
	"github.com/impulseframework/bedrock/internal/github"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// fakeBuilds is Cloud Build in the run tests: one build, described by its JSON.
type fakeBuilds struct {
	build string
}

func (b *fakeBuilds) open(context.Context) (deploy.Builds, error) {
	return b, nil
}

func (b *fakeBuilds) Get(context.Context, string, string, string) ([]byte, error) {
	return []byte(b.build), nil
}

func (b *fakeBuilds) ReadToken(context.Context, string) (string, error) {
	return "", errors.New("no read token in this test")
}

// applied is the world after the bootstrap's apply, with the placement written to a
// checkout and the run's clients: what a trigger's run starts from.
func applied(t *testing.T) (*world, *root.RunClients, string) {
	t.Helper()

	w := newWorld(t)
	w.run(t, root.Apply, true)
	dir := withRecord(t, t.TempDir())
	if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	rc := &root.RunClients{Secrets: func(context.Context) (deploy.Secrets, error) { return w.fake, nil }}

	return w, rc, dir
}

// build is a trigger's build, with the substitutions the steps pass.
func build(p *org.Placement, pr string) string {
	subs := map[string]string{"_BEDROCK_STEPS": org.StepsShape(p), "COMMIT_SHA": "0123456789abcdef0123456789abcdef01234567"}
	if pr != "" {
		subs["_PR_NUMBER"] = pr
	}
	data, _ := json.Marshal(map[string]any{"id": "b-1", "substitutions": subs})

	return string(data)
}

// TestPlanRun: the plan trigger's run reads the pull request's placement, says what the
// merge would change, and posts it once on the pull request as the deployer app, updating
// the same comment on a later push.
func TestPlanRun(t *testing.T) {
	t.Parallel()

	w, rc, dir := applied(t)
	// The pull request adds a domain.
	w.p.Domains["harbor.example"] = org.Domain{}
	if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	rc.Builds = (&fakeBuilds{build: build(w.p, "7")}).open
	req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}
	var out bytes.Buffer
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); err != nil {
		t.Fatalf("PlanRun() error = %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"zones: the apply creates the zone imr-root-gbl-dns-harbor-example for harbor.example",
		"Posted the report on pull request #7 as the deployer app.",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	repo := w.srv.Repos[w.p.GithubOrganization+"/"+w.p.SourceRepo]
	comments := repo.Comments[7]
	if len(comments) != 1 || !strings.HasPrefix(comments[0].Body, "<!-- bedrock org plan -->\n### what the merge changes in the imr root organization") || !strings.Contains(comments[0].Body, "- zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example\n") {
		t.Fatalf("the pull request's comments: %+v", comments)
	}
	if strings.Contains(comments[0].Body, "Nothing:") {
		t.Errorf("the comment says nothing changes:\n%s", comments[0].Body)
	}

	// A later push: the same comment, updated.
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); err != nil {
		t.Fatalf("PlanRun() again error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Updated the report on pull request #7 as the deployer app.") || len(repo.Comments[7]) != 1 {
		t.Errorf("the second run: %d comment(s)\n%s", len(repo.Comments[7]), out.String())
	}
}

// TestPlanRunRefusals: a build that did not start from the trigger, one without the pull
// request, and a hosting organization's checkout are refused.
func TestPlanRunRefusals(t *testing.T) {
	t.Parallel()

	w, rc, dir := applied(t)
	req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}
	tests := []struct {
		name    string
		build   string
		req     *root.RunRequest
		wantErr string
	}{
		{name: "no steps substitution", build: `{"id":"b-1","substitutions":{"COMMIT_SHA":"abc","_PR_NUMBER":"7"}}`, req: req, wantErr: "no _BEDROCK_STEPS substitution: bedrock org plan runs from the plan trigger the bootstrap made"},
		{name: "no pull request", build: build(w.p, ""), req: req, wantErr: `_PR_NUMBER is "": the plan trigger runs on a pull request`},
		{name: "no build named", build: build(w.p, "7"), req: &root.RunRequest{Dir: dir}, wantErr: "is not set: bedrock org plan runs from the plan trigger of the root organization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rc := &root.RunClients{Builds: (&fakeBuilds{build: tt.build}).open, Secrets: rc.Secrets}
			err := root.PlanRun(context.Background(), w.clients, rc, tt.req, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("PlanRun() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestApplyRun: the apply trigger's run takes the lock, makes the pushed placement so,
// leaves the marker and frees the lock; a lock held by another run is refused with what
// it says.
func TestApplyRun(t *testing.T) {
	t.Parallel()

	w, rc, dir := applied(t)
	w.p.Domains["harbor.example"] = org.Domain{}
	if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	rc.Builds = (&fakeBuilds{build: build(w.p, "")}).open
	req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}
	var out bytes.Buffer
	if err := root.ApplyRun(context.Background(), w.clients, rc, req, &out); err != nil {
		t.Fatalf("ApplyRun() error = %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example") {
		t.Errorf("output:\n%s", out.String())
	}
	if _, ok := w.fake.ZoneMap[w.p.Projects[org.RootProjectKey]]["imr-root-gbl-dns-harbor-example"]; !ok {
		t.Errorf("the zone was not made: %v", w.fake.ZoneMap)
	}
	if len(w.fake.Locked) != 0 {
		t.Errorf("the lock is still held: %v", w.fake.Locked)
	}
	marker, err := w.fake.Read(context.Background(), w.p.StateBucket, root.MarkerObject)
	if err != nil || !strings.Contains(string(marker), `"commit": "0123456789abcdef0123456789abcdef01234567"`) {
		t.Errorf("the marker: %s, %v", marker, err)
	}
	lockKey := w.p.StateBucket + "/" + root.LockObject
	if !slices.Contains(w.fake.Calls, "Lock("+w.p.StateBucket+", "+root.LockObject+")") || !slices.Contains(w.fake.Calls, "Unlock("+w.p.StateBucket+", "+root.LockObject+")") {
		t.Errorf("the lock was not taken and freed: %v", w.fake.Calls)
	}

	// Another run holds the lock.
	w.fake.Locked[lockKey] = []byte(`{"build":"b-0","commit":"fedcba9876543210","since":"2026-11-05T01:02:03Z"}`)
	err = root.ApplyRun(context.Background(), w.clients, rc, req, &bytes.Buffer{})
	if want := "another apply holds the lock gs://" + w.p.StateBucket + "/" + root.LockObject + " (build b-0 at fedcba9, since 2026-11-05T01:02:03Z): wait for it"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("ApplyRun() under a lock error = %v, want %q", err, want)
	}
}

// The sentences of a run that stopped on an error, as the log and the plan's comment say
// them.
const (
	stoppedLine    = "The run stopped before it finished, at the error below: what it had gathered before the stop follows, and it is incomplete.\n"
	stoppedComment = "**The plan stopped before it finished, at the error below: what it had gathered before the stop follows, and it is incomplete.**\n\n"
)

// finishedVerdict is the last line of a report that finished, which a stopped run never
// prints: the count of a plan or a check, and an apply's Applied.
var finishedVerdict = regexp.MustCompile(`(?m)^([0-9]+ change\(s\) the apply would make|Applied [0-9]+ change\(s\);|The root organization stands as placement\.json says)`)

// TestPlanRunStopped: a plan that stops on an error in one of the engine's sections says
// in the log what it had gathered before the stop, as incomplete, and puts the same with
// the error into the plan's one comment on the pull request, in the place of an earlier
// plan's; the answer is the run's error. A plan that had gathered nothing posts the stop
// and the error alone, and so does a plan refused before its first section, which has no
// report. Where the comment cannot be posted the log says so and claims no comment. The
// next plan that finishes takes the comment's place again.
func TestPlanRunStopped(t *testing.T) {
	t.Parallel()

	w, rc, dir := applied(t)
	w.p.Domains["harbor.example"] = org.Domain{}
	if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	rc.Builds = (&fakeBuilds{build: build(w.p, "7")}).open
	req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}
	var out bytes.Buffer
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); err != nil {
		t.Fatalf("PlanRun() error = %v\n%s", err, out.String())
	}
	repo := w.srv.Repos[w.p.GithubOrganization+"/"+w.p.SourceRepo]
	zone := "zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example"

	// The budgets cannot be listed: the sections before them had gathered the zone.
	stop := errors.New("the budgets of the billing account could not be listed in this test")
	w.fake.Refuse["Budgets"] = stop
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); !errors.Is(err, stop) {
		t.Fatalf("PlanRun() of a plan that stops = %v, want the stop's error\n%s", err, out.String())
	}
	for _, want := range []string{
		stoppedLine + "zones: the apply creates the zone imr-root-gbl-dns-harbor-example for harbor.example\n",
		"\nBefore the stop: ",
		" change(s) the apply would make; 0 finding(s).\nUpdated the report on pull request #7 as the deployer app.\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the stopped plan's output lacks %q:\n%s", want, out.String())
		}
	}
	if verdict := finishedVerdict.FindString(out.String()); verdict != "" {
		t.Errorf("the stopped plan printed a finished plan's verdict (%s):\n%s", verdict, out.String())
	}
	comments := repo.Comments[7]
	if len(comments) != 1 {
		t.Fatalf("the pull request's comments after the stopped plan: %+v", comments)
	}
	body := comments[0].Body
	for _, want := range []string{
		"<!-- bedrock org plan -->\n### what the merge changes in the imr root organization\n\nAt 0123456:\n\n" + stoppedComment + "- " + zone + "\n",
		"\nThe error the plan stopped at:\n\n    the budgets of the billing account could not be listed in this test\n",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the stopped plan's comment lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "Nothing:") || strings.Contains(body, "found no change") {
		t.Errorf("the stopped plan's comment says nothing changes:\n%s", body)
	}

	// GitHub fails the listing of the pull request's comments: the log alone.
	listing := "GET /repos/" + w.p.GithubOrganization + "/" + w.p.SourceRepo + "/issues/7/comments"
	w.srv.Update(func() { w.srv.Failing[listing] = http.StatusInternalServerError })
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); !errors.Is(err, stop) {
		t.Fatalf("PlanRun() of a plan that stops and cannot post = %v, want the stop's error\n%s", err, out.String())
	}
	notPosted := regexp.MustCompile(`\nThe pull request's comment was not posted \(GitHub ` + regexp.QuoteMeta(listing) + `[^ ]* answered 500: failing in this test\): the plan's stop is in this log alone\.\n$`)
	if got := out.String(); !strings.Contains(got, stoppedLine) || !notPosted.MatchString(got) || strings.Contains(got, "the report on pull request") {
		t.Errorf("the output of a stopped plan that cannot post, want it to end as %s and to claim no comment:\n%s", notPosted, got)
	}
	w.srv.Update(func() { delete(w.srv.Failing, listing) })

	// The first section cannot read: nothing was gathered, and the comment says so.
	delete(w.fake.Refuse, "Budgets")
	w.fake.Refuse["EnabledServices"] = stop
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); !errors.Is(err, stop) {
		t.Fatalf("PlanRun() of a plan that stops in its first section = %v, want the stop's error\n%s", err, out.String())
	}
	if got, want := out.String(), "Updated the report on pull request #7 as the deployer app.\n"; !strings.HasSuffix(got, want) || strings.Contains(got, "stopped") {
		t.Errorf("the output of a plan that had gathered nothing, want no report and %q:\n%s", want, got)
	}
	if body := repo.Comments[7][0].Body; !strings.Contains(body, stoppedComment+"It had found no change before the stop.\n\nThe error the plan stopped at:\n\n    the budgets") || strings.Contains(body, zone) {
		t.Errorf("the comment of a plan that had gathered nothing:\n%s", body)
	}

	// A client cannot be opened: the run is refused before its first section and has no
	// report, and the comment says the same of it with its own error.
	delete(w.fake.Refuse, "EnabledServices")
	refusal := errors.New("the Org Policy client could not be opened in this test")
	policies := w.clients.OrgPolicies
	w.clients.OrgPolicies = func(context.Context, string) (root.OrgPolicies, error) { return nil, refusal }
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); !errors.Is(err, refusal) {
		t.Fatalf("PlanRun() of a plan refused before its first section = %v, want the refusal\n%s", err, out.String())
	}
	if got, want := out.String(), "Updated the report on pull request #7 as the deployer app.\n"; !strings.HasSuffix(got, want) || strings.Contains(got, "stopped") {
		t.Errorf("the output of a plan refused before its first section, want no report and %q:\n%s", want, got)
	}
	if body := repo.Comments[7][0].Body; !strings.Contains(body, stoppedComment+"It had found no change before the stop.\n\nThe error the plan stopped at:\n\n    the Org Policy client could not be opened in this test\n") || strings.Contains(body, zone) {
		t.Errorf("the comment of a plan refused before its first section:\n%s", body)
	}
	w.clients.OrgPolicies = policies

	// The next plan finishes, and its comment takes the place.
	out.Reset()
	if err := root.PlanRun(context.Background(), w.clients, rc, req, &out); err != nil {
		t.Fatalf("PlanRun() after the stops error = %v\n%s", err, out.String())
	}
	if body := repo.Comments[7][0].Body; len(repo.Comments[7]) != 1 || strings.Contains(body, "stopped") || !strings.Contains(body, "- "+zone+"\n") {
		t.Errorf("the comment after a plan that finished (%d comment(s)):\n%s", len(repo.Comments[7]), body)
	}
}

// TestApplyRunStopped: an apply that stops on an error says what it had gathered and how
// many changes it made before the stop, never as a finished apply counts them; the change
// it stopped at is named as not among them; an apply that stops before it lists anything
// says it made none, and one refused before its first section, which has no report, says
// nothing: its error is all there is. It leaves no marker and frees the lock, and the
// answer is the run's error.
func TestApplyRunStopped(t *testing.T) {
	t.Parallel()

	stop := errors.New("refused in this test")
	tests := []struct {
		name    string
		arrange func(w *world)
		want    []string
		wantNot string
	}{
		{
			name: "a read after a change was made",
			arrange: func(w *world) {
				w.p.Domains["harbor.example"] = org.Domain{}
				w.fake.Refuse["Budgets"] = stop
			},
			// The zone, and the two identities' tokens in it.
			want: []string{
				"zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example\n",
				stoppedLine + "Applied 3 change(s) before the stop; 0 finding(s).\n",
			},
			wantNot: "The change the run stopped at",
		},
		{
			name: "a change the apply could not make",
			arrange: func(w *world) {
				project := w.p.Projects[org.RootProjectKey]
				w.fake.Enabled[project] = slices.DeleteFunc(w.fake.Enabled[project], func(s string) bool { return s == "dns.googleapis.com" })
				w.fake.Refuse["EnableServices"] = stop
			},
			want: []string{
				"APIs: enables dns.googleapis.com in imr-root-gbl-core-c3d4\n" + stoppedLine +
					"Applied 0 change(s) before the stop; 0 finding(s). The change the run stopped at is not counted, and may stand in part: APIs: enables dns.googleapis.com in imr-root-gbl-core-c3d4.\n",
			},
		},
		{
			name: "a change listed only once it is made",
			arrange: func(w *world) {
				w.p.Domains["harbor.example"] = org.Domain{}
				w.fake.Refuse["CreateZone"] = stop
			},
			want: []string{
				"zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example\n" + stoppedLine +
					"Applied 0 change(s) before the stop; 0 finding(s). The change the run stopped at is not counted, and may stand in part: zones: creates the zone imr-root-gbl-dns-harbor-example for harbor.example.\n",
			},
		},
		{
			name:    "the first read",
			arrange: func(w *world) { w.fake.Refuse["EnabledServices"] = stop },
			want:    []string{"The run stopped before it finished, at the error below: it had made no change and had no finding or note before the stop.\n"},
			wantNot: "what it had gathered",
		},
		{
			name: "a client that cannot be opened, before the first section",
			arrange: func(w *world) {
				w.clients.OrgPolicies = func(context.Context, string) (root.OrgPolicies, error) { return nil, stop }
			},
			wantNot: "The run stopped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w, rc, dir := applied(t)
			tt.arrange(w)
			if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
				t.Fatal(err)
			}
			rc.Builds = (&fakeBuilds{build: build(w.p, "")}).open
			req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}
			var out bytes.Buffer
			if err := root.ApplyRun(context.Background(), w.clients, rc, req, &out); !errors.Is(err, stop) {
				t.Fatalf("ApplyRun() of an apply that stops = %v, want the stop's error\n%s", err, out.String())
			}
			for _, want := range tt.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("the stopped apply's output lacks %q:\n%s", want, out.String())
				}
			}
			if tt.wantNot != "" && strings.Contains(out.String(), tt.wantNot) {
				t.Errorf("the stopped apply's output says %q:\n%s", tt.wantNot, out.String())
			}
			if verdict := finishedVerdict.FindString(out.String()); verdict != "" {
				t.Errorf("the stopped apply printed a finished apply's verdict (%s):\n%s", verdict, out.String())
			}
			if marker, err := w.fake.Read(context.Background(), w.p.StateBucket, root.MarkerObject); err == nil {
				t.Errorf("the stopped apply left a marker: %s", marker)
			}
			if len(w.fake.Locked) != 0 {
				t.Errorf("the lock is still held: %v", w.fake.Locked)
			}
		})
	}
}

// tofu stands in for OpenTofu in a creation: every program a creation runs succeeds, and
// tofu output answers the outputs of the foundation of the hosting organization rgb, its
// six projects and its folders.
type tofu struct {
	// folders are the folders 1-org's outputs name, by key; rgbFolders when nil.
	folders map[string]string
}

// rgbFolders are rgb's four folders as its 1-org's outputs name them: its own, not the
// ones the record of imp names.
func rgbFolders() map[string]string {
	return map[string]string{"shared": "900000000021", "tst": "900000000022", "stg": "900000000023", "prd": "900000000024"}
}

func (tofu) Run(context.Context, deploy.Command, io.Writer) error {
	return nil
}

func (o tofu) Output(_ context.Context, c deploy.Command, _ io.Writer) ([]byte, error) {
	if c.Args[0] != "output" {
		return nil, nil
	}
	folders := o.folders
	if folders == nil {
		folders = rgbFolders()
	}
	ids, numbers := map[string]string{}, map[string]string{}
	for i, key := range []string{"shr", "net", "spn", "tst", "stg", "prd"} {
		ids[key] = "rgb-" + key + "-gbl-core-e5f6"
		numbers[key] = "10000000010" + strconv.Itoa(i+1)
	}
	keys, values := map[string]string{}, map[string]string{}
	for i, env := range []string{"tst", "stg", "prd"} {
		keys[env] = "tagKeys/28100000000" + strconv.Itoa(i+1)
		values[env] = "tagValues/28200000000" + strconv.Itoa(i+1)
	}
	value := func(v any) map[string]any { return map[string]any{"value": v} }
	data, err := json.Marshal(map[string]any{
		"project_ids": value(ids), "project_numbers": value(numbers),
		"folder_ids":             value(folders),
		"public_invoker_tag_key": value(keys), "public_invoker_tag_value": value(values),
	})
	if err != nil {
		return nil, errors.Wrap(err, "json.Marshal()")
	}

	return data, nil
}

// creating is the applied world with a hosting organization to create: rgb is recorded
// without a folder in the checkout's placement, its own record is in the checkout, the
// root repository has the default branch the hand-off pull request starts from, and the
// creation's seams are wired. The creation's Google and its state bucket are the world's
// own stand-in, so the sections after the creation read what the creation made, and the
// creation's marker lies in the bucket the next apply reads.
func creating(t *testing.T) (*world, *root.RunClients, string) {
	t.Helper()

	w, rc, dir := applied(t)
	fixture, err := org.ReadPlacement("../org/testdata/imr/placement.json")
	if err != nil {
		t.Fatal(err)
	}
	w.p.HostingOrganizations = fixture.HostingOrganizations // imp created, rgb to create
	if err := w.p.Write(filepath.Join(dir, org.PlacementFile)); err != nil {
		t.Fatal(err)
	}
	record := &org.Placement{
		Prefix: "rgb", AppsDomain: "rgb.impapps.example", Spanner: org.Spanner{Config: "nam10"},
		Regions:        []org.Region{{Name: "us-central1", Code: "uc1"}, {Name: "us-west3", Code: "uw3"}},
		TeamGroups:     map[string]string{"tst": "team-tst@imp.example", "stg": "team-stg@imp.example", "prd": "team-prd@imp.example"},
		ContactDomains: []string{"@imp.example"},
		Projects:       map[string]string{"boot": "rgb-boot-gbl-core-e5f6"},
		MonthlyBudget:  500,
	}
	if err := org.Fill(w.p, record); err != nil {
		t.Fatal(err)
	}
	path := org.RecordPath(dir, "rgb")
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := record.Write(path); err != nil {
		t.Fatal(err)
	}
	w.srv.Update(func() {
		repo := w.srv.Repos[w.p.GithubOrganization+"/"+w.p.SourceRepo]
		repo.Refs["refs/heads/"+w.p.GithubDefaultBranch] = github.Object{Type: "commit", SHA: "base1"}
		repo.Ancestry = map[string][]string{"base1": {"base1"}}
		repo.Trees = map[string]string{"base1": "tree0"}
		repo.Files = map[string]string{"tree0:placement.json": "{}"}
	})
	w.clients.Create = &create.Clients{
		Google: func(context.Context, string) (create.Google, error) { return w.fake, nil },
		GitHub: w.clients.GitHub,
		Exec:   tofu{},
		Store:  w.fake,
		Sleep:  func(context.Context, time.Duration) error { return nil },
	}

	return w, rc, dir
}

// TestApplyRunCreated: an apply that creates a hosting organization names it and its
// hand-off pull request after its report, when it finishes and when a section after the
// creation stops it: the organization stands either way, and its pull request waits. The
// next push's apply finds the organization created and names the same pull request.
func TestApplyRunCreated(t *testing.T) {
	t.Parallel()

	const (
		created = "Created rgb; its hand-off pull request: https://github.com/imp-example/imr-root-infrastructure/pull/1\n"
		found   = "rgb was created by an earlier run; its hand-off pull request: https://github.com/imp-example/imr-root-infrastructure/pull/1\n"
	)
	apply := func(w *world, rc *root.RunClients, dir string, out io.Writer) error {
		rc.Builds = (&fakeBuilds{build: build(w.p, "")}).open
		req := &root.RunRequest{BuildID: "b-1", Project: w.p.Projects[org.RootProjectKey], Location: "us-central1", Dir: dir}

		return root.ApplyRun(context.Background(), w.clients, rc, req, out)
	}

	t.Run("an apply that finishes", func(t *testing.T) {
		t.Parallel()

		w, rc, dir := creating(t)
		var out bytes.Buffer
		if err := apply(w, rc, dir, &out); err != nil {
			t.Fatalf("ApplyRun() error = %v\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), "creations: creates the hosting organization rgb\n") {
			t.Errorf("the apply does not say that it creates rgb:\n%s", out.String())
		}
		if want := " finding(s).\n" + created; !strings.HasSuffix(out.String(), want) {
			t.Errorf("the finished apply's output does not end with its verdict and %q:\n%s", created, out.String())
		}
		if _, err := w.fake.Read(context.Background(), w.p.StateBucket, root.MarkerObject); err != nil {
			t.Errorf("the finished apply left no marker: %v", err)
		}
	})

	t.Run("an apply that stops after the creation", func(t *testing.T) {
		t.Parallel()

		w, rc, dir := creating(t)
		// The billing export is read after the creations, and the creation never reads it.
		stop := errors.New("refused in this test")
		w.fake.Refuse["Dataset"] = stop
		var out bytes.Buffer
		if err := apply(w, rc, dir, &out); !errors.Is(err, stop) {
			t.Fatalf("ApplyRun() of an apply that stops = %v, want the stop's error\n%s", err, out.String())
		}
		if !strings.Contains(out.String(), stoppedLine) || !strings.HasSuffix(out.String(), "\n"+created) {
			t.Errorf("the stopped apply's output does not end with what it had gathered and %q:\n%s", created, out.String())
		}
		if marker, err := w.fake.Read(context.Background(), w.p.StateBucket, root.MarkerObject); err == nil {
			t.Errorf("the stopped apply left a marker: %s", marker)
		}

		// The next push: the creation's own marker stands, so the apply finds rgb created
		// and names the pull request the stopped apply opened.
		delete(w.fake.Refuse, "Dataset")
		out.Reset()
		if err := apply(w, rc, dir, &out); err != nil {
			t.Fatalf("ApplyRun() after the stop error = %v\n%s", err, out.String())
		}
		if want := " finding(s).\n" + found; !strings.HasSuffix(out.String(), want) || strings.Contains(out.String(), "Created rgb") {
			t.Errorf("the apply after the stop does not end with its verdict and %q:\n%s", found, out.String())
		}
	})
}

// TestRunErrors: Run answers no report beside an error, whichever it is. The error of a
// run that stopped in one of its sections is a StoppedError: it reads as the section's
// error and has its cause, and it carries the report of what the run had gathered before
// the stop, an empty one when the first read stopped it. The error of a run refused
// before its first section is the refusal alone, and carries no report.
func TestRunErrors(t *testing.T) {
	t.Parallel()

	stop := errors.New("refused in this test")
	check := func(w *world, p *org.Placement) (*root.Report, error) {
		return root.Run(context.Background(), w.clients, &root.Request{Placement: p, Dir: w.dir, Mode: root.Check, Commit: "abc1234", GitHubToken: "test-token"}, &bytes.Buffer{})
	}
	tests := []struct {
		name string
		run  func(t *testing.T) (*root.Report, error)
		// want is the cause a person reads, and is the error errors.Is finds in the
		// run's, where the case refuses with one. stopped says that a section stopped the
		// run, and gathered that the sections before it had listed something.
		want              string
		is                error
		stopped, gathered bool
	}{
		{
			name: "refused: a hosting organization's placement",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				hosting, err := org.ReadPlacement("../org/testdata/imp/placement.json")
				if err != nil {
					t.Fatal(err)
				}

				return check(newWorld(t), hosting)
			},
			want: "the imp organization is a hosting organization: the engine runs a root organization",
		},
		{
			name: "refused: no root project recorded",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newWorld(t)
				unrecorded := *w.p
				unrecorded.Projects = map[string]string{}

				return check(w, &unrecorded)
			},
			want: "placement.json records no root project (projects.root): bedrock org bootstrap records it",
		},
		{
			name: "refused: a client that is not wired",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newWorld(t)
				w.clients.OrgPolicies = nil

				return check(w, w.p)
			},
			want: "no Org Policy client is wired: the root organization sets the organization policies of its hosting organizations",
		},
		{
			name: "refused: the root project's number cannot be read",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newWorld(t)
				unnumbered := *w.p
				unnumbered.ProjectNumbers = map[string]string{}
				w.fake.Refuse["ProjectNumber"] = stop

				return check(w, &unnumbered)
			},
			want: "refused in this test", is: stop,
		},
		{
			name: "refused: no GitHub token, without a cloud",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newGitHubWorld(t, libraryPlacement(t))

				return root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Mode: root.Check}, &bytes.Buffer{})
			},
			want: "no GitHub token: the imf organization has no cloud and is kept on GitHub alone",
		},
		{
			name: "stopped: the first read of the first section",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newWorld(t)
				w.fake.Refuse["EnabledServices"] = stop

				return check(w, w.p)
			},
			want: "refused in this test", is: stop, stopped: true,
		},
		{
			name: "stopped: a read after sections that listed changes",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newWorld(t)
				w.fake.Refuse["Budgets"] = stop

				return check(w, w.p)
			},
			want: "refused in this test", is: stop, stopped: true, gathered: true,
		},
		{
			name: "stopped: a read GitHub refuses, without a cloud",
			run: func(t *testing.T) (*root.Report, error) {
				t.Helper()

				w := newGitHubWorld(t, libraryPlacement(t))
				clients := w.refusing(t, map[string]int{"GET /orgs/imf-example/rulesets": http.StatusForbidden})

				return root.Run(context.Background(), clients, &root.Request{Placement: w.p, Mode: root.Check, GitHubToken: "test-token"}, &bytes.Buffer{})
			},
			want: "answered 403: Resource not accessible by integration", stopped: true, gathered: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			report, err := tt.run(t)
			if err == nil || !strings.Contains(errors.Cause(err).Error(), tt.want) {
				t.Fatalf("Run() error = %v, want the cause %q", err, tt.want)
			}
			if report != nil {
				t.Errorf("Run() answered a report beside the error: %+v", report)
			}
			if tt.is != nil && !errors.Is(err, tt.is) {
				t.Errorf("errors.Is does not find the refusing call's error in %v", err)
			}
			var stopped *root.StoppedError
			if carries := errors.As(err, &stopped); carries != tt.stopped {
				t.Fatalf("Run() error carries a report: %t, want %t (the error: %v)", carries, tt.stopped, err)
			}
			if !tt.stopped {
				return
			}
			if stopped.Report == nil {
				t.Fatal("the stopped run's error carries no report")
			}
			if got := stopped.Report.Gathered(); got != tt.gathered {
				t.Errorf("the stopped run had gathered something: %t, want %t (changes %q, findings %q, notes %q)", got, tt.gathered, whats(stopped.Report), stopped.Report.Findings, stopped.Report.Notes)
			}
			section := stopped.Unwrap()
			if section == nil || err.Error() != section.Error() {
				t.Errorf("the stopped run's error reads %q, want it to read as the section's error %v", err.Error(), section)
			}
		})
	}
}

// TestStoppedCommentError: the comment of a plan that had gathered nothing says that it
// found no change before the stop, and the error, every line of it set as code; an error
// too long for a comment is cut, and the comment says so.
func TestStoppedCommentError(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	nothing := &root.Report{}
	body := nothing.StoppedComment("0123456789abcdef", w.p, errors.New("no client in this test\nand a second line\rand a third, @someone"))
	if want := stoppedComment + "It had found no change before the stop.\n\nThe error the plan stopped at:\n\n    no client in this test\n    and a second line\n    and a third, @someone\n"; !strings.HasSuffix(body, want) {
		t.Errorf("StoppedComment() of a plan that had gathered nothing = %q, want it to end %q", body, want)
	}
	long := nothing.StoppedComment("0123456789abcdef", w.p, errors.New(strings.Repeat("x", 9000)))
	if want := "\n\n    " + strings.Repeat("x", 8000) + "\n\nThe error is cut after 8000 bytes; the run's log ends with the whole of it.\n"; !strings.HasSuffix(long, want) {
		t.Errorf("StoppedComment() with an error of 9000 bytes is %d bytes and ends %q, want the error cut at 8000 and the sentence that says so", len(long), long[max(0, len(long)-120):])
	}
}

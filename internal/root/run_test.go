package root_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/deploy"
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
// and the error alone. Where the comment cannot be posted the log says so and claims no
// comment. The next plan that finishes takes the comment's place again.
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

	// The next plan finishes, and its comment takes the place.
	delete(w.fake.Refuse, "EnabledServices")
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
// says it made none. It leaves no marker and frees the lock, and the answer is the run's
// error.
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

// TestReportWithoutARun: a run refused before its first section answers no report, and
// nothing is written for it: its error says all there is. The comment of a plan refused
// so says that it found no change before the stop, and the error, every line of it set
// as code; an error too long for a comment is cut, and the comment says so.
func TestReportWithoutARun(t *testing.T) {
	t.Parallel()

	var none *root.Report
	var out bytes.Buffer
	for _, mode := range []root.Mode{root.Check, root.Plan, root.Apply} {
		none.WriteStopped(&out, mode)
	}
	if out.Len() != 0 || none.Gathered() {
		t.Errorf("a run without a report wrote %q, gathered %t", out.String(), none.Gathered())
	}
	w := newWorld(t)
	body := none.StoppedComment("0123456789abcdef", w.p, errors.New("no client in this test\nand a second line\rand a third, @someone"))
	if want := stoppedComment + "It had found no change before the stop.\n\nThe error the plan stopped at:\n\n    no client in this test\n    and a second line\n    and a third, @someone\n"; !strings.HasSuffix(body, want) {
		t.Errorf("StoppedComment() without a report = %q, want it to end %q", body, want)
	}
	long := none.StoppedComment("0123456789abcdef", w.p, errors.New(strings.Repeat("x", 9000)))
	if want := "\n\n    " + strings.Repeat("x", 8000) + "\n\nThe error is cut after 8000 bytes; the run's log ends with the whole of it.\n"; !strings.HasSuffix(long, want) {
		t.Errorf("StoppedComment() with an error of 9000 bytes is %d bytes and ends %q, want the error cut at 8000 and the sentence that says so", len(long), long[max(0, len(long)-120):])
	}
}

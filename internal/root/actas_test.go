package root_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-playground/errors/v5"
	"github.com/impulseframework/bedrock/internal/org"
	"github.com/impulseframework/bedrock/internal/root"
)

// The role Cloud Build asks of whoever creates or changes a trigger that runs as a
// service account, on that account.
const serviceAccountUser = "roles/iam.serviceAccountUser"

// actAsGrants are the two grants as the engine's calls and its report name them: the
// applier's Service Account User on the planner and on itself, by account.
func actAsGrants(p *org.Placement) map[string]root.Grant {
	grants := map[string]root.Grant{}
	for _, account := range []string{p.PlannerIdentity(), p.ApplierIdentity()} {
		grants[account] = root.Grant{Resource: account, Role: serviceAccountUser, Member: "serviceAccount:" + p.ApplierIdentity()}
	}

	return grants
}

// madeByAnEarlierRelease brings a root organization the bootstrap made to what a release
// without the applier's Service Account User left, at a pin whose move changes both
// triggers: the two grants are off (unless kept), each trigger that stands lacks its last
// step, the triggers are changed as the applier from here on, and the calls recorded
// start empty. It answers the waits the engine asks for from here on, each of which
// returns at once.
func madeByAnEarlierRelease(t *testing.T, w *world, keepGrants bool) *[]time.Duration {
	t.Helper()

	ctx := context.Background()
	if !keepGrants {
		for account, g := range actAsGrants(w.p) {
			if err := w.fake.RevokeServiceAccount(ctx, account, g.Role, g.Member); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(w.fake.Triggers) != 2 {
		t.Fatalf("the bootstrap left %d trigger(s), want the plan trigger and the apply trigger", len(w.fake.Triggers))
	}
	for _, standing := range w.fake.Triggers {
		standing.Steps = standing.Steps[:len(standing.Steps)-1]
	}
	w.fake.Caller = "serviceAccount:" + w.p.ApplierIdentity()
	w.fake.Calls = nil
	var waits []time.Duration
	w.clients.Sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)

		return nil
	}

	return &waits
}

// grantCall is the call that makes the grant, as the stand-in records it.
func grantCall(g root.Grant) string {
	return "GrantServiceAccount(serviceAccounts/" + g.Resource + ", " + g.Role + ", " + g.Member + ")"
}

// grantsLeadTriggers fails the test unless the run made both of the applier's grants
// before its first change of a trigger (change: CreateTrigger or UpdateTrigger).
func grantsLeadTriggers(t *testing.T, w *world, change string) {
	t.Helper()

	first := slices.IndexFunc(w.fake.Calls, func(c string) bool { return strings.HasPrefix(c, change+"(") })
	if first < 0 {
		t.Fatalf("the run made no %s:\n  %s", change, strings.Join(w.fake.Calls, "\n  "))
	}
	for _, g := range actAsGrants(w.p) {
		if at := slices.Index(w.fake.Calls, grantCall(g)); at < 0 || at > first {
			t.Errorf("%s is call %d and the first %s call %d: the grant is made before any trigger change (-1: never made)", grantCall(g), at, change, first)
		}
	}
}

// TestApplierHoldsServiceAccountUserOnTheTriggersAccounts: each account a trigger runs
// as, the planner and the applier, has the applier's Service Account User among the
// bindings the bootstrap makes and the apply keeps.
func TestApplierHoldsServiceAccountUserOnTheTriggersAccounts(t *testing.T) {
	t.Parallel()

	p, err := org.ReadPlacement("../org/testdata/imr/placement.json")
	if err != nil {
		t.Fatal(err)
	}
	d := root.NewDesired(p)
	var accounts []string
	for _, trigger := range d.Triggers() {
		account := trigger.ServiceAccount[strings.LastIndex(trigger.ServiceAccount, "/")+1:]
		accounts = append(accounts, account)
		want := root.Grant{Resource: account, Role: serviceAccountUser, Member: "serviceAccount:" + p.ApplierIdentity()}
		if !slices.Contains(d.ServiceAccountGrants(), want) {
			t.Errorf("the trigger %s runs as %s, and the grants lack %+v: Cloud Build refuses the applier the trigger's change without it", trigger.Name, account, want)
		}
	}
	if want := []string{p.PlannerIdentity(), p.ApplierIdentity()}; !slices.Equal(accounts, want) {
		t.Errorf("the triggers run as %v, want %v", accounts, want)
	}
}

// TestBootstrapMakesTheAppliersGrantsAndApplyKeepsThem: the bootstrap's apply leaves the
// applier Service Account User on the planner and on itself; a check of an organization
// that lacks one lists it, the apply makes it again, and the check after is clean.
func TestBootstrapMakesTheAppliersGrantsAndApplyKeepsThem(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	if _, out := w.run(t, root.Apply, true); strings.Contains(out, "finding") {
		t.Fatalf("the bootstrap's apply:\n%s", out)
	}
	ctx := context.Background()
	for account, g := range actAsGrants(w.p) {
		if !bound(w.fake.Policies["serviceAccounts/"+account], g.Role, g.Member) {
			t.Errorf("the bootstrap left %s without %s on %s", g.Member, g.Role, account)
		}
	}
	gone := actAsGrants(w.p)[w.p.PlannerIdentity()]
	if err := w.fake.RevokeServiceAccount(ctx, gone.Resource, gone.Role, gone.Member); err != nil {
		t.Fatal(err)
	}
	report, _ := w.run(t, root.Check, false)
	if want := []string{"grants: grants " + gone.Member + " " + gone.Role + " on the identity " + gone.Resource}; !slices.Equal(whats(report), want) {
		t.Errorf("the check lists %q, want %q", whats(report), want)
	}
	if applied, out := w.run(t, root.Apply, false); applied.Applied != 1 || !bound(w.fake.Policies["serviceAccounts/"+gone.Resource], gone.Role, gone.Member) {
		t.Errorf("the apply made %d change(s) and the grant stands: %v\n%s", applied.Applied, bound(w.fake.Policies["serviceAccounts/"+gone.Resource], gone.Role, gone.Member), out)
	}
	if clean, out := w.run(t, root.Check, false); !clean.Clean() {
		t.Errorf("the check after the apply is not clean:\n%s\n%v", out, whats(clean))
	}
}

// TestGrantsAreMadeBeforeAnyTriggerChange: within one run the applier's Service Account
// User on the two accounts is made before a trigger is created (the bootstrap, from
// nothing) or changed (an apply of an organization that lacks the grants).
func TestGrantsAreMadeBeforeAnyTriggerChange(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.run(t, root.Apply, true)
	grantsLeadTriggers(t, w, "CreateTrigger")

	madeByAnEarlierRelease(t, w, false)
	w.run(t, root.Apply, false)
	grantsLeadTriggers(t, w, "UpdateTrigger")
}

// TestApplyChangesTheTriggersAsTheApplier: Cloud Build lets the applier change a trigger
// only while it holds Service Account User on the account the trigger runs as. An apply
// of a root organization made without the grants makes both and then brings both
// triggers to their steps, with no wait where IAM applies the grants at once, and the
// check after it is clean.
func TestApplyChangesTheTriggersAsTheApplier(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.run(t, root.Apply, true)
	waits := madeByAnEarlierRelease(t, w, false)
	planner, applier := actAsGrants(w.p)[w.p.PlannerIdentity()], actAsGrants(w.p)[w.p.ApplierIdentity()]

	applied, out := w.run(t, root.Apply, false)
	want := []string{
		"grants: grants " + planner.Member + " " + serviceAccountUser + " on the identity " + planner.Resource,
		"grants: grants " + applier.Member + " " + serviceAccountUser + " on the identity " + applier.Resource,
		"Cloud Build: brings the trigger " + w.p.PlanTriggerName() + " to its steps: steps",
		"Cloud Build: brings the trigger " + w.p.ApplyTriggerName() + " to its steps: steps",
	}
	if !slices.Equal(whats(applied), want) || applied.Applied != len(want) {
		t.Errorf("the apply made %d of %q, want all of %q\n%s", applied.Applied, whats(applied), want, out)
	}
	if len(*waits) != 0 {
		t.Errorf("the apply waited %v, and no change was refused", *waits)
	}
	if clean, out := w.run(t, root.Check, false); !clean.Clean() {
		t.Errorf("the check after the apply is not clean:\n%s\n%v", out, whats(clean))
	}
}

// TestTriggerChangeWaitsForTheGrant: a trigger change refused with a 403 just after the
// same run granted the applier Service Account User on the trigger's account is tried
// again with a backoff, since IAM takes time to apply a grant.
func TestTriggerChangeWaitsForTheGrant(t *testing.T) {
	t.Parallel()

	// Three refusals, then IAM has applied the grant: the change is made on the fourth
	// try, after waits that double.
	t.Run("the grant is applied while the run waits", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		w.run(t, root.Apply, true)
		waits := madeByAnEarlierRelease(t, w, false)
		w.fake.Unapplied = 3
		applied, out := w.run(t, root.Apply, false)
		if applied.Applied != 4 {
			t.Errorf("the apply made %d change(s), want the two grants and the two triggers:\n%s", applied.Applied, out)
		}
		if want := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second}; !slices.Equal(*waits, want) {
			t.Errorf("the apply waited %v, want %v", *waits, want)
		}
		waiting := "  (IAM has not applied serviceAccount:" + w.p.ApplierIdentity() + "'s " + serviceAccountUser + " on " + w.p.PlannerIdentity() + " yet, which this run granted moments ago; trying again in 5s)\n"
		if !strings.Contains(out, waiting) {
			t.Errorf("the apply's log lacks %q:\n%s", waiting, out)
		}
		if clean, out := w.run(t, root.Check, false); !clean.Clean() {
			t.Errorf("the check after the apply is not clean:\n%s\n%v", out, whats(clean))
		}
	})

	// IAM never applies the grant: the tries end seven minutes of waits after the first
	// refusal, and the run fails with the grant and the account named, and with what
	// IAM's documentation says of the time a policy change takes: typically 2 minutes,
	// potentially 7 minutes or longer, and so no longest time.
	t.Run("the grant is not applied in the 7 minutes of tries", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		w.run(t, root.Apply, true)
		waits := madeByAnEarlierRelease(t, w, false)
		w.fake.Unapplied = 1000
		var out bytes.Buffer
		_, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: root.Apply, Commit: "abc1234", GitHubToken: "test-token"}, &out)
		if err == nil {
			t.Fatalf("the apply went through, and Cloud Build refused every change of the trigger:\n%s", out.String())
		}
		applier, planner := w.p.ApplierIdentity(), w.p.PlannerIdentity()
		want := "Cloud Build refuses the change of the trigger " + w.p.PlanTriggerName() + " (googleapi: Error 403: The caller does not have permission): the trigger runs as " + planner +
			", and Cloud Build lets an identity create or change it only when the identity holds " + serviceAccountUser + " on that account (the permission iam.serviceAccounts.actAs). This run granted the role on " + planner +
			" to serviceAccount:" + applier + ", and IAM has not applied the grant after 7 minutes of tries: its documentation says a policy change typically takes 2 minutes, and potentially 7 minutes or longer. The grant stands: run the apply trigger " + w.p.ApplyTriggerName() +
			" again, and it changes the trigger once IAM applies the grant"
		if got := errors.Cause(err).Error(); got != want {
			t.Errorf("the apply's error is\n  %s\nwant\n  %s", got, want)
		}
		minute := time.Minute
		wantWaits := []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, minute, minute, minute, minute, minute, 45 * time.Second}
		if !slices.Equal(*waits, wantWaits) {
			t.Errorf("the apply waited %v, want %v", *waits, wantWaits)
		}
		var total time.Duration
		for _, wait := range *waits {
			total += wait
		}
		if total != 7*time.Minute {
			t.Errorf("the apply waited %s in all, want 7 minutes, the longer of the two times IAM's documentation names for a policy change, and no longer", total)
		}
		report := stoppedReport(t, err)
		if report.Applied != 2 {
			t.Errorf("the stopped apply counts %d change(s) made, want the two grants", report.Applied)
		}
		for account, g := range actAsGrants(w.p) {
			if !bound(w.fake.Policies["serviceAccounts/"+account], g.Role, g.Member) {
				t.Errorf("the stopped apply left %s without %s on %s, and its error says the grant stands", g.Member, g.Role, account)
			}
		}
		if slices.Contains(w.fake.Calls, "UpdateTrigger("+w.p.ApplyTriggerName()+")") {
			t.Errorf("the apply went on to the apply trigger after the plan trigger's change failed:\n  %s", strings.Join(w.fake.Calls, "\n  "))
		}
	})

	// The grants stood before the run: a refusal is not IAM's delay, and it is the
	// change's answer at once, as Cloud Build gave it.
	t.Run("a refusal the run's grants do not explain", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		w.run(t, root.Apply, true)
		waits := madeByAnEarlierRelease(t, w, true)
		w.fake.Unapplied = 1
		var out bytes.Buffer
		_, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: root.Apply, Commit: "abc1234", GitHubToken: "test-token"}, &out)
		if err == nil || errors.Cause(err).Error() != "googleapi: Error 403: The caller does not have permission" {
			t.Errorf("the apply's error is %v, want Cloud Build's refusal as it gave it\n%s", err, out.String())
		}
		if len(*waits) != 0 {
			t.Errorf("the apply waited %v on a grant it did not make", *waits)
		}
	})

	// The bootstrap makes the applier's grants too, and its trigger changes are the
	// break-glass account's: a refusal of one never waits on a grant to the applier.
	t.Run("the bootstrap's run", func(t *testing.T) {
		t.Parallel()

		w := newWorld(t)
		var waits []time.Duration
		w.clients.Sleep = func(_ context.Context, d time.Duration) error {
			waits = append(waits, d)

			return nil
		}
		w.fake.Caller = "user:" + w.p.BreakGlassAccount
		var out bytes.Buffer
		_, err := root.Run(context.Background(), w.clients, &root.Request{Placement: w.p, Dir: w.dir, Mode: root.Apply, Commit: "abc1234", GitHubToken: "test-token", Bootstrap: true}, &out)
		if err == nil || errors.Cause(err).Error() != "googleapi: Error 403: The caller does not have permission" {
			t.Errorf("the bootstrap's error is %v, want Cloud Build's refusal as it gave it\n%s", err, out.String())
		}
		if len(waits) != 0 {
			t.Errorf("the bootstrap waited %v on a grant to the applier", waits)
		}
	})
}

// TestPlanSaysTheAppliersGrants: the plan of a root organization that lacks the applier's
// Service Account User on the two accounts lists each grant in the words of every other
// grant, in the run's log and in the pull request's comment, ahead of the trigger changes
// that wait on them and as the first of what the merge changes, and makes nothing.
func TestPlanSaysTheAppliersGrants(t *testing.T) {
	t.Parallel()

	w := newWorld(t)
	w.run(t, root.Apply, true)
	madeByAnEarlierRelease(t, w, false)
	applier, planner := w.p.ApplierIdentity(), w.p.PlannerIdentity()

	report, _ := w.run(t, root.Plan, false)
	var log bytes.Buffer
	report.Write(&log, root.Plan)
	wantLog := "grants: the apply grants serviceAccount:" + applier + " " + serviceAccountUser + " on the identity " + planner + "\n" +
		"grants: the apply grants serviceAccount:" + applier + " " + serviceAccountUser + " on the identity " + applier + "\n" +
		"Cloud Build: the apply brings the trigger " + w.p.PlanTriggerName() + " to its steps: steps\n" +
		"Cloud Build: the apply brings the trigger " + w.p.ApplyTriggerName() + " to its steps: steps\n"
	if !strings.HasPrefix(log.String(), wantLog) || !strings.HasSuffix(log.String(), "4 change(s) the apply would make; 0 finding(s).\n") {
		t.Errorf("the plan's log is\n%s\nwant it to open\n%sand to end with the count of the four changes", log.String(), wantLog)
	}
	wantComment := "At 0123456:\n\n" +
		"- grants: grants serviceAccount:" + applier + " " + serviceAccountUser + " on the identity " + planner + "\n" +
		"- grants: grants serviceAccount:" + applier + " " + serviceAccountUser + " on the identity " + applier + "\n" +
		"- Cloud Build: brings the trigger " + w.p.PlanTriggerName() + " to its steps: steps\n" +
		"- Cloud Build: brings the trigger " + w.p.ApplyTriggerName() + " to its steps: steps\n"
	if comment := report.Comment("0123456789abcdef", w.p); !strings.Contains(comment, wantComment) {
		t.Errorf("the plan's comment is\n%s\nwant in it\n%s", comment, wantComment)
	}
	if slices.ContainsFunc(w.fake.Calls, func(c string) bool { return strings.HasPrefix(c, "Grant") || strings.HasPrefix(c, "UpdateTrigger") }) {
		t.Errorf("the plan made something:\n  %s", strings.Join(w.fake.Calls, "\n  "))
	}
}

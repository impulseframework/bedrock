// retire.go is the retirement of one version of a versioned secret (bedrock secret retire,
// through the operations workflow): the live release's build run again with the
// instruction. A versioned secret (secret:"versioned" in the code) reaches the processes
// in every enabled version, and its values in the database each name the version that
// wrote them, so a version leaves in a fixed order. The plan step refuses what cannot be
// retired and hands the plan every enabled version but the one retiring. The migrate
// step, after the migrations, runs the release's own migrate command once more with
// -retire-secret <VARIABLE>_<version>: the application rewrites every stored value that
// names the version under the current one and confirms none is left by exiting 0. The
// revision without the version then deploys and takes traffic, and the record says what
// was retired. Disabling the version in Secret Manager is the workflow's job, as the
// environment's operations identity, once this build has succeeded: no step here disables
// or destroys anything, so a run that stops anywhere leaves the version enabled and the
// revision that served before it serving.

package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/derive"
	"github.com/impulseframework/bedrock/internal/secret"
)

// The retirement's instruction, facts and flag.
const (
	// retireVariableSub and retireVersionSub are the instruction the operations workflow's
	// retire action sets on the live release's version trigger: the versioned secret's
	// variable and the version to take out of use; _REQUESTER says who asked.
	retireVariableSub = "_RETIRE_SECRET"
	retireVersionSub  = "_RETIRE_VERSION"
	// retireVersionsFact is what the plan step leaves for the migrate step: the variable
	// of every enabled version of the secret, the retiring one included, with the
	// version's resource name, as a JSON object.
	retireVersionsFact = "RETIRE_SECRET_VERSIONS"
	// retiredVariableFact and retiredVersionFact are what the migrate step leaves for the
	// record once the application has confirmed that no value names the version.
	retiredVariableFact = "RETIRED_SECRET"
	retiredVersionFact  = "RETIRED_VERSION"
	// retireArg is the migrate command's flag for the rewrite; its value is the variable
	// the retiring version is handed over under (APP_SECRET_KEY_1).
	retireArg = "-retire-secret"
)

// secretVersionRE is a Secret Manager version's number.
var secretVersionRE = regexp.MustCompile(`^[1-9]\d*$`)

// Retirement is the retirement a build carries: the version of a versioned secret the
// operations workflow asked to take out of use.
type Retirement struct {
	// Variable is the versioned secret's variable, as the code declares it.
	Variable string
	// Version is the Secret Manager version to retire.
	Version string
	// Requester is who asked (_REQUESTER).
	Requester string
}

// retirementOf reads the retirement off the build's substitutions, nil for a build that
// carries none, and refuses one that cannot run: a variable without a version or a
// version without a variable, a variable of the wrong shape, a version that is not a
// positive integer, and one that names no requester.
func retirementOf(subs map[string]string) (*Retirement, error) {
	r := &Retirement{Variable: subs[retireVariableSub], Version: subs[retireVersionSub], Requester: subs[requesterSub]}
	switch {
	case r.Variable == "" && r.Version == "":
		return nil, nil
	case r.Variable == "" || r.Version == "":
		return nil, errors.Newf("%s=%q with %s=%q: a retirement names the versioned secret's variable and the version to retire, both", retireVariableSub, r.Variable, retireVersionSub, r.Version)
	case secret.ValidateVariable(r.Variable) != nil:
		return nil, errors.Newf("%s %q is not a secret's variable: upper snake case under the APP_ prefix, as the code declares it (APP_SECRET_KEY)", retireVariableSub, r.Variable)
	case !secretVersionRE.MatchString(r.Version):
		return nil, errors.Newf("%s %q is not a secret version: a positive integer, as Secret Manager numbers them", retireVersionSub, r.Version)
	case r.Requester == "":
		return nil, errors.Newf("%s=%s names no requester (%s): a retirement says who asked for it", retireVariableSub, r.Variable, requesterSub)
	}

	return r, nil
}

// String says what the retirement does, for the build's log.
func (r *Retirement) String() string {
	return fmt.Sprintf("version %s of %s is taken out of use, asked for by %s: the migrate command rewrites every stored value that names it and confirms none is left, the revision deploys without it, and the operations workflow then disables it in Secret Manager", r.Version, r.Variable, r.Requester)
}

// target is the variable the retiring version is handed over under, which the migrate
// command's flag names (APP_SECRET_KEY_1).
func (r *Retirement) target() string {
	return derive.VersionVariable(r.Variable, r.Version)
}

// planRetirement is the plan step's part of a retirement, nothing for a build that
// carries none. It refuses, before anything changes, a variable the release does not
// declare versioned, one declared at a level the migrate command does not construct (the
// command, which does the rewrite, never receives it), a version that is not enabled, the newest enabled version (every
// value is written under it), and any version while the live release does not hold the
// newest enabled one (the rewrite writes under it, so the revision serving now could not
// open what was rewritten, and a release that does not hold it may still write under the
// version retiring). It then takes the version out of what the plan is told,
// so that the revision this build deploys does not receive it, and answers the fact the
// migrate step reads: every enabled version, the retiring one included, since the
// rewrite opens values under the one and writes them under the newest.
func planRetirement(ctx context.Context, clients *Clients, m *derive.Model, subs map[string]string, pins *derivedPins, out io.Writer) (map[string]string, error) {
	r, err := retirementOf(subs)
	if err != nil || r == nil {
		return nil, err
	}
	env := subs[envSub]
	var declared *derive.Secret
	var versioned []string
	for i := range m.Secrets {
		s := &m.Secrets[i]
		if !s.Versioned {
			continue
		}
		versioned = append(versioned, s.Variable.Name)
		if s.Variable.Name == r.Variable {
			declared = s
		}
	}
	if declared == nil {
		declares := "it declares no secret so"
		if len(versioned) > 0 {
			declares = "declared so: " + strings.Join(versioned, ", ")
		}

		return nil, errors.Newf("%s%s is not a secret this release declares secret:%q (%s): a version is retired of a secret whose every enabled version the service holds, and nothing was changed", rejected, r.Variable, secret.Versioned, declares)
	}
	if reason := m.RetirementOutOfReach(declared); reason != "" {
		return nil, errors.Newf("%s%s. Nothing was changed", rejected, reason)
	}
	id := secretContainerID(m, subs[appSub], env, declared)
	var enabled []string
	if pins != nil {
		enabled = pins.Versioned[r.Variable]
	}
	if !slices.Contains(enabled, r.Version) {
		return nil, errors.Newf("%sversion %s of %s is not enabled in %s (%s of it %s enabled): no process holds a version that is not enabled, so there is nothing to retire, and nothing was changed. Name an enabled version other than the newest", rejected, r.Version, id, env, versionsPhrase(enabled), isAre(enabled))
	}
	newest := enabled[len(enabled)-1]
	if r.Version == newest {
		return nil, errors.Newf("%sversion %s of %s is its newest enabled version in %s, the one every value is written under: retiring it would leave %s writing under a version it no longer holds, and nothing was changed. Rotate first (bedrock secret rotate %s %s), which adds the next version and runs the live release on it, then retire %s", rejected, r.Version, id, env, env, env, r.Variable, r.Version)
	}
	if err := r.heldBehind(ctx, clients, subs, newest); err != nil {
		return nil, err
	}
	resources := make(map[string]string, len(enabled))
	for _, version := range enabled {
		resources[derive.VersionVariable(r.Variable, version)] = "projects/" + subs[projectSub] + "/secrets/" + id + "/versions/" + version
	}
	encoded, err := json.Marshal(resources)
	if err != nil {
		return nil, errors.Wrap(err, "json.Marshal()")
	}
	kept := slices.DeleteFunc(slices.Clone(enabled), func(version string) bool {
		return version == r.Version
	})
	pins.Versioned[r.Variable] = kept
	fmt.Fprintf(out, "Retirement: %s. The plan hands the revision %s of %s (%s) and not version %s; the migrate command still receives it, to open what it rewrites.\n", r, versionsPhrase(kept), id, versionVariables(r.Variable, kept), r.Version)
	r.pullRequests(ctx, clients, subs, out)

	return map[string]string{retireVersionsFact: string(encoded)}, nil
}

// pullRequests says what the retirement leaves the pull requests with, in the environment
// they deploy to and nowhere else. A pull request's stack shares that environment's
// secrets and mounts the versions that were enabled when it was planned, so services
// built before the retirement keep naming the version once it is disabled, and a pull
// request's own database is not the one this run rewrites. Nothing is done for them
// here: the line names the pull requests that have an environment now (their services
// stand, by the label the sweep reads), or says that they could not be listed, and what
// each needs.
func (r *Retirement) pullRequests(ctx context.Context, clients *Clients, subs map[string]string, out io.Writer) {
	env := subs[envSub]
	if env != tstEnvironment {
		return
	}
	const needs = "Each is built again with a /gcbrun comment once the version is disabled, and its build mounts the versions that are enabled; one whose own database holds values written under version %s takes /gcbrun reload-db, since this run rewrites %s's database and no pull request's.\n"
	numbers, err := pullRequestEnvironments(ctx, clients, subs)
	switch {
	case err != nil:
		fmt.Fprintf(out, "Pull requests: the pull requests with an environment in %s could not be listed (%v), and the retirement goes on. A pull request's services built before it were handed version %s of %s and keep naming it. "+needs, env, errors.Cause(err), r.Version, r.Variable, r.Version, env)
	case len(numbers) == 0:
		fmt.Fprintf(out, "Pull requests: none has an environment in %s now, so no pull request's services name version %s of %s.\n", env, r.Version, r.Variable)
	default:
		fmt.Fprintf(out, "Pull requests with an environment in %s: %s. Their services were handed the versions enabled when they were built, version %s of %s among them for one built before this run, and keep naming it. "+needs, env, joinAnd(numbers), r.Version, r.Variable, r.Version, env)
	}
}

// heldBehind refuses the retirement unless the release live in the environment holds the
// newest enabled version of the secret, by its deployment record. The rewrite writes every
// value under the newest enabled version, and a release writes under the newest version
// it holds: a live release without the newest (a rotation whose run did not deploy)
// could not open what this run rewrites until the next revision serves, and not at all
// when that revision does not start, and it may still write under the version retiring.
func (r *Retirement) heldBehind(ctx context.Context, clients *Clients, subs map[string]string, newest string) error {
	env := subs[envSub]
	live, err := liveRelease(ctx, clients.Storage, subs)
	if err != nil {
		return err
	}
	if live == nil {
		return errors.Newf("%sno release is live in %s by its deployment records: a version is retired by the live release, whose migrate command rewrites the values that name it, and nothing was changed. Release first, then retire version %s of %s", rejected, env, r.Version, r.Variable)
	}
	held := live.VersionedSecrets[r.Variable]
	if slices.Contains(held, newest) {
		return nil
	}
	holds := "names none"
	if len(held) > 0 {
		holds = "names " + versionsPhrase(held)
	}

	return errors.Newf("%sthe release live in %s (%s) does not hold version %s of %s, the newest enabled (its record %s): this run rewrites every value under version %s, which the revision serving now could not open, and that revision may still write under version %s. Nothing was changed. Run the live release again first (bedrock rerun %s %s), so that it takes up version %s, then retire %s", rejected, env, live.Version, newest, r.Variable, holds, newest, r.Version, env, live.Version, newest, r.Version)
}

// retire is the migrate step's part of a retirement, after the migrations: the release's
// migrate command run once more with the flag and the variable the retiring version is
// handed over under, and with every enabled version of the secret in its environment
// (the plan step's fact, each read as the deploy identity, which the stack grants
// accessor on the versioned secrets at the levels the command constructs, the ones the
// plan step lets a retirement name; the values are never printed). The application rewrites every stored value that names the version and
// confirms none is left by exiting 0, and the step leaves the retirement for the record.
// Any other exit fails the build here, before a revision deploys, with nothing disabled.
func (m *migrateRun) retire(ctx context.Context, clients *Clients, w Workspace, env map[string]string, r *Retirement) error {
	raw := env[retireVersionsFact]
	if raw == "" {
		return errors.Newf("%s names no versions for the retirement of %s (%s): the stack's plan step leaves them, and without them nothing is rewritten, so the run stops before a revision without version %s deploys", EnvironmentFile, r.Variable, retireVersionsFact, r.Version)
	}
	var versions map[string]string
	if err := json.Unmarshal([]byte(raw), &versions); err != nil {
		return errors.Wrapf(err, "json.Unmarshal(): %s", retireVersionsFact)
	}
	if _, ok := versions[r.target()]; !ok {
		return errors.Newf("%s does not name %s, the version the run retires: the stack's plan step leaves every enabled version of %s, the retiring one included", retireVersionsFact, r.target(), r.Variable)
	}
	secrets, err := clients.Secrets(ctx)
	if err != nil {
		return err
	}
	defer secrets.Close()
	vars := map[string]string{}
	for _, pair := range m.command.Env {
		name, value, _ := strings.Cut(pair, "=")
		vars[name] = value
	}
	for _, name := range slices.Sorted(maps.Keys(versions)) {
		payload, err := accessWhenGranted(ctx, clients, secrets, versions[name])
		if err != nil {
			return errors.Wrapf(err, "%s: the retirement's %s", versions[name], name)
		}
		vars[name] = string(payload)
	}
	c := m.command
	c.Env = make([]string, 0, len(vars))
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		c.Env = append(c.Env, name+"="+vars[name])
	}
	c.Args = []string{retireArg, r.target()}
	fmt.Fprintf(m.out, "=== Retirement, asked for by %s: running the migrate command with %s %s ===\n", r.Requester, retireArg, r.target())
	fmt.Fprintf(m.out, "It runs with every enabled version of %s, read as the deploy identity (%s; the values are not printed), rewrites every stored value that names version %s under the newest, and confirms none is left by exiting 0.\n", r.Variable, strings.Join(slices.Sorted(maps.Keys(versions)), ", "), r.Version)
	started := time.Now()
	if err := m.exec.Run(ctx, c, m.out); err != nil {
		return errors.Newf("the release's migrate command did not confirm that no stored value names version %s of %s (%s %s, after %s; its lines are above: %v). Nothing is retired: the version stays enabled, this run deploys no revision, and the next release hands the version to the service as before. A release whose migrate command does not know %s was built before the framework took it: release on a framework that has it, then retire", r.Version, r.Variable, retireArg, r.target(), time.Since(started).Round(time.Second), errors.Cause(err), retireArg)
	}
	fmt.Fprintf(m.out, "No stored value names version %s of %s any more, by the migrate command's word (%s). The revision this run deploys does not receive the version, and the operations workflow disables it in Secret Manager once the build has succeeded; it is never destroyed here.\n", r.Version, r.Variable, time.Since(started).Round(time.Second))

	return w.Append(map[string]string{retiredVariableFact: r.Variable, retiredVersionFact: r.Version})
}

// retiredOf is the record's retirement, nil for a run that retired nothing: what the
// migrate step left once the application confirmed, and who asked.
func retiredOf(env map[string]string, build *Build) *RetiredSecret {
	if env[retiredVariableFact] == "" {
		return nil
	}

	return &RetiredSecret{Variable: env[retiredVariableFact], Version: env[retiredVersionFact], Requester: build.Substitutions[requesterSub]}
}

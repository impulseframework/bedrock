package render

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/derive"
)

// The indentation the rendered workflow writes a job's variables, a step and a step's
// script at.
const (
	jobKeyIndent    = "    "
	jobEnvIndent    = "      "
	stepIndent      = "      - "
	stepKeyIndent   = "        "
	stepBlockIndent = "          "
)

// workflowStep is one step of a rendered workflow job as a runner runs it: the script of
// its run block and the variables the job and the step declare, their expressions not yet
// resolved.
type workflowStep struct {
	script string
	env    map[string]string
}

// stepRun is what a run of a step is given: the workflow's inputs by name, the outputs
// of the steps before it (target.project), and a directory put first on the PATH, where a
// test keeps its stand-in for gcloud.
type stepRun struct {
	inputs  map[string]string
	outputs map[string]string
	bin     string
}

// stepResult is how a step ended: whether it failed, everything it printed, what it
// wrote to GITHUB_OUTPUT, and the directory it ran in.
type stepResult struct {
	failed  bool
	printed string
	outputs string
	dir     string
}

// expressionRE is a GitHub expression in a variable's value.
var expressionRE = regexp.MustCompile(`\$\{\{ *([A-Za-z0-9_.]+) *\}\}`)

// stepOf reads the named step out of a job's text (jobOf): its run block, with the
// block's indentation taken off, and the job's and the step's variables.
func stepOf(t *testing.T, job, name string) *workflowStep {
	t.Helper()

	step := &workflowStep{env: map[string]string{}}
	lines := strings.Split(job, "\n")
	found := false
	for i := 0; i < len(lines); i++ {
		switch {
		case lines[i] == jobKeyIndent+"env:":
			i = step.variables(lines, i+1, jobEnvIndent)
		case lines[i] == stepIndent+"name: "+name:
			found = true
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], stepIndent) && (lines[i] == "" || strings.HasPrefix(lines[i], stepKeyIndent)); i++ {
				switch lines[i] {
				case stepKeyIndent + "env:":
					i = step.variables(lines, i+1, stepBlockIndent)
				case stepKeyIndent + "run: |":
					var script []string
					for i++; i < len(lines) && (lines[i] == "" || strings.HasPrefix(lines[i], stepBlockIndent)); i++ {
						script = append(script, strings.TrimPrefix(lines[i], stepBlockIndent))
					}
					i--
					step.script = strings.Join(script, "\n") + "\n"
				}
			}
			i--
		}
	}
	if !found || step.script == "" {
		t.Fatalf("the job has no step %q with a run block", name)
	}

	return step
}

// variables reads the KEY: value lines written at the indentation from line i on, into
// the step's variables, and answers the index of the last one it read.
func (s *workflowStep) variables(lines []string, i int, indent string) int {
	for ; i < len(lines) && strings.HasPrefix(lines[i], indent) && !strings.HasPrefix(lines[i], indent+" "); i++ {
		entry := strings.TrimPrefix(lines[i], indent)
		if strings.HasPrefix(entry, "#") {
			continue
		}
		key, value, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`) {
			value = value[1 : len(value)-1]
		}
		s.env[key] = value
	}

	return i - 1
}

// run runs the step's script the way a runner runs a run block that names no shell
// (bash -e), in a directory of its own, with the job's and the step's variables, their
// expressions resolved from the run's inputs and the earlier steps' outputs (an input
// not given is empty, as GitHub passes it; the actor is octocat).
func (s *workflowStep) run(t *testing.T, r *stepRun) *stepResult {
	t.Helper()

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("no bash to run the workflow's step with: %v (the checks a step makes are proven by running them)", err)
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "step.sh")
	if err := os.WriteFile(script, []byte(s.script), 0o600); err != nil {
		t.Fatal(err)
	}
	output, summary := filepath.Join(dir, "github-output"), filepath.Join(dir, "github-summary")
	path := os.Getenv("PATH")
	if r.bin != "" {
		path = r.bin + string(os.PathListSeparator) + path
	}
	cmd := exec.CommandContext(t.Context(), bash, "-e", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + path, "HOME=" + dir, "GITHUB_OUTPUT=" + output, "GITHUB_STEP_SUMMARY=" + summary}
	for key, value := range s.env {
		resolved := expressionRE.ReplaceAllStringFunc(value, func(expression string) string {
			name := expressionRE.FindStringSubmatch(expression)[1]
			switch {
			case name == "github.actor":
				return "octocat"
			case strings.HasPrefix(name, "inputs."):
				return r.inputs[strings.TrimPrefix(name, "inputs.")]
			case strings.HasPrefix(name, "steps."):
				return r.outputs[strings.Replace(strings.TrimPrefix(name, "steps."), ".outputs.", ".", 1)]
			}
			t.Fatalf("%s = %s: the test resolves no expression %s", key, value, name)

			return ""
		})
		cmd.Env = append(cmd.Env, key+"="+resolved)
	}
	printed, err := cmd.CombinedOutput()
	result := &stepResult{failed: err != nil, printed: string(printed), dir: dir}
	if exited := new(exec.ExitError); err != nil && !errors.As(err, &exited) {
		t.Fatalf("the step did not run: %v", err)
	}
	if written, err := os.ReadFile(output); err == nil {
		result.outputs = string(written)
	}

	return result
}

// wiredWorkflow renders the fixture with tst and stg wired for operations (a project the
// placement records for each) and answers its operations workflow.
func wiredWorkflow(t *testing.T, fixture, placement string) string {
	t.Helper()

	p, err := derive.ReadPlacement(filepath.Join(fixtures, placement))
	if err != nil {
		t.Fatalf("derive.ReadPlacement() error = %v", err)
	}
	if p.Projects == nil {
		p.Projects = map[string]derive.Project{}
	}
	p.Projects["tst"] = derive.Project{ID: "imp-tst-gbl-core-b241", Number: "123456789012"}
	p.Projects["stg"] = derive.Project{ID: "imp-stg-gbl-core-0fa7", Number: "234567890123"}
	files, err := Render(deriveWith(t, fixture, p))
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	for _, f := range files {
		if f.Root && strings.HasSuffix(f.Path, OperationsWorkflow) {
			return string(f.Content)
		}
	}
	t.Fatalf("the render of %s holds no %s", fixture, OperationsWorkflow)

	return ""
}

// The steps that read a job's inputs, by the name the workflow gives them.
const (
	targetStep         = "The environment's project, identity and trigger"
	restoreTargetStep  = "The environment's project, identity, trigger and restore"
	teardownTargetStep = "The environment typed back, and its project, identity and trigger"
	secretTargetStep   = "The environment's project, identity, trigger and the secret's container"
	migrateTargetStep  = "The environment's project, identity, trigger and log bucket"
	rollbackTargetStep = "The environment's project, identity, triggers and inputs"
	releaseTargetStep  = "The first environment's project, identity and release trigger"
)

// The sentences a job refuses an input with.
const (
	refusedEnvironment = "the environment is none of this application's (tst, stg, or prd), spelled exactly as there: nothing ran"
	refusedReason      = "the reason is one line with no | character"
	refusedBackup      = "backup is a backup's resource name (projects/<p>/instances/<i>/backups/<b>) or @<RFC 3339 moment>"
	refusedDatabase    = "database is a database's name as the list action prints it"
)

// TestOperationsInputsHeldWhole runs the step of each job of the rendered operations
// workflow that reads the job's inputs, as a runner runs it, over values a person can
// give: the value as it should be, one that carries a line break, and one that carries
// the separator of the list of substitutions the value reaches (a comma, or | where the
// list is separated by |). A value is taken only whole: a version that is a number on
// one of its lines, a reason that ends itself with a |, an environment that closes the
// variable's name it is read into are each refused before any line uses them, and the
// step writes no output and runs nothing of what the value carried.
func TestOperationsInputsHeldWhole(t *testing.T) {
	t.Parallel()

	harbor, beacon := operationsWorkflow(t, "harbor", "placement.json"), wiredWorkflow(t, "beacon", "placement-beacon.json")
	const (
		harborTarget = "project=imp-tst-gbl-core-b241\nprovider=projects/123456789012/locations/global/workloadIdentityPools/imp-tst-github/providers/github\nidentity=imp-tst-gbl-harbor-ops@imp-tst-gbl-core-b241.iam.gserviceaccount.com\n"
		version      = "trigger=imp-tst-uc1-harbor-version\n"
		stack        = "trigger=imp-tst-uc1-harbor-stack\n"
		backup       = "projects/p/instances/i/backups/harbor-db-20261005-0430"
		injected     = "tst:-}\"; touch injected; echo \"${X"
	)
	type inputs = map[string]string
	tests := []struct {
		name     string
		workflow string
		job      string
		step     string
		inputs   inputs
		// wantOutputs is what the step writes to GITHUB_OUTPUT when it takes the inputs;
		// wantRefusal is the sentence it refuses them with, writing nothing.
		wantOutputs string
		wantRefusal string
	}{
		// The environment, in every job.
		{name: "list: an environment", workflow: harbor, job: "list", step: targetStep, inputs: inputs{"environment": "tst"}, wantOutputs: harborTarget + version},
		{name: "list: an environment of two lines", workflow: harbor, job: "list", step: targetStep, inputs: inputs{"environment": "tst\nstg"}, wantRefusal: refusedEnvironment},
		{name: "list: an environment that closes the name it is read into", workflow: harbor, job: "list", step: targetStep, inputs: inputs{"environment": injected}, wantRefusal: refusedEnvironment},
		{name: "list: an environment the application does not have", workflow: harbor, job: "list", step: targetStep, inputs: inputs{"environment": "dev"}, wantRefusal: refusedEnvironment},
		{name: "maintenance: an environment", workflow: harbor, job: "maintenance", step: targetStep, inputs: inputs{"environment": "tst"}, wantOutputs: harborTarget + version},
		{name: "maintenance: an environment that closes the name it is read into", workflow: harbor, job: "maintenance", step: targetStep, inputs: inputs{"environment": injected}, wantRefusal: refusedEnvironment},
		{name: "apply: an environment", workflow: harbor, job: "apply", step: targetStep, inputs: inputs{"environment": "tst"}, wantOutputs: harborTarget + stack},
		{name: "apply: an environment that closes the name it is read into", workflow: harbor, job: "apply", step: targetStep, inputs: inputs{"environment": injected}, wantRefusal: refusedEnvironment},
		{name: "release: an environment, which the job does not read", workflow: harbor, job: "release", step: releaseTargetStep, inputs: inputs{"environment": "stg"}},
		{name: "release: an environment of two lines", workflow: harbor, job: "release", step: releaseTargetStep, inputs: inputs{"environment": "stg\n::error::x"}, wantRefusal: refusedEnvironment},

		// run: the release reaches the trigger's tag.
		{name: "run: a release", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": "tst", "release": "v1.4.2"}, wantOutputs: harborTarget + version},
		{name: "run: an environment that closes the name it is read into", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": injected, "release": "v1.4.2"}, wantRefusal: refusedEnvironment},
		{name: "run: a release of two lines", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": "tst", "release": "v1.4.2\nv9.9.9"}, wantRefusal: "run names the release to run again, a release tag, whole"},
		{name: "run: a release that carries a substitution", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": "tst", "release": "v1.4.2,_PR_NUMBER=5"}, wantRefusal: "run names the release to run again, a release tag, whole"},
		{name: "run: a release with a fourth number", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": "tst", "release": "v1.4.2.9"}, wantRefusal: "run names the release to run again, a release tag, whole"},
		{name: "run: no release", workflow: harbor, job: "run", step: targetStep, inputs: inputs{"environment": "tst"}, wantRefusal: "run names the release to run again, a release tag, whole"},

		// restore: the backup, the reason and the database reach a list separated by |.
		{name: "restore: a backup by name, a reason with commas", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup, "reason": "a bad import, row 12; see the ticket"}, wantOutputs: "kind=generation\n" + harborTarget + version + "restore=empty\n"},
		{name: "restore: a moment, a database and a release", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "prd", "backup": "@2026-10-05T04:30:00.5+02:00", "database": "imp-prd-gbl-harbor-db-g2", "release": "v1.4.2"}, wantOutputs: "kind=generation\nproject=imp-prd-gbl-core-abe8\n"},
		{name: "restore: a release's cut", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "before": "v1.4.2"}, wantOutputs: "kind=generation\n" + harborTarget},
		{name: "restore: an environment that closes the name it is read into", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": injected, "backup": backup}, wantRefusal: refusedEnvironment},
		{name: "restore: a reason with a |", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup, "reason": "why|_PR_NUMBER=5"}, wantRefusal: refusedReason},
		{name: "restore: a reason of two lines", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup, "reason": "why\nand more"}, wantRefusal: refusedReason},
		{name: "restore: a reason with a carriage return", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup, "reason": "why\rand more"}, wantRefusal: refusedReason},
		{name: "restore: a moment that carries a substitution", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": "@2026-10-05T04:30:00Z|_PR_NUMBER=5"}, wantRefusal: refusedBackup},
		{name: "restore: a moment of two lines, the second an output of the step", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": "@2026-10-05T04:30:00Z\nrelease=v9.9.9"}, wantRefusal: refusedBackup},
		{name: "restore: a backup's name of two lines", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup + "\n" + backup}, wantRefusal: refusedBackup},
		{name: "restore: a database that carries a substitution", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": "@2026-10-05T04:30:00Z", "database": "harbor-db|_ENV=prd"}, wantRefusal: refusedDatabase},
		{name: "restore: a database of two lines", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": "@2026-10-05T04:30:00Z", "database": "harbor-db\nharbor-db-g2"}, wantRefusal: refusedDatabase},
		{name: "restore: a release of two lines", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "backup": backup, "release": "v1.4.2\nv9.9.9"}, wantRefusal: "the release is a release tag, whole"},
		{name: "restore: a cut that carries a substitution", workflow: harbor, job: "restore", step: restoreTargetStep, inputs: inputs{"environment": "tst", "before": "v1.4.2|_PR_NUMBER=5"}, wantRefusal: "before names a release tag, whole"},

		// rollback: the reason reaches a list separated by |.
		{name: "rollback: a reason, and the release to return to", workflow: harbor, job: "rollback", step: rollbackTargetStep, inputs: inputs{"environment": "tst", "reason": "the import page fails, see the ticket", "release": "v1.4.1"}, wantOutputs: harborTarget + version + "rollback=imp-tst-uc1-harbor-rollback\n"},
		{name: "rollback: an environment that closes the name it is read into", workflow: harbor, job: "rollback", step: rollbackTargetStep, inputs: inputs{"environment": injected, "reason": "why"}, wantRefusal: refusedEnvironment},
		{name: "rollback: a reason with a |", workflow: harbor, job: "rollback", step: rollbackTargetStep, inputs: inputs{"environment": "tst", "reason": "why|_REQUESTER=someone-else"}, wantRefusal: refusedReason},
		{name: "rollback: a reason of two lines", workflow: harbor, job: "rollback", step: rollbackTargetStep, inputs: inputs{"environment": "tst", "reason": "why\nand more"}, wantRefusal: refusedReason},
		{name: "rollback: a release of two lines", workflow: harbor, job: "rollback", step: rollbackTargetStep, inputs: inputs{"environment": "tst", "reason": "why", "release": "v1.4.1\nto=v9.9.9"}, wantRefusal: "the release is a release tag, whole"},

		// teardown: the action reaches a list separated by commas.
		{name: "teardown: the action and the environment typed back", workflow: harbor, job: "teardown", step: teardownTargetStep, inputs: inputs{"environment": "tst", "action": "teardown", "confirm": "tst"}, wantOutputs: harborTarget + stack},
		{name: "teardown: an environment that closes the name it is read into, typed back", workflow: harbor, job: "teardown", step: teardownTargetStep, inputs: inputs{"environment": injected, "action": "destroy", "confirm": injected}, wantRefusal: refusedEnvironment},
		{name: "teardown: an action that carries a substitution", workflow: harbor, job: "teardown", step: teardownTargetStep, inputs: inputs{"environment": "tst", "action": "teardown,_APPLY_IDENTITY=x", "confirm": "tst"}, wantRefusal: "this job runs teardown or destroy"},
		{name: "teardown: an action of two lines", workflow: harbor, job: "teardown", step: teardownTargetStep, inputs: inputs{"environment": "tst", "action": "teardown\ndestroy", "confirm": "tst"}, wantRefusal: "this job runs teardown or destroy"},

		// The migration operations: the action, the table and the version reach a list
		// separated by commas.
		{name: "force: a version", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "table": "data", "version": "7"}, wantOutputs: harborTarget + version + "logs=imp-tst-gbl-harbor-migrate-logs\n"},
		{name: "force: no version, as -1", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "table": "schema", "version": "-1"}, wantOutputs: harborTarget},
		{name: "force: version 0", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "version": "0"}, wantOutputs: harborTarget},
		{name: "version: no version to give", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "version", "release": "v1.4.2", "table": "schema"}, wantOutputs: harborTarget},
		{name: "force: an environment that closes the name it is read into", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": injected, "action": "force", "release": "v1.4.2", "version": "7"}, wantRefusal: refusedEnvironment},
		{name: "force: a version of two lines, the second a number", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "version": "x\n7"}, wantRefusal: "force needs the version the database is at, whole"},
		{name: "force: a version of two lines, the first a number", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "version": "7\n,_PR_NUMBER=5"}, wantRefusal: "force needs the version the database is at, whole"},
		{name: "force: a version that carries a substitution", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "version": "7,_SERVICES=us-central1=other"}, wantRefusal: "force needs the version the database is at, whole"},
		{name: "force: a version below -1", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "version": "-2"}, wantRefusal: "force needs the version the database is at, whole"},
		{name: "force: a table that carries a substitution", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "force", "release": "v1.4.2", "table": "data,_PROJECT=other", "version": "7"}, wantRefusal: "the table is schema or data"},
		{name: "an action that carries a substitution", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "version,_PR_NUMBER=5", "release": "v1.4.2"}, wantRefusal: "this job runs the migration operations (version, rerun and force)"},
		{name: "an action of two lines", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "version\nforce", "release": "v1.4.2"}, wantRefusal: "this job runs the migration operations (version, rerun and force)"},
		{name: "rerun: a release that carries a substitution", workflow: harbor, job: "migration", step: migrateTargetStep, inputs: inputs{"environment": "tst", "action": "rerun", "release": "v1.4.2,_PR_NUMBER=5"}, wantRefusal: "rerun names the release the migrations are at, a release tag, whole"},

		// rotate and retire: the variable and the version reach a list separated by commas.
		{name: "rotate: a generated secret", workflow: beacon, job: "rotate", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_COOKIE_KEY"}, wantOutputs: "container=imp-tst-gbl-beacon-cookie-key\nproject=imp-tst-gbl-core-b241\n"},
		{name: "rotate: an environment that closes the name it is read into", workflow: beacon, job: "rotate", step: secretTargetStep, inputs: inputs{"environment": injected, "variable": "APP_COOKIE_KEY"}, wantRefusal: refusedEnvironment},
		{name: "rotate: a variable of two lines", workflow: beacon, job: "rotate", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_COOKIE_KEY\nAPP_SECRET_KEY"}, wantRefusal: "rotate names the generated or versioned secret's variable, whole"},
		{name: "retire: a version", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "12"}, wantOutputs: "container=imp-tst-gbl-beacon-secret-key\nproject=imp-tst-gbl-core-b241\n"},
		{name: "retire: an environment that closes the name it is read into", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": injected, "variable": "APP_SECRET_KEY", "version": "1"}, wantRefusal: refusedEnvironment},
		{name: "retire: a version of two lines, the second a number", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "1,_PR_NUMBER=5\n1"}, wantRefusal: "retire names the version of APP_SECRET_KEY to retire, a positive integer, whole"},
		{name: "retire: a version of two lines, the first a number", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "1\n,_SERVICES=us-central1=other"}, wantRefusal: "retire names the version of APP_SECRET_KEY to retire, a positive integer, whole"},
		{name: "retire: a version that carries a substitution", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "1,_PR_NUMBER=5"}, wantRefusal: "retire names the version of APP_SECRET_KEY to retire, a positive integer, whole"},
		{name: "retire: version 0", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "0"}, wantRefusal: "retire names the version of APP_SECRET_KEY to retire, a positive integer, whole"},
		{name: "retire: no version", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY"}, wantRefusal: "retire names the version of APP_SECRET_KEY to retire, a positive integer, whole"},
		{name: "retire: a variable that carries a substitution", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY,_PR_NUMBER=5", "version": "1"}, wantRefusal: "retire names the versioned secret's variable, whole"},
		{name: "retire: a variable of two lines", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_SECRET_KEY\nAPP_SECRET_KEY", "version": "1"}, wantRefusal: "retire names the versioned secret's variable, whole"},
		{name: "retire: a secret that is not versioned", workflow: beacon, job: "retire", step: secretTargetStep, inputs: inputs{"environment": "tst", "variable": "APP_COOKIE_KEY", "version": "1"}, wantRefusal: `APP_COOKIE_KEY is not a secret the code declares secret:"versioned"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := stepOf(t, jobOf(t, tt.workflow, tt.job), tt.step).run(t, &stepRun{inputs: tt.inputs})
			if _, err := os.Stat(filepath.Join(got.dir, "injected")); err == nil {
				t.Errorf("the step ran what the environment input carried")
			}
			if tt.wantRefusal != "" {
				if !got.failed || !strings.Contains(got.printed, "::error::") || !strings.Contains(got.printed, tt.wantRefusal) {
					t.Fatalf("the step failed = %t, want the refusal %q; it printed:\n%s\nand wrote:\n%s", got.failed, tt.wantRefusal, got.printed, got.outputs)
				}
				if got.outputs != "" {
					t.Errorf("a refused step wrote outputs:\n%s", got.outputs)
				}

				return
			}
			if got.failed {
				t.Fatalf("the step failed over inputs it takes; it printed:\n%s", got.printed)
			}
			if !strings.Contains(got.outputs, tt.wantOutputs) {
				t.Errorf("the step wrote\n%s\nwant it to hold\n%s", got.outputs, tt.wantOutputs)
			}
			if lines, want := strings.Count(got.outputs, "\n"), strings.Count(got.outputs, "="); lines != want {
				t.Errorf("the step wrote %d lines for %d outputs:\n%s", lines, want, got.outputs)
			}
		})
	}
}

// gcloudStandIn is the script the tests put on the PATH as gcloud, for the steps that read
// an environment's deployment records and start a build. It keeps what it is told beside
// itself: every call's arguments in calls, the records the bucket holds in records/, the
// records the build writes while it runs in written/ (moved into records/ when the trigger
// is run), and the status the build ends with in status. The listing prints each record
// with its own timestamp, as gcloud storage ls -l prints an object's time.
const gcloudStandIn = `#!/usr/bin/env bash
here=$(cd "$(dirname "$0")" && pwd)
printf '%s\n' "$*" >> "$here/calls"
case "$1 $2 $3" in
  "builds triggers describe") echo records ;;
  "builds triggers run")
    cp "$here"/written/*.json "$here/records/" 2>/dev/null
    echo b-9 ;;
  "builds describe "*) cat "$here/status" ;;
  "storage ls "*)
    for record in "$here"/records/*.json; do
      [ -e "$record" ] || continue
      printf '     512  %s  gs://records/beacon/tst/3c4d/releases/%s\n' "$(jq -r .timestamp "$record")" "${record##*/}"
    done ;;
  "storage cat "*)
    shift 2
    for object in "$@"; do
      case "$object" in
        */stack.json) echo '{"id": "3c4d"}' ;;
        *) cat "$here/records/${object##*/}" ;;
      esac
    done ;;
esac
`

// standIn makes a directory holding the stand-in for gcloud, the records the bucket holds
// before the build and the ones the build writes (each by its build's id), and the
// build's last status, and answers the directory.
func standIn(t *testing.T, before, written map[string]string, status string) string {
	t.Helper()

	if _, err := exec.LookPath("jq"); err != nil {
		t.Fatalf("no jq to run the workflow's step with: %v (the step reads the deployment records with it)", err)
	}
	bin := t.TempDir()
	files := map[string]string{"gcloud": gcloudStandIn, "status": status + "\n", "calls": ""}
	for build, record := range before {
		files[filepath.Join("records", build+".json")] = record + "\n"
	}
	for build, record := range written {
		files[filepath.Join("written", build+".json")] = record + "\n"
	}
	for _, dir := range []string{"records", "written"} {
		if err := os.MkdirAll(filepath.Join(bin, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(content), 0o700); err != nil { //nolint:gosec // the stand-in is run
			t.Fatal(err)
		}
	}

	return bin
}

// calls are the stand-in's calls so far, one a line.
func calls(t *testing.T, bin string) string {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(bin, "calls"))
	if err != nil {
		t.Fatal(err)
	}

	return string(data)
}

// TestRetireWaitsForItsOwnLiveRecord runs the retire job's build step of the rendered
// workflow against a stand-in for gcloud: the step starts the live release's build with
// the instruction, each value in its own substitution, and ends well only when that build
// succeeded, its live record says the version was retired, and that record is the
// environment's newest live one. A release approved alongside, which planned while the
// version was enabled and took traffic after the retirement's build, or in the same
// second, stops the job before the step that disables the version, with what to do; so
// does a build that says nothing of the retirement, one that retired something else, and
// one that failed. The step after it disables that version of that container and nothing
// else, and says in the environment pull requests deploy to what they need.
func TestRetireWaitsForItsOwnLiveRecord(t *testing.T) {
	t.Parallel()

	const (
		buildStep   = "Rewrite what names the version and run the live release again without it"
		disableStep = "Disable the version"
		live        = `{"version": "v1.2.3", "status": "live", "timestamp": "2026-10-05T04:00:00Z", "build": "b-5", "versionedSecrets": {"APP_SECRET_KEY": ["1", "2"]}}`
		retired     = `{"version": "v1.2.3", "status": "live", "timestamp": "2026-10-05T05:00:00Z", "build": "b-9", "versionedSecrets": {"APP_SECRET_KEY": ["2"]}, "retiredSecret": {"variable": "APP_SECRET_KEY", "version": "1", "requester": "octocat"}}`
		started     = "builds triggers run imp-tst-uc1-beacon-version --tag v1.2.3 --region us-central1 --project imp-tst-gbl-core-b241 --substitutions _REQUESTER=octocat,_RETIRE_SECRET=APP_SECRET_KEY,_RETIRE_VERSION=1 --format=value(metadata.build.id)\n"
		notNewest   = "Build b-9 retired version 1 of APP_SECRET_KEY in the revision it deployed, but its record is not the newest live one in tst: "
		askAgain    = " took traffic at or after it, and that revision may have been handed the version. Nothing was disabled, and the version stays enabled. Once that release is live and no other is on its way, ask again (bedrock secret retire tst APP_SECRET_KEY 1)"
	)
	workflow := wiredWorkflow(t, "beacon", "placement-beacon.json")
	tests := []struct {
		name    string
		before  map[string]string
		written map[string]string
		status  string
		// wantPrinted is what the step prints; wantRefusal the sentence it stops the job
		// with; wantStarted whether it started the build.
		wantPrinted string
		wantRefusal string
		wantStarted bool
	}{
		{
			name:        "the retirement's build is the newest live record",
			before:      map[string]string{"b-5": live},
			written:     map[string]string{"b-9": retired},
			status:      "SUCCESS",
			wantPrinted: "Build b-9 succeeded, its record says so and is the newest live one in tst: no stored value names version 1 of APP_SECRET_KEY, and tst runs v1.2.3 without it.",
			wantStarted: true,
		},
		{
			name:    "a release that took traffic before the retirement's build",
			before:  map[string]string{"b-5": live},
			written: map[string]string{"b-9": retired, "b-8": `{"version": "v1.2.3", "status": "live", "timestamp": "2026-10-05T04:58:00Z", "build": "b-8", "versionedSecrets": {"APP_SECRET_KEY": ["1", "2"]}}`},
			status:  "SUCCESS", wantPrinted: "is the newest live one in tst", wantStarted: true,
		},
		{
			name:        "a release approved alongside took traffic after the retirement's build",
			before:      map[string]string{"b-5": live},
			written:     map[string]string{"b-9": retired, "b-10": `{"version": "v1.2.4", "status": "live", "timestamp": "2026-10-05T05:02:00Z", "build": "b-10", "versionedSecrets": {"APP_SECRET_KEY": ["1", "2"]}}`},
			status:      "SUCCESS",
			wantRefusal: notNewest + "v1.2.4, build b-10" + askAgain,
			wantStarted: true,
		},
		{
			name:        "a release took traffic in the same second: the two are not told apart",
			before:      map[string]string{"b-5": live},
			written:     map[string]string{"b-9": retired, "b-10": `{"version": "v1.2.4", "status": "live", "timestamp": "2026-10-05T05:00:00Z", "build": "b-10"}`},
			status:      "SUCCESS",
			wantRefusal: notNewest + "v1.2.3, build b-9 and v1.2.4, build b-10" + askAgain,
			wantStarted: true,
		},
		{
			name:    "a preview deployed after it took no traffic and does not count",
			before:  map[string]string{"b-5": live},
			written: map[string]string{"b-9": retired, "b-10": `{"version": "v1.2.4", "status": "preview", "timestamp": "2026-10-05T05:02:00Z", "build": "b-10"}`},
			status:  "SUCCESS", wantPrinted: "is the newest live one in tst", wantStarted: true,
		},
		{
			name:        "a build that succeeded and says nothing of a retirement",
			before:      map[string]string{"b-5": live},
			written:     map[string]string{"b-9": `{"version": "v1.2.3", "status": "live", "timestamp": "2026-10-05T05:00:00Z", "build": "b-9"}`},
			status:      "SUCCESS",
			wantRefusal: "Build b-9 succeeded, but its deployment record does not say that version 1 of APP_SECRET_KEY was retired",
			wantStarted: true,
		},
		{
			name:        "a build whose record retired another version",
			before:      map[string]string{"b-5": live},
			written:     map[string]string{"b-9": strings.Replace(retired, `"version": "1"`, `"version": "2"`, 1)},
			status:      "SUCCESS",
			wantRefusal: "Build b-9 succeeded, but its deployment record does not say that version 1 of APP_SECRET_KEY was retired",
			wantStarted: true,
		},
		{
			name:        "a build that wrote no record",
			before:      map[string]string{"b-5": live},
			status:      "SUCCESS",
			wantRefusal: "Build b-9 succeeded, but its deployment record does not say that version 1 of APP_SECRET_KEY was retired",
			wantStarted: true,
		},
		{
			name:        "a build that failed",
			before:      map[string]string{"b-5": live},
			status:      "FAILURE",
			wantRefusal: "Build b-9 ended FAILURE; its log says why (a refusal names what to do). Version 1 of APP_SECRET_KEY stays enabled, nothing was disabled",
			wantStarted: true,
		},
		{
			name:        "no release live: nothing is started",
			status:      "SUCCESS",
			wantRefusal: "No release is live in tst by its deployment records",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bin := standIn(t, tt.before, tt.written, tt.status)
			run := &stepRun{
				inputs:  map[string]string{"environment": "tst", "variable": "APP_SECRET_KEY", "version": "1"},
				outputs: map[string]string{"target.project": "imp-tst-gbl-core-b241", "target.trigger": "imp-tst-uc1-beacon-version", "target.container": "imp-tst-gbl-beacon-secret-key"},
				bin:     bin,
			}
			got := stepOf(t, jobOf(t, workflow, "retire"), buildStep).run(t, run)
			if started := strings.Contains(calls(t, bin), started); started != tt.wantStarted {
				t.Errorf("the step started the build with the instruction = %t, want %t; gcloud was called:\n%s", started, tt.wantStarted, calls(t, bin))
			}
			if strings.Contains(calls(t, bin), "secrets versions") {
				t.Errorf("the build's step touched the secret's versions:\n%s", calls(t, bin))
			}
			if tt.wantRefusal != "" {
				if !got.failed || !strings.Contains(got.printed, "::error::"+tt.wantRefusal) {
					t.Fatalf("the step failed = %t, want the refusal %q; it printed:\n%s", got.failed, tt.wantRefusal, got.printed)
				}

				return
			}
			if got.failed || !strings.Contains(got.printed, tt.wantPrinted) {
				t.Fatalf("the step failed = %t, want it to print %q; it printed:\n%s", got.failed, tt.wantPrinted, got.printed)
			}
			disabled := stepOf(t, jobOf(t, workflow, "retire"), disableStep).run(t, run)
			if disabled.failed {
				t.Fatalf("the disabling step failed; it printed:\n%s", disabled.printed)
			}
			if want := "secrets versions disable 1 --secret imp-tst-gbl-beacon-secret-key --project imp-tst-gbl-core-b241 --quiet\n"; !strings.HasSuffix(calls(t, bin), want) {
				t.Errorf("the disabling step called gcloud with\n%s\nwant its last call\n%s", calls(t, bin), want)
			}
			containsAll(t, disabled.printed,
				"Version 1 of imp-tst-gbl-beacon-secret-key is disabled in tst, asked for by octocat",
				"Pull requests: a pull request's services built before this retirement were handed version 1 and still name it, and it is disabled now. Build each open pull request again (a /gcbrun comment): its build mounts the versions that are enabled.",
			)
		})
	}

	// Beyond the environment pull requests deploy to, the disabling step says nothing of them.
	bin := standIn(t, nil, nil, "SUCCESS")
	beyond := stepOf(t, jobOf(t, workflow, "retire"), disableStep).run(t, &stepRun{
		inputs:  map[string]string{"environment": "stg", "variable": "APP_SECRET_KEY", "version": "1"},
		outputs: map[string]string{"target.project": "imp-stg-gbl-core-0fa7", "target.container": "imp-stg-gbl-beacon-secret-key"},
		bin:     bin,
	})
	if beyond.failed || !strings.Contains(beyond.printed, "Version 1 of imp-stg-gbl-beacon-secret-key is disabled in stg") || strings.Contains(beyond.printed, "Pull requests") {
		t.Errorf("the disabling step in stg failed = %t and printed:\n%s", beyond.failed, beyond.printed)
	}
}

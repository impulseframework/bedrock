package render

import (
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
	if err != nil {
		if _, exited := err.(*exec.ExitError); !exited { //nolint:errorlint // CombinedOutput returns the *exec.ExitError itself
			t.Fatalf("the step did not run: %v", err)
		}
	}
	if written, err := os.ReadFile(output); err == nil {
		result.outputs = string(written)
	}

	return result
}

// wiredWorkflow renders the fixture with tst wired for operations (a project the
// placement records) and answers its operations workflow.
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

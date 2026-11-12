package org

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/impulseframework/bedrock/internal/render"
)

// TestRenderRootWithoutCloud: a root organization without a cloud renders its README, its
// journal, its ignore rules and the workflow its engine runs in, and no layer. The
// workflow is owned, so the check compares it; it names the environment that holds the
// key, the jobs by the names the ruleset and a pull request show, and the pull request's
// files by a commit it checks out as data, and it carries the triggers' own script for
// the pinned bedrock, run from the runner's directory with Python isolated.
func TestRenderRootWithoutCloud(t *testing.T) {
	t.Parallel()

	p := noCloudRoot()
	files, err := Render(p)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	got := map[string]render.File{}
	var paths []string
	for _, f := range files {
		got[f.Path] = f
		paths = append(paths, f.Path)
	}
	want := map[string]render.Tier{WorkflowFile: render.Owned, "README.md": render.Owned, "JOURNAL.md": render.Seeded, ".gitignore": render.Seeded}
	if len(got) != len(want) {
		t.Errorf("Render() wrote %v, want the workflow, the README, the journal and the ignore rules", paths)
	}
	for path, tier := range want {
		if f, ok := got[path]; !ok || f.Tier != tier {
			t.Errorf("%s: rendered %t with tier %v, want tier %v", path, ok, f.Tier, tier)
		}
	}
	workflow := string(got[WorkflowFile].Content)
	for _, phrase := range []string{
		"pull_request_target:", `- "master"`, "name: " + PlanJobName, "name: " + ApplyJobName, "name: " + CheckJobName,
		"environment: " + RootEnvironment, "permissions: {}", "persist-credentials: false", "path: organization",
		"ref: ${{ github.event.pull_request.head.sha }}", "ref: ${{ github.sha }}",
		InfrastructureKeySecret + ": ${{ secrets." + InfrastructureKeySecret + " }}",
		"BEDROCK_PULL_REQUEST: ${{ github.event.pull_request.number }}",
		`python3 -I fetch-bedrock.py "$RUNNER_TEMP" https://github.com https://github.com/impulseframework/bedrock.git "$GITHUB_WORKSPACE/organization/placement.json"`,
		"          import hashlib\n", `"$RUNNER_TEMP/bedrock" org plan`, `"$RUNNER_TEMP/bedrock" org apply`, `"$RUNNER_TEMP/bedrock" org check --issue`,
		"group: bedrock-apply",
	} {
		if !strings.Contains(workflow, phrase) {
			t.Errorf("the workflow lacks %q", phrase)
		}
	}
	for _, absent := range []string{"[[", "]]", "<no value>", "\t"} {
		if strings.Contains(workflow, absent) {
			t.Errorf("the workflow carries %q", absent)
		}
	}
	readme := string(got["README.md"].Content)
	for _, phrase := range []string{
		"# imf-root-infrastructure", "the `imf-example` GitHub organization", "This root organization has no cloud.", "`" + PlanJobName + "`",
		"`imf-example-infrastructure`", "the secret `" + InfrastructureKeySecret + "` of this repository's environment `" + RootEnvironment + "`",
		"bedrock secret add " + InfrastructureKey + " --from-file", "1 approving review(s)",
		"- immutable releases, on for every repository of the organization", "## Immutable releases\n\nEvery repository of the `imf-example` organization is held to\nimmutable releases",
		`in the "Releases" section,` + "\n" + `"All repositories"`,
	} {
		if !strings.Contains(readme, phrase) {
			t.Errorf("the README lacks %q", phrase)
		}
	}
	if strings.Contains(readme, "<no value>") || strings.Contains(readme, "Cloud Build trigger") {
		t.Error("the README of a root organization without a cloud names a value it lacks, or the triggers")
	}

	// What is rendered is what the check compares: written, the directory is clean; the
	// workflow changed by hand is a finding.
	dir := t.TempDir()
	if _, err := Write(p, files, dir); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	report, err := Check(p, dir)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if !report.Clean() {
		t.Errorf("Check() of the directory as rendered found %+v", report.Findings)
	}
	path := filepath.Join(dir, filepath.FromSlash(WorkflowFile))
	if err := os.WriteFile(path, []byte(strings.Replace(workflow, "environment: "+RootEnvironment, "environment: other", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	report, err = Check(p, dir)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if len(report.Findings) != 1 || report.Findings[0].Path != WorkflowFile {
		t.Errorf("Check() of a workflow changed by hand found %+v, want the workflow", report.Findings)
	}
}

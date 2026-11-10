// rootgithub.go renders the repository of a root organization without a cloud: its
// README, its journal, and the workflow its engine runs in. The workflow is to such a
// root organization what the two Cloud Build triggers are to one with a cloud, and since
// a workflow's steps are the repository's own file where a trigger's are the trigger's,
// the file is owned: bedrock org render writes it and bedrock org check compares it.

package org

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/go-playground/errors/v5"

	"github.com/impulseframework/bedrock/internal/render"
)

const (
	// rootGitHubTemplateDir holds the files of a root organization without a cloud.
	rootGitHubTemplateDir = templateDir + "/root-github"
	workflowTemplate      = rootGitHubTemplateDir + "/workflows/bedrock.yml.tmpl"
	// workflowScriptIndent is the depth of a step's script in the workflow file.
	workflowScriptIndent = "          "
)

// workflowStep is what the workflow's shared steps see: the view, and the commit the
// job reads the repository's files at.
type workflowStep struct {
	View *view
	Ref  string
}

// renderRootWorkflow renders the workflow of a root organization without a cloud. Its
// template's delimiters are [[ and ]], so GitHub's own ${{ }} expressions stand as they
// are.
func renderRootWorkflow(v *view) (render.File, error) {
	src, err := templates.ReadFile(workflowTemplate)
	if err != nil {
		return render.File{}, errors.Wrapf(err, "embed.FS.ReadFile(): %s", workflowTemplate)
	}
	step := func(v *view, ref string) workflowStep {
		return workflowStep{View: v, Ref: ref}
	}
	t, err := template.New(WorkflowFile).Delims("[[", "]]").Option("missingkey=error").Funcs(template.FuncMap{"step": step}).Parse(string(src))
	if err != nil {
		return render.File{}, errors.Wrapf(err, "template.Parse(): %s", workflowTemplate)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		return render.File{}, errors.Wrapf(err, "template.Execute(): %s", WorkflowFile)
	}

	return render.File{Path: WorkflowFile, Tier: render.Owned, Content: buf.Bytes()}, nil
}

// The names the workflow and the README of a root organization without a cloud carry.
func (*view) RootEnvironment() string {
	return RootEnvironment
}

func (*view) EnvironmentKeySecret() string {
	return InfrastructureKeySecret
}

func (*view) InfrastructureKeyName() string {
	return InfrastructureKey
}

func (*view) ReleaseKeyName() string {
	return ReleaseKey
}

func (*view) ReleaseKeyOrgSecret() string {
	return ReleaseKeyOrgSecret
}

func (*view) ReleaseClientIDOrgSecret() string {
	return ReleaseClientIDOrgSecret
}

func (*view) ReleaseLoginVariable() string {
	return ReleaseLoginVariable
}

func (*view) WorkflowFile() string {
	return WorkflowFile
}

func (*view) PlanJobName() string {
	return PlanJobName
}

func (*view) ApplyJobName() string {
	return ApplyJobName
}

func (*view) CheckJobName() string {
	return CheckJobName
}

// FetchBedrockYAML is the script that puts the pinned bedrock in place, the triggers' own
// (fetchBedrockScript), at the depth of a step's script in the workflow file.
func (*view) FetchBedrockYAML() string {
	lines := strings.Split(strings.TrimRight(fetchBedrockScript, "\n"), "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = workflowScriptIndent + line
		}
	}

	return strings.Join(lines, "\n")
}

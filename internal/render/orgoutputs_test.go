package render

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// keyedOrgOutput is an output of 1-org whose value is a map by project key or by
// environment code, as its description opens.
var keyedOrgOutput = regexp.MustCompile(`(?s)output "([a-z_]+)" \{\s+description = "By (?:project key|environment code),`)

// orgOutputRead is a read of one of 1-org's outputs through a layer's local.org, with the
// character that follows the name.
var orgOutputRead = regexp.MustCompile(`local\.org\.([a-z_]+)(.?)`)

// TestKeyedOrgOutputsReadByKey: 1-org answers each custom role, and each tag of an
// environment project, as a map by project key, so a layer that reads one takes its own
// entry (local.org.<name>[var.environment]). A read without the key hands the resource the
// whole map, which the plan refuses: the grant of the queue's maintenance to the deploy
// identity read its role that way.
func TestKeyedOrgOutputsReadByKey(t *testing.T) {
	t.Parallel()

	outputs, err := os.ReadFile(filepath.Join("..", "org", "templates", "1-org", "outputs.tf.tmpl"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	keyed := map[string]bool{}
	for _, m := range keyedOrgOutput.FindAllSubmatch(outputs, -1) {
		keyed[string(m[1])] = true
	}
	for _, name := range []string{"cloud_tasks_queue_operator_role", "secret_version_disabler_role", "public_invoker_tag_value"} {
		if !keyed[name] {
			t.Fatalf("1-org's output %s is not read as a map by key: the test no longer finds the outputs it holds the layers to", name)
		}
	}

	reads := 0
	for _, dir := range []string{"templates", filepath.Join("..", "org", "templates")} {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tf.tmpl") {
				return err
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range orgOutputRead.FindAllSubmatch(content, -1) {
				if !keyed[string(m[1])] {
					continue
				}
				reads++
				if string(m[2]) != "[" {
					t.Errorf("%s reads local.org.%s without a key: the output is a map by project key, read as local.org.%s[var.environment]", path, m[1], m[1])
				}
			}

			return nil
		})
		if err != nil {
			t.Fatalf("filepath.WalkDir(%s) error = %v", dir, err)
		}
	}
	if reads == 0 {
		t.Fatal("no layer reads a keyed output of 1-org: the test looks in the wrong place")
	}
}

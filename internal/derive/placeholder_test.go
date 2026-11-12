package derive

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlaceholderImage holds the image a first placement names to the welcome image as
// it is published: the name the workflow publishes this repository's welcome directory
// under, at the version welcome/VERSION records, which is the tag. A version raised in
// one place and not the other would name an image that is never published.
func TestPlaceholderImage(t *testing.T) {
	t.Parallel()

	record, err := os.ReadFile(filepath.Join("..", "..", "welcome", "VERSION"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	version, _, _ := strings.Cut(string(record), " ")
	registry, rest, _ := strings.Cut(PlaceholderImage, "/")
	name, tag, _ := strings.Cut(rest, ":")
	if tag != version {
		t.Errorf("PlaceholderImage = %q, whose tag is %q, and welcome/VERSION records %q", PlaceholderImage, tag, version)
	}
	workflow, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "welcome-image.yml"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	for _, line := range []string{"      REGISTRY: " + registry + "\n", "      NAME: " + name + "\n"} {
		if !strings.Contains(string(workflow), line) {
			t.Errorf("PlaceholderImage = %q, and the workflow that publishes the image does not hold %q", PlaceholderImage, strings.TrimSpace(line))
		}
	}
}

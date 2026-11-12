package derive

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestPlaceholderImage holds the image a first placement names to the welcome image as
// it is published: the name the workflow publishes this repository's welcome directory
// under, by digest, for the version welcome/VERSION records or the one before it (a
// version is raised and published first, and its digest is named after). A version
// ahead of the record would name an image that is never published, and a name by tag
// would follow the tag wherever it is moved.
func TestPlaceholderImage(t *testing.T) {
	t.Parallel()

	image := regexp.MustCompile(`\A([^/]+)/([^@:]+)@sha256:[0-9a-f]{64}\z`).FindStringSubmatch(PlaceholderImage)
	if image == nil {
		t.Fatalf("PlaceholderImage = %q, want the image by its digest: registry/name@sha256: and 64 hexadecimal digits", PlaceholderImage)
	}
	registry, name := image[1], image[2]

	record, err := os.ReadFile(filepath.Join("..", "..", "welcome", "VERSION"))
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	recorded, _, _ := strings.Cut(string(record), " ")
	latest, err := strconv.Atoi(strings.TrimPrefix(recorded, "v"))
	if err != nil {
		t.Fatalf("welcome/VERSION records %q, not a version (v1, v2, ...)", recorded)
	}
	named, err := strconv.Atoi(strings.TrimPrefix(PlaceholderVersion, "v"))
	if err != nil || !strings.HasPrefix(PlaceholderVersion, "v") || named < 1 {
		t.Fatalf("PlaceholderVersion = %q, not a version (v1, v2, ...)", PlaceholderVersion)
	}
	if named != latest && named != latest-1 {
		t.Errorf("PlaceholderVersion = %q, and welcome/VERSION records %q: the placement names the recorded version or, until that one is published and its digest named, the one before it", PlaceholderVersion, recorded)
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

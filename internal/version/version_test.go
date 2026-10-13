package version

import (
	"regexp"
	"testing"
)

func TestVersionFormat(t *testing.T) {
	if Version == "" {
		t.Fatal("Version string must not be empty")
	}

	// Проверка на базовое соответствие SemVer (X.Y.Z или X.Y.Z-prerelease)
	semverRegex := regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	if !semverRegex.MatchString(Version) {
		t.Errorf("Version %q does not match semantic versioning format", Version)
	}
}

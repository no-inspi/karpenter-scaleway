package version

import "testing"

func TestVersionNotEmpty(t *testing.T) {
	if Version == "" {
		t.Fatal("Version must never be empty; ldflags injection failed or default missing")
	}
}

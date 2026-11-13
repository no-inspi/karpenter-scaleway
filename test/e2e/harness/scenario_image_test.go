package harness

import "testing"

// TestImageOverridesFromEnv verifies that addImageOverrides copies
// E2E_IMAGE_REPO / E2E_IMAGE_TAG into the Values map under the keys
// the chart's _helpers.tpl reads (image.repository / image.tag), and
// that unset env leaves the map untouched.
func TestImageOverridesFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		repo     string
		tag      string
		wantRepo string
		wantTag  string
		wantLen  int
	}{
		{name: "both set", repo: "ghcr.io/x/y", tag: "abc123", wantRepo: "ghcr.io/x/y", wantTag: "abc123", wantLen: 2},
		{name: "tag only", repo: "", tag: "abc123", wantTag: "abc123", wantLen: 1},
		{name: "repo only", repo: "ghcr.io/x/y", tag: "", wantRepo: "ghcr.io/x/y", wantLen: 1},
		{name: "neither", repo: "", tag: "", wantLen: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("E2E_IMAGE_REPO", tc.repo)
			t.Setenv("E2E_IMAGE_TAG", tc.tag)
			values := map[string]string{}
			addImageOverrides(values)
			if got := values["image.repository"]; got != tc.wantRepo {
				t.Errorf("image.repository=%q want %q", got, tc.wantRepo)
			}
			if got := values["image.tag"]; got != tc.wantTag {
				t.Errorf("image.tag=%q want %q", got, tc.wantTag)
			}
			if len(values) != tc.wantLen {
				t.Errorf("Values length=%d want %d (map=%v)", len(values), tc.wantLen, values)
			}
		})
	}
}

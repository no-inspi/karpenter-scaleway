package harness

import (
	"strings"
	"testing"
)

func TestRunIDFromEnv_FallbackOnEmpty(t *testing.T) {
	t.Setenv("E2E_RUN_ID", "")
	got := RunIDFromEnv()
	if len(got) < 4 {
		t.Errorf("RunIDFromEnv fallback too short: %q", got)
	}
	if !strings.HasPrefix(got, "loc-") {
		t.Errorf("RunIDFromEnv fallback must start with %q, got %q", "loc-", got)
	}
}

func TestRunIDFromEnv_RespectsCaller(t *testing.T) {
	t.Setenv("E2E_RUN_ID", "manual-1234")
	if got := RunIDFromEnv(); got != "manual-1234" {
		t.Errorf("expected manual-1234, got %q", got)
	}
}

func TestBackendFromEnv_RejectsEmpty(t *testing.T) {
	t.Setenv("E2E_BACKEND", "")
	if _, err := BackendFromEnv(); err == nil {
		t.Error("expected error on empty backend")
	}
}

func TestBackendFromEnv_RejectsUnknown(t *testing.T) {
	t.Setenv("E2E_BACKEND", "azure")
	if _, err := BackendFromEnv(); err == nil {
		t.Error("expected error on unknown backend")
	}
}

func TestBackendFromEnv_AcceptsKapsuleAndKosmos(t *testing.T) {
	for _, b := range []string{"kapsule", "kosmos"} {
		t.Setenv("E2E_BACKEND", b)
		got, err := BackendFromEnv()
		if err != nil {
			t.Fatalf("BackendFromEnv(%s): %v", b, err)
		}
		if string(got) != b {
			t.Errorf("backend = %q, want %q", got, b)
		}
	}
}

func TestSelectedBackends_Filtering(t *testing.T) {
	cases := []struct {
		env  string
		want []Backend
	}{
		{"", []Backend{BackendKapsule, BackendKosmos}},
		{"kapsule", []Backend{BackendKapsule}},
		{"kosmos", []Backend{BackendKosmos}},
		{"azure", []Backend{BackendKapsule, BackendKosmos}}, // unknown → both
	}
	for _, tc := range cases {
		t.Run(tc.env, func(t *testing.T) {
			t.Setenv("E2E_BACKEND", tc.env)
			got := SelectedBackends()
			if len(got) != len(tc.want) {
				t.Fatalf("len: got %d want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] got %s want %s", i, got[i], tc.want[i])
				}
			}
		})
	}
}

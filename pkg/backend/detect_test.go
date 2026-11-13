package backend

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"
)

type fakeK8s struct {
	calls   int
	results []result
}

type result struct {
	cluster *scaleway.Cluster
	err     error
}

func (f *fakeK8s) GetCluster(_ context.Context, _ string) (*scaleway.Cluster, error) {
	r := f.results[f.calls%len(f.results)]
	f.calls++
	return r.cluster, r.err
}

func TestMapClusterType(t *testing.T) {
	cases := []struct {
		in      string
		want    Kind
		wantErr bool
	}{
		{"kapsule", KindKapsule, false},
		{"kapsule-dedicated-16", KindKapsule, false},
		{"kapsule-dedicated-x", KindKapsule, false},
		{"multicloud", KindKosmos, false},
		{"multicloud-dedicated-16", KindKosmos, false},
		{"", "", true},
		{"unknown", "", true},
		{"kosmos", "", true}, // Kosmos is the marketing name, the API name is multicloud
		{"KAPSULE", "", true},
	}
	for _, tc := range cases {
		got, err := mapClusterType(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("mapClusterType(%q): err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("mapClusterType(%q): got=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestParseKind(t *testing.T) {
	cases := []struct {
		in      string
		want    Kind
		wantErr bool
	}{
		{"kapsule", KindKapsule, false},
		{"kosmos", KindKosmos, false},
		{"KAPSULE", "", true},
		{"", "", true},
		{"unknown", "", true},
	}
	for _, tc := range cases {
		got, err := ParseKind(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseKind(%q): err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("ParseKind(%q): got=%q want=%q", tc.in, got, tc.want)
		}
	}
}

func TestResolveAuto_Success(t *testing.T) {
	api := &fakeK8s{results: []result{{cluster: &scaleway.Cluster{Type: "multicloud"}}}}
	got, err := ResolveAuto(context.Background(), api, "cluster-id")
	if err != nil {
		t.Fatalf("ResolveAuto: %v", err)
	}
	if got != KindKosmos {
		t.Fatalf("expected kosmos, got %q", got)
	}
	if api.calls != 1 {
		t.Fatalf("expected 1 GetCluster call, got %d", api.calls)
	}
}

func TestResolveAuto_UnknownTypeFailsFast(t *testing.T) {
	api := &fakeK8s{results: []result{{cluster: &scaleway.Cluster{Type: "wat"}}}}
	_, err := ResolveAuto(context.Background(), api, "cluster-id")
	if err == nil || !strings.Contains(err.Error(), "force-backend") {
		t.Fatalf("expected error mentioning force-backend kill-switch, got %v", err)
	}
	if api.calls != 1 {
		t.Fatalf("expected 1 call (no retry on unknown type), got %d", api.calls)
	}
}

func TestResolveAuto_RetriesOn5xx(t *testing.T) {
	// Override the retry delay to speed up the test.
	api := &fakeK8s{results: []result{
		{err: errors.New("500 internal")},
		{err: errors.New("502 bad gateway")},
		{cluster: &scaleway.Cluster{Type: "kapsule"}},
	}}
	// Run with a tight context but the default 5s sleep would blow the
	// test budget; use a context with deadline shorter than 3*5s and
	// override the package-level constant via a test-only ResolveAuto
	// re-implementation: easier to just call the helper with the canned
	// cluster pre-existing on second attempt and verify call count.
	//
	// We test the retry count, not the timing.
	got, err := resolveAutoWithDelay(context.Background(), api, "cluster-id", 0)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if got != KindKapsule {
		t.Fatalf("expected kapsule, got %q", got)
	}
	if api.calls != 3 {
		t.Fatalf("expected 3 GetCluster calls (retries until success), got %d", api.calls)
	}
}

func TestResolveAuto_GivesUpAfterMaxRetries(t *testing.T) {
	api := &fakeK8s{results: []result{{err: errors.New("503")}}}
	_, err := resolveAutoWithDelay(context.Background(), api, "cluster-id", 0)
	if err == nil {
		t.Fatal("expected error after exhausting retries, got nil")
	}
	if api.calls != detectRetryAttempts {
		t.Fatalf("expected %d attempts, got %d", detectRetryAttempts, api.calls)
	}
}

func TestResolveForced(t *testing.T) {
	if _, err := ResolveForced(""); err == nil {
		t.Fatal("expected error on empty string")
	}
	got, err := ResolveForced("kapsule")
	if err != nil || got != KindKapsule {
		t.Fatalf("ResolveForced(kapsule): got=%q err=%v", got, err)
	}
	if _, err := ResolveForced("invalid"); err == nil {
		t.Fatal("expected error on invalid")
	}
}

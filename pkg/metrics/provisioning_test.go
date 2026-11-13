package metrics

import (
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

func TestMeasureCreate_RecordsSuccess(t *testing.T) {
	provisioningDuration.Reset()
	err := MeasureCreate("kapsule", "fr-par-1", func() error {
		time.Sleep(5 * time.Millisecond)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count := testutil.CollectAndCount(provisioningDuration, "karpenter_scaleway_nodeclaim_provisioning_duration_seconds")
	if count != 1 {
		t.Fatalf("expected 1 series collected, got %d", count)
	}
}

func TestMeasureCreate_RecordsFailureLabel(t *testing.T) {
	provisioningDuration.Reset()
	sentinelErr := errors.New("boom")
	got := MeasureCreate("kosmos", "nl-ams-1", func() error { return sentinelErr })
	if !errors.Is(got, sentinelErr) {
		t.Fatalf("expected sentinel returned unchanged, got %v", got)
	}
	count := testutil.CollectAndCount(provisioningDuration, "karpenter_scaleway_nodeclaim_provisioning_duration_seconds")
	if count != 1 {
		t.Fatalf("expected 1 series, got %d", count)
	}
}

func TestMarkRejected_EmitsRejectedOutcomeOnly(t *testing.T) {
	provisioningDuration.Reset()
	MarkRejected("kapsule", "fr-par-1", 3*time.Millisecond)
	outcomes := collectOutcomeLabels(t)
	if len(outcomes) != 1 || outcomes[0] != "rejected" {
		t.Fatalf("expected single outcome=rejected series, got %v", outcomes)
	}
}

func TestMeasureCreate_InsufficientCapacityRecordsICEOutcome(t *testing.T) {
	provisioningDuration.Reset()
	ice := karpcp.NewInsufficientCapacityError(errors.New("no stock"))
	if got := MeasureCreate("kapsule", "fr-par-1", func() error { return ice }); !karpcp.IsInsufficientCapacityError(got) {
		t.Fatalf("expected ICE returned unchanged, got %v", got)
	}
	outcomes := collectOutcomeLabels(t)
	if len(outcomes) != 1 || outcomes[0] != "ice" {
		t.Fatalf("expected single outcome=ice series, got %v", outcomes)
	}
}

func TestMeasureCreate_PlainErrorRecordsFailOutcome(t *testing.T) {
	provisioningDuration.Reset()
	_ = MeasureCreate("kapsule", "fr-par-1", func() error { return errors.New("boom") })
	outcomes := collectOutcomeLabels(t)
	if len(outcomes) != 1 || outcomes[0] != "fail" {
		t.Fatalf("expected single outcome=fail series, got %v", outcomes)
	}
}

// collectOutcomeLabels collects current series from provisioningDuration
// and returns the outcome label of each. Used to assert that callers emit
// only the expected outcome bucket without polluting siblings.
func collectOutcomeLabels(t *testing.T) []string {
	t.Helper()
	ch := make(chan prometheus.Metric, 16)
	provisioningDuration.Collect(ch)
	close(ch)
	var out []string
	for m := range ch {
		var pb dto.Metric
		if err := m.Write(&pb); err != nil {
			t.Fatalf("write metric: %v", err)
		}
		for _, lp := range pb.Label {
			if lp.GetName() == "outcome" {
				out = append(out, lp.GetValue())
			}
		}
	}
	return out
}

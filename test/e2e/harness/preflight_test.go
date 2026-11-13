package harness

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeDiscovery is a minimal stub for discoveryProbe. It returns errs in
// order until exhausted, then returns nil. Concurrency-safe so tests can
// run under -race.
type fakeDiscovery struct {
	errs    []error
	idx     atomic.Int32
	callsAt []time.Time
}

func (f *fakeDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	f.callsAt = append(f.callsAt, time.Now())
	i := f.idx.Add(1) - 1
	if int(i) < len(f.errs) {
		return nil, nil, f.errs[i]
	}
	return nil, nil, nil
}

func TestPollDiscovery_SucceedsImmediatelyWhenReady(t *testing.T) {
	f := &fakeDiscovery{}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := PollDiscovery(ctx, f, 10*time.Millisecond); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if int(f.idx.Load()) != 1 {
		t.Errorf("expected 1 discovery call, got %d", f.idx.Load())
	}
}

func TestPollDiscovery_RetriesUntilSuccess(t *testing.T) {
	transient := errors.New("the server is currently unable to handle the request")
	f := &fakeDiscovery{errs: []error{transient, transient, transient}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := PollDiscovery(ctx, f, 10*time.Millisecond); err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if int(f.idx.Load()) != 4 {
		t.Errorf("expected 4 discovery calls (3 transient + 1 ok), got %d", f.idx.Load())
	}
}

func TestPollDiscovery_SurfacesLastErrorOnTimeout(t *testing.T) {
	flaky := errors.New("metrics.k8s.io/v1beta1: 503")
	f := &fakeDiscovery{errs: []error{flaky, flaky, flaky, flaky, flaky, flaky, flaky, flaky, flaky, flaky}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := PollDiscovery(ctx, f, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "metrics.k8s.io/v1beta1") {
		t.Errorf("error should surface the last underlying error; got: %v", err)
	}
	if !strings.Contains(err.Error(), "discovery never settled") {
		t.Errorf("error should include the harness-level marker; got: %v", err)
	}
}

func TestPollDiscovery_RejectsNilDiscoveryProbe(t *testing.T) {
	if err := PollDiscovery(context.Background(), nil, 10*time.Millisecond); err == nil {
		t.Fatal("expected error when discovery client is nil")
	}
}

func TestPollDiscovery_DefaultsEveryWhenZero(t *testing.T) {
	// every <= 0 should fall back to a sane default rather than busy-loop.
	// We only assert the function returns at all (with a forced ready stub).
	f := &fakeDiscovery{}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	if err := PollDiscovery(ctx, f, 0); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
}

func TestWaitForAPIDiscovery_RejectsEmptyKubeconfigPath(t *testing.T) {
	err := WaitForAPIDiscovery(context.Background(), "")
	if err == nil {
		t.Fatal("expected error on empty kubeconfig path")
	}
	if !strings.Contains(err.Error(), "kubeconfig path required") {
		t.Errorf("expected explicit message; got: %v", err)
	}
}

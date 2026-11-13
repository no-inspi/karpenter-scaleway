package kapsule

// Regression tests — Kapsule node stuck in creation_error.
//
// Scenario: under Scaleway capacity pressure the instance backing a freshly
// created pool lands in the terminal "creation_error" status. Before the fix
// the backend waited the whole createTimeout, then returned a generic error,
// and Karpenter retried the exact same (instance type, zone) offering.
//
// Expected: Create deletes the failed pool, returns an
// InsufficientCapacityError, and GetInstanceTypes reports the offering as
// unavailable for a while so Karpenter's next attempt picks another type or
// zone.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/no-inspi/karpenter-scaleway/pkg/scaleway"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/scaleway/scaleway-sdk-go/scw"
	corev1 "k8s.io/api/core/v1"
	karpv1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	karpcp "sigs.k8s.io/karpenter/pkg/cloudprovider"
)

const (
	ceType = "GP1-XS"
	ceZone = "fr-par-1"
)

func creationErrorAPI() *fakeKapsule {
	return &fakeKapsule{
		countTotal: 10,
		countKarp:  10,
		createPoolFn: func(_ context.Context, p scaleway.CreatePoolParams) (*scaleway.KapsulePool, error) {
			return &scaleway.KapsulePool{ID: "pool-failed", NodeClaim: p.Name, NodeType: p.InstanceType, Zone: p.Zone, Size: 1}, nil
		},
		waitForNodeFn: func(_ context.Context, poolID string, _ time.Duration) (*scaleway.KapsuleNode, error) {
			return nil, fmt.Errorf("node n-1 in pool %s: %w", poolID, scaleway.ErrNodeCreationError)
		},
	}
}

func TestCreate_CreationErrorReturnsInsufficientCapacityAndDeletesPool(t *testing.T) {
	api := creationErrorAPI()
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass(ceZone))

	out, err := bk.Create(context.Background(), mkNCForCreate("nc-ce", ceType, ceZone))
	if out != nil {
		t.Fatalf("expected nil NodeClaim, got %+v", out)
	}
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Fatalf("expected InsufficientCapacityError, got %T: %v", err, err)
	}
	if len(api.deletePoolCalls) != 1 || api.deletePoolCalls[0] != "pool-failed" {
		t.Fatalf("expected failed pool to be deleted once, got %v", api.deletePoolCalls)
	}
}

func TestCreate_CreationErrorMarksOfferingUnavailable(t *testing.T) {
	api := creationErrorAPI()
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass(ceZone, "fr-par-2"))
	now := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	bk.unavailable.now = func() time.Time { return now }

	if avail := offeringAvailable(t, bk, ceType, ceZone); !avail {
		t.Fatalf("precondition: %s/%s should be available before the failure", ceType, ceZone)
	}

	if _, err := bk.Create(context.Background(), mkNCForCreate("nc-ce", ceType, ceZone)); err == nil {
		t.Fatal("expected Create to fail")
	}

	if offeringAvailable(t, bk, ceType, ceZone) {
		t.Fatalf("%s/%s must be unavailable right after creation_error", ceType, ceZone)
	}
	if !offeringAvailable(t, bk, ceType, "fr-par-2") {
		t.Fatalf("%s/fr-par-2 must stay available (only the failed zone is marked)", ceType)
	}

	now = now.Add(unavailableOfferingTTL + time.Second)
	if !offeringAvailable(t, bk, ceType, ceZone) {
		t.Fatalf("%s/%s must be available again after the TTL", ceType, ceZone)
	}
}

// Non-capacity failures (timeout, API error) keep the previous behaviour:
// plain error, pool cleaned up, offering left untouched.
func TestCreate_WaitTimeoutIsNotInsufficientCapacity(t *testing.T) {
	api := creationErrorAPI()
	api.waitForNodeFn = func(context.Context, string, time.Duration) (*scaleway.KapsuleNode, error) {
		return nil, context.DeadlineExceeded
	}
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass(ceZone))

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-to", ceType, ceZone))
	if err == nil || karpcp.IsInsufficientCapacityError(err) {
		t.Fatalf("expected a plain error, got %v", err)
	}
	if len(api.deletePoolCalls) != 1 {
		t.Fatalf("expected pool cleanup, got %v", api.deletePoolCalls)
	}
	if !offeringAvailable(t, bk, ceType, ceZone) {
		t.Fatal("timeout must not mark the offering unavailable")
	}
}

func offeringAvailable(t *testing.T, bk *Kapsule, instanceType, zone string) bool {
	t.Helper()
	np := &karpv1.NodePool{}
	np.Spec.Template.Spec.NodeClassRef = &karpv1.NodeClassReference{Name: "default"}
	its, err := bk.GetInstanceTypes(context.Background(), np)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	for _, it := range its {
		if it.Name != instanceType {
			continue
		}
		for _, o := range it.Offerings {
			if r := o.Requirements.Get(corev1.LabelTopologyZone); r != nil && r.Has(zone) {
				return o.Available
			}
		}
	}
	t.Fatalf("offering %s/%s not found in catalog", instanceType, zone)
	return false
}

// --- failed-pool cleanup (PR #23 review) ---

// capturedLogs collects formatted log lines so tests can assert that a
// failed cleanup is visible rather than silently swallowed.
type capturedLogs struct {
	mu    sync.Mutex
	lines []string
}

func (c *capturedLogs) logger() logr.Logger {
	return funcr.New(func(prefix, args string) {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.lines = append(c.lines, prefix+" "+args)
	}, funcr.Options{})
}

func (c *capturedLogs) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func newCleanupBackend(t *testing.T, api *fakeKapsule) (*Kapsule, *capturedLogs) {
	t.Helper()
	bk := newKapsuleWithQuota(t, api, QuotaConfig{}, mkNodeClass(ceZone))
	logs := &capturedLogs{}
	bk.log = logs.logger()
	bk.failedPoolDeleteRetryInterval = time.Millisecond
	bk.failedPoolDeleteTimeout = 200 * time.Millisecond
	return bk, logs
}

var errPoolScaling = fmt.Errorf("delete pool pool-failed: %w",
	&scw.TransientStateError{Resource: "pool", ResourceID: "pool-failed", CurrentState: "scaling"})

// Scaleway rejects DeletePool while the creation_error pool is still
// "scaling": the cleanup must retry until the delete goes through.
func TestCreate_CreationErrorRetriesDeleteWhilePoolTransient(t *testing.T) {
	api := creationErrorAPI()
	api.deletePoolErrs = []error{errPoolScaling, errPoolScaling}
	bk, logs := newCleanupBackend(t, api)

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-ce", ceType, ceZone))
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Fatalf("expected InsufficientCapacityError, got %v", err)
	}
	if len(api.deletePoolCalls) != 3 {
		t.Fatalf("expected 3 DeletePool attempts (2 transient + 1 success), got %v", api.deletePoolCalls)
	}
	if !logs.contains("retrying") {
		t.Fatalf("transient delete failures must be logged, got %v", logs.lines)
	}
}

// A permanent DeletePool failure is not retried but is logged as an error
// (the pool may be leaked), and Create still reports the original cause.
func TestCreate_CreationErrorDeletePoolPermanentFailureIsLogged(t *testing.T) {
	api := creationErrorAPI()
	api.deletePoolErr = fmt.Errorf("delete pool pool-failed: %w", &scw.ResponseError{StatusCode: 403, Message: "forbidden"})
	bk, logs := newCleanupBackend(t, api)

	_, err := bk.Create(context.Background(), mkNCForCreate("nc-ce", ceType, ceZone))
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Fatalf("cleanup failure must not mask the ICE, got %v", err)
	}
	if len(api.deletePoolCalls) != 1 {
		t.Fatalf("permanent error must not be retried, got %v", api.deletePoolCalls)
	}
	if !logs.contains("pool may be leaked") || !logs.contains("pool-failed") {
		t.Fatalf("permanent delete failure must be logged with the pool id, got %v", logs.lines)
	}
}

// A pool stuck in a transient state is retried only up to the deadline.
func TestCreate_CreationErrorDeleteRetryIsBounded(t *testing.T) {
	api := creationErrorAPI()
	api.deletePoolErr = errPoolScaling
	bk, logs := newCleanupBackend(t, api)

	start := time.Now()
	_, err := bk.Create(context.Background(), mkNCForCreate("nc-ce", ceType, ceZone))
	if !karpcp.IsInsufficientCapacityError(err) {
		t.Fatalf("expected InsufficientCapacityError, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("cleanup retry not bounded: took %s", elapsed)
	}
	if len(api.deletePoolCalls) < 2 {
		t.Fatalf("expected several DeletePool attempts before giving up, got %d", len(api.deletePoolCalls))
	}
	if !logs.contains("timed out deleting failed kapsule pool") {
		t.Fatalf("give-up must be logged, got %v", logs.lines)
	}
}

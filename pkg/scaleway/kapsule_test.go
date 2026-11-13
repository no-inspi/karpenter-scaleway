package scaleway

// Regression tests for KapsuleClient edge-case error absorption.
//
// The Scaleway K8s API returns a transient_state error with current_state ==
// "deleting" when a DeletePool request races with an in-progress pool
// teardown. KapsuleClient.DeletePool must absorb that error and return nil
// (idempotent success) so Karpenter does not treat it as a fatal failure.
//
// These tests use a real scw.Client pointed at an httptest.Server to exercise
// the full SDK error-parsing stack, rather than mocking at the KapsuleAPI
// interface level.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	k8s "github.com/scaleway/scaleway-sdk-go/api/k8s/v1"
	"github.com/scaleway/scaleway-sdk-go/scw"
)

// newKapsuleClientForTest creates a KapsuleClient backed by a scw.Client
// whose API URL is pointed at the provided httptest.Server. The fake auth
// values must pass client-side format checks but are never sent anywhere real.
func newKapsuleClientForTest(t *testing.T, srv *httptest.Server) *KapsuleClient {
	t.Helper()
	scwClient, err := scw.NewClient(
		scw.WithAuth("SCW0000000000000FAKE", "00000000-0000-4000-8000-000000000000"),
		scw.WithDefaultProjectID("00000000-0000-4000-8000-000000000000"),
		scw.WithAPIURL(srv.URL),
	)
	if err != nil {
		t.Fatalf("scw.NewClient: %v", err)
	}
	return &KapsuleClient{
		api:       k8s.NewAPI(scwClient),
		region:    scw.RegionFrPar,
		clusterID: "cluster-uuid",
	}
}

// TestCreatePool_NameConflictRecoversFromExistingPool is the regression test
// for this edge case: when CreatePool hits a unique-name constraint error
// (invalid_arguments / argument_name == "name"), KapsuleClient.CreatePool must
// recover by calling findPoolByNodeClaim (via ListPools) and returning the
// orphaned pool rather than propagating the error.
//
// Counterfactual: if the recovery block at pkg/scaleway/kapsule.go:91-96 is
// removed, this test fails with a non-nil InvalidArgumentsError.
func TestCreatePool_NameConflictRecoversFromExistingPool(t *testing.T) {
	const ncName = "test-nodeclaim-abc123"
	const existingPoolID = "pool-existing-uuid"

	// Build the invalid_arguments error body the Scaleway K8s API would return
	// when a second CreatePool hits the unique pool-name constraint.
	conflictBody, err := json.Marshal(map[string]interface{}{
		"type": "invalid_arguments",
		"details": []map[string]string{
			{
				"argument_name": "name",
				"reason":        "constraint_must_be_unique",
				"help_message":  "name must be unique within the cluster",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal conflict body: %v", err)
	}

	// Build the ListPools response containing the already-existing pool.
	// The pool carries the "nodeclaim=<ncName>" tag so that toKapsulePool /
	// findPoolByNodeClaim can match it.
	listBody, err := json.Marshal(map[string]interface{}{
		"total_count": 1,
		"pools": []map[string]interface{}{
			{
				"id":                 existingPoolID,
				"cluster_id":         "cluster-uuid",
				"name":               kapsulePoolName(ncName),
				"status":             "ready",
				"node_type":          "DEV1-M",
				"autoscaling":        false,
				"size":               1,
				"min_size":           1,
				"max_size":           1,
				"version":            "1.30.0",
				"zone":               "fr-par-1",
				"tags":               []string{TagKarpenterManaged, nodeClaimTagPrefix + ncName},
				"kubelet_args":       map[string]string{},
				"container_runtime":  "containerd",
				"root_volume_type":   "l_ssd",
				"public_ip_disabled": false,
				"security_group_id":  "",
				"region":             "fr-par",
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal list body: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pools"):
			// Simulate the unique-name conflict on CreatePool.
			w.WriteHeader(http.StatusBadRequest) // 400
			_, _ = w.Write(conflictBody)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/pools"):
			// Simulate ListPools returning the orphaned pool.
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(listBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := newKapsuleClientForTest(t, srv)
	pool, gotErr := client.CreatePool(context.Background(), CreatePoolParams{
		Zone:         scw.Zone("fr-par-1"),
		InstanceType: "DEV1-M",
		Name:         ncName,
	})
	if gotErr != nil {
		t.Fatalf("CreatePool returned non-nil error for name conflict (recovery failed): %v", gotErr)
	}
	if pool == nil {
		t.Fatal("CreatePool returned nil pool despite recovery succeeding")
	}
	if pool.ID != existingPoolID {
		t.Errorf("recovered pool ID = %q, want %q", pool.ID, existingPoolID)
	}
	if pool.NodeClaim != ncName {
		t.Errorf("recovered pool NodeClaim = %q, want %q", pool.NodeClaim, ncName)
	}
}

// TestCreatePool_SetsUnregisteredStartupTaint guards the startup taint: Karpenter v1
// expects every managed node to register with karpenter.sh/unregistered:
// NoExecute and removes it itself once registration completes. It must be a
// startup taint (applied once, not reconciled): a reconciled pool taint would
// be put back by Kapsule after Karpenter removes it.
func TestCreatePool_SetsUnregisteredStartupTaint(t *testing.T) {
	var got k8s.CreatePoolRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/pools") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode CreatePool body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"pool-uuid","zone":"fr-par-1","node_type":"dev1_m","size":1}`))
	}))
	defer srv.Close()

	client := newKapsuleClientForTest(t, srv)
	if _, err := client.CreatePool(context.Background(), CreatePoolParams{
		Zone:         scw.Zone("fr-par-1"),
		InstanceType: "DEV1-M",
		Name:         "nc-taint",
	}); err != nil {
		t.Fatalf("CreatePool: %v", err)
	}

	if len(got.StartupTaints) != 1 {
		t.Fatalf("startup_taints = %+v, want exactly the unregistered taint", got.StartupTaints)
	}
	taint := got.StartupTaints[0]
	if taint.Key != "karpenter.sh/unregistered" || taint.Effect != k8s.CoreV1TaintEffectNoExecute {
		t.Errorf("startup taint = %+v, want karpenter.sh/unregistered:NoExecute", taint)
	}
	if len(got.Taints) != 0 {
		t.Errorf("taints = %+v, want none (reconciled taints would be re-applied after Karpenter removes them)", got.Taints)
	}
}

// TestDeletePool_TransientStateDeletingIsNil is the regression test for
// this edge case: DeletePool must return nil when the SDK responds with a
// transient_state error whose current_state is "deleting" — the pool is
// already being torn down, so this is idempotent success.
func TestDeletePool_TransientStateDeletingIsNil(t *testing.T) {
	// Build the JSON body that the Scaleway K8s API would return for a
	// DELETE /k8s/v1/regions/fr-par/pools/<id> request on an already-deleting
	// pool. The SDK's hasResponseError path deserialises this into a
	// *scw.TransientStateError via unmarshalStandardError("transient_state",…).
	body, err := json.Marshal(map[string]interface{}{
		"message":       "pool is already being deleted",
		"type":          "transient_state",
		"resource":      "pool",
		"resource_id":   "pool-deleting",
		"current_state": "deleting",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict) // 409 — arbitrary non-2xx that carries the body
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	client := newKapsuleClientForTest(t, srv)
	gotErr := client.DeletePool(context.Background(), "pool-deleting")
	if gotErr != nil {
		t.Fatalf("DeletePool returned non-nil error for transient_state/deleting: %v", gotErr)
	}
}

// nodesServer serves ListNodes for a pool, returning the status produced by
// statusFor(call) on the n-th call (1-based). A status of the form
// "http:<code>" makes that call fail with the given HTTP status instead.
// Any other route 404s.
func nodesServer(t *testing.T, statusFor func(call int32) string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/nodes") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		n := calls.Add(1)
		status := statusFor(n)
		if code, ok := strings.CutPrefix(status, "http:"); ok {
			c, _ := strconv.Atoi(code)
			w.WriteHeader(c)
			_, _ = w.Write([]byte(`{"message":"injected failure","type":"unknown"}`))
			return
		}
		body, _ := json.Marshal(map[string]interface{}{
			"total_count": 1,
			"nodes": []map[string]interface{}{{
				"id":          "node-uuid",
				"pool_id":     "pool-uuid",
				"cluster_id":  "cluster-uuid",
				"name":        "scw-karp-node",
				"provider_id": "scaleway://instance/fr-par-1/instance-uuid",
				"status":      status,
				"region":      "fr-par",
			}},
		})
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// TestWaitForNodeReady_CreationErrorFailsFast is the regression test for
// this edge case: when Scaleway moves the pool's node to "creation_error"
// (capacity shortage), WaitForNodeReady must return ErrNodeCreationError
// right away instead of waiting for the full timeout.
func TestWaitForNodeReady_CreationErrorFailsFast(t *testing.T) {
	srv, calls := nodesServer(t, func(call int32) string {
		if call < 3 {
			return "creating"
		}
		return "creation_error"
	})
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	start := time.Now()
	node, err := client.WaitForNodeReady(context.Background(), "pool-uuid", time.Minute)
	if !errors.Is(err, ErrNodeCreationError) {
		t.Fatalf("want ErrNodeCreationError, got node=%v err=%v", node, err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("WaitForNodeReady took %s, want fail-fast", elapsed)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("ListNodes calls = %d, want 3", got)
	}
}

func TestWaitForNodeReady_ReturnsReadyNode(t *testing.T) {
	srv, _ := nodesServer(t, func(call int32) string {
		if call < 2 {
			return "creating"
		}
		return "ready"
	})
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	node, err := client.WaitForNodeReady(context.Background(), "pool-uuid", time.Minute)
	if err != nil {
		t.Fatalf("WaitForNodeReady: %v", err)
	}
	if node.ProviderID != "scaleway://instance/fr-par-1/instance-uuid" || node.Name != "scw-karp-node" {
		t.Fatalf("unexpected node %+v", node)
	}
}

func TestWaitForNodeReady_TimesOut(t *testing.T) {
	srv, _ := nodesServer(t, func(int32) string { return "creating" })
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	_, err := client.WaitForNodeReady(context.Background(), "pool-uuid", 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if errors.Is(err, ErrNodeCreationError) {
		t.Fatalf("timeout must not be reported as creation_error: %v", err)
	}
}

// Regression test for the PR #23 review: a transient ListNodes failure
// (429 / 5xx) must not abort the wait — it is logged and polling continues.
func TestWaitForNodeReady_RetriesTransientListErrors(t *testing.T) {
	srv, calls := nodesServer(t, func(call int32) string {
		switch call {
		case 1:
			return "http:503"
		case 2:
			return "http:429"
		case 3:
			return "creating"
		default:
			return "ready"
		}
	})
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	node, err := client.WaitForNodeReady(context.Background(), "pool-uuid", time.Minute)
	if err != nil {
		t.Fatalf("WaitForNodeReady must survive 503/429, got %v", err)
	}
	if node == nil || node.Name != "scw-karp-node" {
		t.Fatalf("unexpected node %+v", node)
	}
	if got := calls.Load(); got != 4 {
		t.Fatalf("ListNodes calls = %d, want 4", got)
	}
}

// A permanent ListNodes failure (4xx other than 429) still aborts at once.
func TestWaitForNodeReady_PermanentListErrorAborts(t *testing.T) {
	srv, calls := nodesServer(t, func(int32) string { return "http:403" })
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	_, err := client.WaitForNodeReady(context.Background(), "pool-uuid", time.Minute)
	if err == nil {
		t.Fatal("expected an error on 403")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("ListNodes calls = %d, want 1 (no retry on permanent error)", got)
	}
}

// Transient errors until the deadline surface as a timeout carrying the last
// error, not as the transient error itself.
func TestWaitForNodeReady_TransientErrorsUntilTimeout(t *testing.T) {
	srv, _ := nodesServer(t, func(int32) string { return "http:502" })
	client := newKapsuleClientForTest(t, srv)
	client.nodePollInterval = time.Millisecond

	_, err := client.WaitForNodeReady(context.Background(), "pool-uuid", 50*time.Millisecond)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want context.DeadlineExceeded, got %v", err)
	}
	if !strings.Contains(err.Error(), "list nodes error") {
		t.Fatalf("timeout error should mention the last list error, got %v", err)
	}
}

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"transient state scaling", &scw.TransientStateError{Resource: "pool", CurrentState: "scaling"}, true},
		{"wrapped transient", fmt.Errorf("delete pool p: %w", &scw.TransientStateError{CurrentState: "scaling"}), true},
		{"429", &scw.ResponseError{StatusCode: 429}, true},
		{"500", &scw.ResponseError{StatusCode: 500}, true},
		{"503", &scw.ResponseError{StatusCode: 503}, true},
		{"400", &scw.ResponseError{StatusCode: 400}, false},
		{"403", &scw.ResponseError{StatusCode: 403}, false},
		{"not found", &scw.ResourceNotFoundError{Resource: "pool"}, false},
		{"plain", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryable(tc.err); got != tc.want {
				t.Fatalf("IsRetryable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

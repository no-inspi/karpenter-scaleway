package harness

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	scwscheme "github.com/no-inspi/karpenter-scaleway/pkg/scheme"
)

// addImageOverrides copies E2E_IMAGE_REPO / E2E_IMAGE_TAG into the
// helm Values map when set. The CI build-image job (e2e.yml) sets
// these to point each smoke run at the freshly-pushed :<sha>; locally
// they're typically empty and the chart falls back to the
// values.yaml defaults.
func addImageOverrides(values map[string]string) {
	if v := os.Getenv("E2E_IMAGE_REPO"); v != "" {
		values["image.repository"] = v
	}
	if v := os.Getenv("E2E_IMAGE_TAG"); v != "" {
		values["image.tag"] = v
	}
}

// Backend names are kept as a string type so scenario authors can pass
// them via `t.Run("kapsule", ...)` cleanly.
type Backend string

const (
	BackendKapsule Backend = "kapsule"
	BackendKosmos  Backend = "kosmos"
)

// Cluster bundles the per-test plumbing scenarios use.
type Cluster struct {
	Backend    Backend
	RunID      string
	Region     string
	ClusterID  string
	Kubeconfig []byte
	KubeClient client.Client
	Typed      kubernetes.Interface
	// Namespace is the workload namespace where scenarios create
	// test Pods/NodePools/NodeClasses. NOT the Karpenter controller
	// namespace (which is "karpenter", set by KarpenterInstaller).
	Namespace string
}

// SetupCluster runs the matching Terraform module, writes the
// kubeconfig to a temp file, installs the Helm chart, and returns a
// ready-to-use Cluster. Cleanup is registered via t.Cleanup: helm
// uninstall first (best-effort), then terraform destroy.
//
// A single scenario typically owns its cluster end-to-end: 5-10 min
// for TF apply, ~30 s for chart install, ~few minutes for the test
// itself, then 2-5 min for destroy. Total cell wall-time ≤ 25 min by
// design.
func SetupCluster(t *testing.T, b Backend) *Cluster {
	t.Helper()

	moduleSubdir := string(b)
	moduleDir, err := AbsModuleDir("test", "e2e", "terraform", moduleSubdir)
	if err != nil {
		t.Fatalf("locate TF module: %v", err)
	}

	runID := RunIDFromEnv()
	region := os.Getenv("E2E_REGION")
	if region == "" {
		region = "fr-par"
	}
	projectID := requireEnv(t, "SCW_DEFAULT_PROJECT_ID")
	orgID := requireEnv(t, "SCW_DEFAULT_ORGANIZATION_ID")

	tf := &Terraform{
		ModuleDir: moduleDir,
		Vars: map[string]string{
			"run_id":          runID,
			"project_id":      projectID,
			"organization_id": orgID,
			"region":          region,
		},
	}

	// Register terraform destroy BEFORE calling Apply — so even if Apply
	// panics mid-way, the cleanup goroutine fires and we don't leak a
	// paid Scaleway cluster.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if err := tf.Destroy(ctx); err != nil {
			t.Logf("terraform destroy (run_id=%s): %v", runID, err)
		}
	})

	applyCtx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	res, err := tf.Apply(applyCtx)
	if err != nil {
		t.Fatalf("terraform apply: %v", err)
	}

	clusterID := res.Outputs["cluster_id"]
	kubeconfig := []byte(res.Outputs["kubeconfig"])
	// Write the kubeconfig to a stable, scenario-discoverable path so the
	// e2e.yml workflow's failure-time log-collection step can find it.
	// /tmp keeps it readable from the parent process; the run_id makes
	// concurrent test invocations non-overlapping; t.Cleanup removes it.
	kubeconfigPath := filepath.Join("/tmp", fmt.Sprintf("e2e-%s.kubeconfig", runID))
	if err := os.WriteFile(kubeconfigPath, kubeconfig, 0o600); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(kubeconfigPath) })

	// Pre-flight: helm's discovery phase calls ServerGroupsAndResources at
	// the start of `upgrade --install`. On a fresh Kapsule cluster, the
	// metrics-server addon registers `metrics.k8s.io/v1beta1` *after* the
	// cluster reports Ready but before its pod is healthy, surfacing as
	// "client rate limiter ... context deadline exceeded" and aborting
	// before helm's own --wait/--timeout apply. Replicate the discovery
	// call here and retry until it returns clean — once we can do it,
	// helm will too.
	//
	// Kosmos gets a longer budget: metrics-server stays Pending until the
	// bootstrap external node joins (~5-7 min), so its APIService cannot
	// settle before then.
	preflightBudget := 5 * time.Minute
	if b == BackendKosmos {
		preflightBudget = 12 * time.Minute
	}
	preflightCtx, preflightCancel := context.WithTimeout(context.Background(), preflightBudget)
	defer preflightCancel()
	if err := WaitForAPIDiscovery(preflightCtx, kubeconfigPath); err != nil {
		dumpKubeDiagnostics(t, kubeconfigPath)
		t.Fatalf("preflight: API discovery never settled: %v", err)
	}

	chartDir, err := AbsModuleDir("charts", "karpenter-scaleway")
	if err != nil {
		t.Fatalf("locate chart: %v", err)
	}

	values := map[string]string{
		"scaleway.accessKey":      res.Outputs["scw_access_key"],
		"scaleway.secretKey":      res.Outputs["scw_secret_key"],
		"scaleway.organizationID": orgID,
		// Instances (Kosmos) are created in this project; it must match the
		// project the controller IAM key is scoped to (iam.tf project_ids),
		// else "insufficient permissions: create compute_servers".
		"scaleway.projectID": projectID,
		"scaleway.clusterID": clusterID,
		"scaleway.region":    region,
		"forceBackend":       string(b),
	}
	if b == BackendKosmos {
		values["scaleway.kosmos.poolID"] = res.Outputs["kosmos_pool_id"]
		values["scaleway.kosmos.apiKey"] = res.Outputs["kosmos_pool_api_key"]
	}
	addImageOverrides(values)

	installer := &KarpenterInstaller{
		ChartDir:       chartDir,
		KubeconfigPath: kubeconfigPath,
		Namespace:      "karpenter",
		Values:         values,
	}
	// WaitTimeoutSeconds keeps the 600 s default (install.go) for both
	// backends: on Kosmos the controller lands on the bootstrap external
	// node, which registers asynchronously (~5-7 min: download k8s-agent,
	// install kubelet/containerd/CNI, join) before the pod can schedule.

	// Register helm uninstall BEFORE calling Install — same panic-safety
	// rationale as the terraform destroy cleanup above.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := installer.Uninstall(ctx); err != nil {
			t.Logf("helm uninstall (run_id=%s): %v", runID, err)
		}
	})

	// installCtx must give helm strictly more wall-time than its --timeout
	// (600 s); otherwise the parent context SIGKILLs helm before it can
	// produce a structured error ("signal: killed" instead of "deployment X
	// failed to become Ready"). 15 min also wraps Kosmos bootstrap latency.
	installCtx, cancelInstall := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancelInstall()
	if err := installer.Install(installCtx); err != nil {
		// Dump cluster state while it is still up — the terraform-destroy
		// cleanup is registered earlier so it fires AFTER this, and the
		// e2e.yml log-collection step runs after the cluster is already
		// gone. Without this, an unready controller is undebuggable.
		dumpKubeDiagnostics(t, kubeconfigPath)
		t.Fatalf("helm install: %v", err)
	}

	cfg, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		t.Fatalf("parse kubeconfig: %v", err)
	}
	scheme := scwscheme.New()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	typed, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatalf("typed client: %v", err)
	}

	return &Cluster{
		Backend:    b,
		RunID:      runID,
		Region:     region,
		ClusterID:  clusterID,
		Kubeconfig: kubeconfig,
		KubeClient: c,
		Typed:      typed,
		Namespace:  "default",
	}
}

// dumpKubeDiagnostics best-effort logs cluster state (nodes, controller
// pods + their events) to the test log. Called on install/setup failure
// while the cluster is still alive — the terraform-destroy cleanup fires
// afterwards, and the e2e.yml log-collection step runs once the cluster is
// already gone, so this is the only window that captures why a pod is
// unready. kubectl is preinstalled on GitHub ubuntu runners; if it is
// missing the failures are logged and swallowed.
func dumpKubeDiagnostics(t *testing.T, kubeconfigPath string) {
	t.Helper()
	invocations := [][]string{
		{"get", "nodes", "-o", "wide"},
		{"-n", "karpenter", "get", "pods", "-o", "wide"},
		{"-n", "karpenter", "describe", "pods"},
		{"get", "nodeclaims", "-A", "-o", "wide"},
		{"get", "nodepools,scalewaynodeclasses", "-A", "-o", "wide"},
		{"describe", "nodeclaims", "-A"},
		{"get", "events", "-A", "--sort-by=.lastTimestamp"},
		{"-n", "karpenter", "logs", "deploy/karpenter-scaleway", "--tail=400"},
	}
	for _, args := range invocations {
		full := append([]string{"--kubeconfig", kubeconfigPath}, args...)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		out, err := exec.CommandContext(ctx, "kubectl", full...).CombinedOutput()
		cancel()
		t.Logf("=== kubectl %v ===\n%s\n(err=%v)", args, out, err)
	}
}

// DumpDiagnostics writes the cluster's kubeconfig to a temp file and logs
// cluster state (see dumpKubeDiagnostics). Call it inline at a scenario's
// failure point — before t.Fatalf and before the per-test cleanups delete
// the NodePool/NodeClaims — so the captured state reflects the moment of
// failure. The e2e.yml log-collection step can't do this: it runs after
// terraform-destroy has already torn the cluster down.
func (c *Cluster) DumpDiagnostics(t *testing.T) {
	t.Helper()
	f, err := os.CreateTemp("", "e2e-diag-*.kubeconfig")
	if err != nil {
		t.Logf("DumpDiagnostics: temp kubeconfig: %v", err)
		return
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(c.Kubeconfig); err != nil {
		t.Logf("DumpDiagnostics: write kubeconfig: %v", err)
		_ = f.Close()
		return
	}
	_ = f.Close()
	dumpKubeDiagnostics(t, f.Name())
}

// RunIDFromEnv returns $E2E_RUN_ID if set, else a random 8-hex slug
// prefixed with "loc-" so scenarios run from a developer laptop are
// distinguishable from CI runs.
func RunIDFromEnv() string {
	if id := os.Getenv("E2E_RUN_ID"); id != "" {
		return id
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "loc-" + hex.EncodeToString(b[:])
}

// BackendFromEnv reads $E2E_BACKEND. Used by the workflow when it
// wants to gate a single matrix axis from outside the test source.
func BackendFromEnv() (Backend, error) {
	switch v := os.Getenv("E2E_BACKEND"); v {
	case "kapsule":
		return BackendKapsule, nil
	case "kosmos":
		return BackendKosmos, nil
	default:
		return "", fmt.Errorf("E2E_BACKEND must be 'kapsule' or 'kosmos', got %q", v)
	}
}

// SelectedBackends returns the backends a scenario should iterate over.
// If E2E_BACKEND is set to "kapsule" or "kosmos", returns just that
// backend. If unset, returns both. The CI workflow sets E2E_BACKEND so
// each matrix cell only spins up one cluster; local developers can
// leave it unset and exercise both backends.
//
// Unknown values (e.g. "kapsuel" typo) fall back to "both" instead of
// erroring. This prefers an over-broad test run to a silent zero-test
// run, but it does mean a typo costs ~2× the cluster time. The matrix
// in e2e.yml only ever passes valid values, so the typo path is
// limited to manual `make e2e-cloud` invocations.
func SelectedBackends() []Backend {
	if v := os.Getenv("E2E_BACKEND"); v != "" {
		switch Backend(v) {
		case BackendKapsule:
			return []Backend{BackendKapsule}
		case BackendKosmos:
			return []Backend{BackendKosmos}
		}
	}
	return []Backend{BackendKapsule, BackendKosmos}
}

// requireEnv fails the current test if `name` is unset. Defined here
// so all scenarios share the same error format.
func requireEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required for real-cluster scenarios", name)
	}
	return v
}

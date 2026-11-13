package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
)

// KarpenterInstaller invokes `helm upgrade --install` against a real
// cluster's kubeconfig and waits for the controller Deployment to
// become Ready.
type KarpenterInstaller struct {
	// ChartDir is the path to the Helm chart on disk (e.g.
	// charts/karpenter-scaleway from the repo root).
	ChartDir string
	// KubeconfigPath is the path to a kubeconfig file the harness
	// wrote out from the Terraform output.
	KubeconfigPath string
	// Namespace is the release namespace; it will be created if it
	// does not exist.
	Namespace string
	// Values are passed to helm via `--set k=v`. Values containing
	// commas or special characters should not be passed here — use
	// ValuesFile for those (not yet supported, add when needed).
	//
	// Secret-bearing keys (passwords, API keys) appear on the helm
	// argv. GitHub Actions step-debug logging and any log scraper
	// that captures process arguments will see them. Move secrets to
	// a values file (or `--set-file`) before enabling step-debug in
	// CI.
	Values map[string]string
	// ReleaseName overrides the Helm release name. Defaults to
	// "karpenter-scaleway".
	ReleaseName string
	// WaitTimeoutSeconds is the helm --wait timeout; defaults to 600 (defaultWaitSeconds).
	WaitTimeoutSeconds int

	helmBin string
}

// defaultWaitSeconds is the helm operation timeout (covers discovery, CRD
// validation, apply, and --wait readiness in one budget). 240 s was tight
// enough on a fresh Kapsule cluster that helm's client-go rate limiter
// drained its context inside the discovery phase ("client rate limiter
// Wait returned an error: context deadline exceeded"). 600 s gives ~3×
// observed wall-time and matches the 10 min cluster-bootstrap window
// Kapsule typically needs before all addons stabilise.
const defaultWaitSeconds = 600

// defaultBurstLimit raises helm's client-side throttle from its 100/s
// default. The chart instantiates a few CRDs whose OpenAPI validation
// generates dozens of `unrecognized format "int32/int64"` calls each;
// 200/s leaves headroom for the apiserver's own rate limiting.
const defaultBurstLimit = 200

func (k *KarpenterInstaller) Install(ctx context.Context) error {
	if err := k.locateHelm(); err != nil {
		return err
	}
	if k.ReleaseName == "" {
		k.ReleaseName = "karpenter-scaleway"
	}
	if k.WaitTimeoutSeconds <= 0 {
		k.WaitTimeoutSeconds = defaultWaitSeconds
	}
	args := []string{
		"upgrade", "--install", k.ReleaseName, k.ChartDir,
		"--namespace", k.Namespace,
		"--create-namespace",
		"--kubeconfig", k.KubeconfigPath,
		"--wait",
		"--timeout", strconv.Itoa(k.WaitTimeoutSeconds) + "s",
		"--burst-limit", strconv.Itoa(defaultBurstLimit),
		// Kapsule's apiserver serves the /openapi/v3/* endpoints slowly on
		// fresh clusters; helm fans out one call per resource kind in the
		// chart, queues them through client-go's rate limiter, and the
		// per-call contexts fire ("client rate limiter Wait returned an
		// error: context deadline exceeded") long before --timeout. Helm's
		// OpenAPI validation is redundant here: the chart is already
		// validated against a real in-process apiserver via `make envtest`
		// in CI, and the apply phase will surface any real schema errors
		// when the apiserver itself rejects the resource.
		"--disable-openapi-validation",
	}
	for _, kv := range k.sortedValueArgs() {
		args = append(args, "--set", kv)
	}
	cmd := exec.CommandContext(ctx, k.helmBin, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("helm upgrade --install: %w", err)
	}
	return nil
}

// Uninstall removes the Helm release. Safe to call on a cluster being
// torn down; failures are returned but the calling test should usually
// log+continue (the terraform destroy will reap everything anyway).
func (k *KarpenterInstaller) Uninstall(ctx context.Context) error {
	if err := k.locateHelm(); err != nil {
		return err
	}
	if k.ReleaseName == "" {
		k.ReleaseName = "karpenter-scaleway"
	}
	if k.WaitTimeoutSeconds <= 0 {
		k.WaitTimeoutSeconds = defaultWaitSeconds
	}
	cmd := exec.CommandContext(ctx, k.helmBin,
		"uninstall", k.ReleaseName,
		"--namespace", k.Namespace,
		"--kubeconfig", k.KubeconfigPath,
		"--wait",
		"--timeout", strconv.Itoa(k.WaitTimeoutSeconds)+"s",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (k *KarpenterInstaller) sortedValueArgs() []string {
	keys := make([]string, 0, len(k.Values))
	for kk := range k.Values {
		keys = append(keys, kk)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, kk := range keys {
		out = append(out, fmt.Sprintf("%s=%s", kk, k.Values[kk]))
	}
	return out
}

// locateHelm resolves the helm binary on PATH on first call.
func (k *KarpenterInstaller) locateHelm() error {
	if k.helmBin != "" {
		return nil
	}
	p, err := exec.LookPath("helm")
	if err != nil {
		return errors.New("helm binary not on PATH (install helm >= 3.10)")
	}
	k.helmBin = p
	return nil
}

package harness

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestInstallKarpenter_BuildsCorrectHelmArgs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("posix shim only")
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, "argv.log")
	body := `#!/bin/sh
echo "$@" >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "helm"), []byte(body), 0o755); err != nil {
		t.Fatalf("write fake helm: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	kubeconfigPath := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfigPath, []byte("apiVersion: v1\nkind: Config"), 0o600); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}

	inst := &KarpenterInstaller{
		ChartDir:       filepath.Join(dir, "chart"), // does not need to exist for the shim
		KubeconfigPath: kubeconfigPath,
		Namespace:      "karpenter",
		Values: map[string]string{
			"scaleway.accessKey":      "SCWXXX",
			"scaleway.secretKey":      "00000000-0000-4000-8000-000000000000",
			"scaleway.organizationID": "org-uuid",
			"scaleway.clusterID":      "cluster-uuid",
			"scaleway.region":         "fr-par",
		},
		WaitTimeoutSeconds: 1, // unit test only — keep deterministic
	}
	if err := inst.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read argv.log: %v", err)
	}
	got := string(logBytes)
	for _, want := range []string{"upgrade", "--install", "karpenter-scaleway", "--set", "scaleway.accessKey=SCWXXX", "--burst-limit", "200", "--disable-openapi-validation"} {
		if !strings.Contains(got, want) {
			t.Errorf("expected helm argv to contain %q; got\n%s", want, got)
		}
	}
}

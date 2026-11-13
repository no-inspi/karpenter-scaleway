package harness

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeTerraformScript writes a shell script to dir that records its
// argv into <dir>/argv.log and emits the canned -json output the
// harness expects from `terraform output -json`.
func fakeTerraformScript(t *testing.T, dir string, exitCode int, jsonOutput string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake shim assumes POSIX shell")
	}
	path := filepath.Join(dir, "terraform")
	body := `#!/bin/sh
# Strip the leading -chdir=... flag so $1 is always the subcommand.
case "$1" in
  -chdir=*)
    shift
    ;;
esac
echo "$@" >> "` + filepath.Join(dir, "argv.log") + `"
case "$1" in
  output)
    cat <<'JSON'
` + jsonOutput + `
JSON
    ;;
esac
exit ` + fmt.Sprintf("%d", exitCode) + `
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake terraform: %v", err)
	}
	return path
}

func TestTerraformApply_HappyPath(t *testing.T) {
	dir := t.TempDir()
	output := `{
  "cluster_id": {"value": "11111111-2222-3333-4444-555555555555"},
  "kubeconfig":  {"value": "apiVersion: v1\nkind: Config\n", "sensitive": true},
  "region":      {"value": "fr-par"}
}`
	fakeTerraformScript(t, dir, 0, output)
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	tf := &Terraform{ModuleDir: dir, Vars: map[string]string{"run_id": "test"}}
	res, err := tf.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if res.Outputs["cluster_id"] != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("cluster_id mismatch: %q", res.Outputs["cluster_id"])
	}
	if res.Outputs["region"] != "fr-par" {
		t.Errorf("region: %q", res.Outputs["region"])
	}
	if got := res.Outputs["kubeconfig"]; got != "apiVersion: v1\nkind: Config\n" {
		t.Errorf("kubeconfig roundtrip: %q", got)
	}
	if !filepathExists(filepath.Join(dir, "argv.log")) {
		t.Error("expected argv.log to be created by fake terraform")
	}
}

func TestTerraformApply_RetriesOnce(t *testing.T) {
	dir := t.TempDir()
	// Stateful shim: strip -chdir= flag, then:
	//   init   -> always succeeds
	//   apply  -> first invocation fails, second succeeds
	//   output -> returns canned JSON
	body := `#!/bin/sh
case "$1" in
  -chdir=*)
    shift
    ;;
esac
case "$1" in
  init)
    exit 0
    ;;
  apply)
    counter="` + filepath.Join(dir, "counter") + `"
    if [ ! -f "$counter" ]; then echo 1 > "$counter"; exit 1; fi
    echo "$@" >> "` + filepath.Join(dir, "argv.log") + `"
    exit 0
    ;;
  output)
    echo '{"cluster_id":{"value":"ok"},"region":{"value":"fr-par"},"kubeconfig":{"value":"","sensitive":true}}'
    exit 0
    ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "terraform"), []byte(body), 0o755); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))

	tf := &Terraform{ModuleDir: dir, ApplyTimeout: 10 * time.Second}
	res, err := tf.Apply(context.Background())
	if err != nil {
		t.Fatalf("Apply with retry: %v", err)
	}
	if res.Outputs["cluster_id"] != "ok" {
		t.Errorf("cluster_id: %q", res.Outputs["cluster_id"])
	}
	// argv.log is written only on the successful apply attempt.
	if !filepathExists(filepath.Join(dir, "argv.log")) {
		t.Error("expected argv.log to be written by successful apply attempt")
	}
}

func filepathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Terraform wraps a single Terraform module directory and executes the
// init / apply / destroy / output cycle. It is the only surface scenario
// authors should touch — they should never shell out to terraform
// directly.
type Terraform struct {
	// ModuleDir is the absolute or relative path to the .tf module to
	// run against. The module is left in place; we run terraform with
	// `-chdir=<ModuleDir>` so the working directory of the test
	// process stays untouched.
	ModuleDir string
	// Vars maps Terraform variable names to their values. They are
	// passed via `-var key=value` (so values must be plain strings —
	// numbers, bools, JSON, etc. should be encoded as strings the
	// module declares as `string`).
	Vars map[string]string
	// ApplyTimeout caps a single apply attempt. Defaults to 25 minutes.
	ApplyTimeout time.Duration
	// Env is appended to the child process's env (e.g. SCW_ACCESS_KEY).
	Env []string

	// initialised lazily; not exported.
	binPath string
}

// ApplyResult carries the outputs the scenarios will read.
type ApplyResult struct {
	// Outputs is the flattened map of `terraform output -json` value
	// fields. Sensitive values are included unredacted — the caller is
	// responsible for not logging them.
	Outputs map[string]string
}

const (
	defaultApplyTimeout = 25 * time.Minute
)

// Apply runs `terraform init` once and then `terraform apply` up to
// twice on transient failures. It returns the parsed `terraform output`
// on success.
func (t *Terraform) Apply(ctx context.Context) (*ApplyResult, error) {
	if err := t.locateBinary(); err != nil {
		return nil, err
	}
	if t.ApplyTimeout <= 0 {
		t.ApplyTimeout = defaultApplyTimeout
	}
	if err := t.run(ctx, "init", "-input=false", "-no-color"); err != nil {
		return nil, fmt.Errorf("terraform init: %w", err)
	}
	args := append([]string{"apply", "-auto-approve", "-input=false", "-no-color"}, t.varArgs()...)
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		applyCtx, cancel := context.WithTimeout(ctx, t.ApplyTimeout)
		err := t.run(applyCtx, args...)
		cancel()
		if err == nil {
			lastErr = nil
			break
		}
		lastErr = err
	}
	if lastErr != nil {
		return nil, fmt.Errorf("terraform apply (after retry): %w", lastErr)
	}
	return t.readOutputs(ctx)
}

// Destroy runs `terraform destroy -auto-approve` and returns any
// error. Scenarios register it via t.Cleanup and decide whether to
// log or ignore the error so it does not mask the test's own failure.
func (t *Terraform) Destroy(ctx context.Context) error {
	if err := t.locateBinary(); err != nil {
		return err
	}
	args := append([]string{"destroy", "-auto-approve", "-input=false", "-no-color"}, t.varArgs()...)
	return t.run(ctx, args...)
}

func (t *Terraform) varArgs() []string {
	out := make([]string, 0, 2*len(t.Vars))
	for k, v := range t.Vars {
		out = append(out, "-var", fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

func (t *Terraform) run(ctx context.Context, args ...string) error {
	full := append([]string{"-chdir=" + t.ModuleDir}, args...)
	cmd := exec.CommandContext(ctx, t.binPath, full...)
	cmd.Env = append(os.Environ(), t.Env...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (t *Terraform) locateBinary() error {
	if t.binPath != "" {
		return nil
	}
	p, err := exec.LookPath("terraform")
	if err != nil {
		return errors.New("terraform binary not on PATH (install terraform >= 1.5)")
	}
	t.binPath = p
	return nil
}

func (t *Terraform) readOutputs(ctx context.Context) (*ApplyResult, error) {
	cmd := exec.CommandContext(ctx, t.binPath, "-chdir="+t.ModuleDir, "output", "-json", "-no-color")
	cmd.Env = append(os.Environ(), t.Env...)
	stdout, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("terraform output -json: %w", err)
	}
	var raw map[string]struct {
		Value any `json:"value"`
	}
	if err := json.Unmarshal(stdout, &raw); err != nil {
		return nil, fmt.Errorf("decode outputs: %w", err)
	}
	flat := make(map[string]string, len(raw))
	for k, v := range raw {
		switch s := v.Value.(type) {
		case string:
			flat[k] = s
		case nil:
			flat[k] = ""
		default:
			b, _ := json.Marshal(s)
			flat[k] = string(b)
		}
	}
	return &ApplyResult{Outputs: flat}, nil
}

// AbsModuleDir resolves a module path relative to the repo root. The
// scenario tests run from `test/e2e/scenarios`, so they need a way to
// reach `test/e2e/terraform/<backend>/`.
func AbsModuleDir(parts ...string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	// Walk up until we find go.mod (= repo root).
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found from %s", wd)
		}
		dir = parent
	}
	return filepath.Join(append([]string{dir}, parts...)...), nil
}

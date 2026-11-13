// Package harness contains helpers shared by the real-cluster scenarios
// in test/e2e/scenarios. Lives outside the e2ecloud build tag so it can
// be unit-tested by `go test ./...` (it does NOT touch Scaleway at unit
// test time — all real calls are gated by the harness consumers in the
// `e2ecloud`-tagged scenario files).
package harness

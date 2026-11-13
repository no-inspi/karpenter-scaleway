// Package metrics emits structured timestamps the measurement scripts can
// grep out of the controller log file. Format is intentionally rigid so
// hack/extract-timestamps.sh stays trivial.
//
// Phases :
//
//	A — Scaleway pure : T0 just before instance.CreateServer, T1 when the
//	    Node corresponding to the new Instance reaches Ready=True.
//	B — Provider total : T0 at the entry of CloudProvider.Create(), T1 when
//	    Karpenter sees the NodeClaim go Ready=True.
package metrics

import (
	"time"

	"github.com/go-logr/logr"
)

// Mark logs a single timestamped event for latency decomposition. Emits both
// a Unix epoch ("unix") and an RFC3339 form ("t") — the bash extractor uses
// "unix" because parsing portable epochs out of arbitrary date strings on
// macOS+Linux is needlessly painful, and "t" stays useful for human reading.
//
// Example log line :
//
//	INFO latency.mark phase=A point=T0 nodeclaim=nc-abc unix=1777551644 t=2026-04-30T12:34:56Z
func Mark(log logr.Logger, phase, point, nodeclaim string) {
	now := time.Now().UTC()
	log.Info("latency.mark",
		"phase", phase,
		"point", point,
		"nodeclaim", nodeclaim,
		"unix", now.Unix(),
		"t", now.Format(time.RFC3339),
	)
}

package scaleway

import (
	"errors"
	"net"
	"net/http"

	"github.com/scaleway/scaleway-sdk-go/scw"
)

// IsRetryable reports whether err is a transient Scaleway failure that a
// caller may retry: the resource is in a transient state (e.g. a pool still
// "scaling"), the API rate-limited us (429), the API failed server-side
// (5xx), or the request hit a network error. Anything else (4xx validation,
// auth, not found, …) is permanent and must not be retried.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var transient *scw.TransientStateError
	if errors.As(err, &transient) {
		return true
	}
	var rerr *scw.ResponseError
	if errors.As(err, &rerr) {
		return rerr.StatusCode == http.StatusTooManyRequests || rerr.StatusCode >= http.StatusInternalServerError
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

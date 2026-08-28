package provider

import (
	"errors"
	"net/http"
	"strings"
)

type httpStatusCoder interface {
	HTTPStatusCode() int
}

type statusCoder interface {
	StatusCode() int
}

type statuser interface {
	Status() int
}

// isNotFound reports whether err is a Resend not-found API error. resend-go/v4
// currently returns opaque errors for non-2xx API responses, so prefer typed
// status access if present and retain a narrow message fallback for the current
// SDK.
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var httpStatus httpStatusCoder
	if errors.As(err, &httpStatus) && httpStatus.HTTPStatusCode() == http.StatusNotFound {
		return true
	}
	var statusCode statusCoder
	if errors.As(err, &statusCode) && statusCode.StatusCode() == http.StatusNotFound {
		return true
	}
	var status statuser
	if errors.As(err, &status) && status.Status() == http.StatusNotFound {
		return true
	}
	msg := strings.TrimSpace(strings.TrimPrefix(err.Error(), "[ERROR]:"))
	return strings.EqualFold(msg, "not found") || strings.HasSuffix(strings.ToLower(msg), " not found")
}

func stringSliceAs[T ~string, U ~string](in []T) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = U(v)
	}
	return out
}

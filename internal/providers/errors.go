package providers

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
)

type UpstreamError struct {
	Provider   string
	StatusCode int
	Message    string
}

func (e *UpstreamError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.StatusCode > 0 {
		return fmt.Sprintf("%s status %d: %s", e.Provider, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("%s upstream error: %s", e.Provider, e.Message)
}

func (e *UpstreamError) Retryable() bool {
	if e == nil {
		return false
	}
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	var upstream *UpstreamError
	if errors.As(err, &upstream) {
		return upstream.Retryable()
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	return false
}

package cluster

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/jwilder/k3a/pkg/retry"
)

func TestClassifyAzurePostgresCreateError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		retriable bool
	}{
		{name: "nil"},
		{name: "unknown", err: errors.New("unexpected failure")},
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{
			name: "async DNS failure with successful polling response",
			err: fmt.Errorf("failed to create postgres server: %w", &azcore.ResponseError{
				ErrorCode: "PrivateDnsZoneNameNotValid", StatusCode: http.StatusOK,
			}),
			retriable: true,
		},
		{
			name: "DNS failure on create",
			err: &azcore.ResponseError{
				ErrorCode: "PrivateDnsZoneNameNotValid", StatusCode: http.StatusBadRequest,
			},
			retriable: true,
		},
		{name: "request timeout", err: &azcore.ResponseError{StatusCode: http.StatusRequestTimeout}, retriable: true},
		{name: "conflict", err: &azcore.ResponseError{StatusCode: http.StatusConflict}, retriable: true},
		{name: "throttled", err: &azcore.ResponseError{StatusCode: http.StatusTooManyRequests}, retriable: true},
		{name: "server error", err: &azcore.ResponseError{StatusCode: http.StatusInternalServerError}, retriable: true},
		{name: "unavailable", err: &azcore.ResponseError{StatusCode: http.StatusServiceUnavailable}, retriable: true},
		{name: "invalid parameters", err: &azcore.ResponseError{StatusCode: http.StatusBadRequest}},
		{name: "unauthorized", err: &azcore.ResponseError{StatusCode: http.StatusUnauthorized}},
		{name: "forbidden", err: &azcore.ResponseError{StatusCode: http.StatusForbidden}},
		{name: "not found", err: &azcore.ResponseError{StatusCode: http.StatusNotFound}},
		{name: "other async failure", err: &azcore.ResponseError{ErrorCode: "InvalidParameterValue", StatusCode: http.StatusOK}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyAzurePostgresCreateError(test.err); got != test.retriable {
				t.Fatalf("classifyAzurePostgresCreateError() = %v, want %v", got, test.retriable)
			}
		})
	}
}

func TestPostgresCreateRetry(t *testing.T) {
	dnsErr := fmt.Errorf("failed to create postgres server: %w", &azcore.ResponseError{
		ErrorCode: "PrivateDnsZoneNameNotValid", StatusCode: http.StatusOK,
	})
	invalidErr := fmt.Errorf("failed to begin postgres server creation: %w", &azcore.ResponseError{
		ErrorCode: "InvalidParameterValue", StatusCode: http.StatusBadRequest,
	})
	tests := []struct {
		name         string
		failures     []error
		wantAttempts int
		wantErr      error
	}{
		{name: "initial success", wantAttempts: 1},
		{name: "retry async failure", failures: []error{dnsErr}, wantAttempts: 2},
		{name: "exhausted", failures: []error{dnsErr, dnsErr, dnsErr, dnsErr}, wantAttempts: 4, wantErr: dnsErr},
		{name: "permanent failure", failures: []error{invalidErr}, wantAttempts: 1, wantErr: invalidErr},
		{name: "permanent failure after retry", failures: []error{dnsErr, invalidErr}, wantAttempts: 2, wantErr: invalidErr},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			err := retry.RetryWithDelays(context.Background(), []time.Duration{0, 0, 0}, classifyAzurePostgresCreateError, func(context.Context) error {
				attempts++
				if attempts <= len(test.failures) {
					return test.failures[attempts-1]
				}
				return nil
			})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("retry error = %v, want %v", err, test.wantErr)
			}
			if attempts != test.wantAttempts {
				t.Fatalf("attempts = %d, want %d", attempts, test.wantAttempts)
			}
		})
	}
}

func TestPostgresCreateRetryCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	attempts := 0
	err := retry.RetryWithDelays(ctx, []time.Duration{time.Hour}, classifyAzurePostgresCreateError, func(context.Context) error {
		attempts++
		cancel()
		return &azcore.ResponseError{ErrorCode: "PrivateDnsZoneNameNotValid", StatusCode: http.StatusOK}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retry error = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

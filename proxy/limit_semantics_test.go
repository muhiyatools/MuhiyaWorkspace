package proxy

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// All three refusal causes used to reach the client as one 429 rate_limit_error.
// They need opposite client behavior: throughput clears in a minute and should
// be retried, a spent budget never clears on its own and must NOT be, and a
// suspended account needs an operator. A client that cannot tell them apart
// retries a permanent condition until the user gives up.

func TestLimitKindClassification(t *testing.T) {
	for _, row := range []struct {
		name string
		err  error
		want LimitKind
	}{
		{"throughput", &LimitError{Kind: LimitThroughput, Err: errors.New("RPM")}, LimitThroughput},
		{"budget", &LimitError{Kind: LimitBudget, Err: errors.New("budget limit of $0.50 exceeded")}, LimitBudget},
		{"suspended", &LimitError{Kind: LimitSuspended, Err: errors.New("user account is suspended")}, LimitSuspended},
		// An unclassified error defaults to throughput: the conservative choice,
		// because it tells the client to try again rather than to stop.
		{"unclassified defaults to retryable", errors.New("something else"), LimitThroughput},
		{"wrapped", fmt.Errorf("outer: %w", &LimitError{Kind: LimitBudget, Err: errors.New("x")}), LimitBudget},
	} {
		t.Run(row.name, func(t *testing.T) {
			if got := KindOf(row.err); got != row.want {
				t.Fatalf("KindOf = %v, want %v", got, row.want)
			}
		})
	}
}

func TestWriteLimitErrorShapes(t *testing.T) {
	h := &ProxyHandler{}
	for _, row := range []struct {
		name       string
		err        error
		wantStatus int
		wantType   string
		wantRetry  string
	}{
		{
			name:       "throughput advises a backoff",
			err:        &LimitError{Kind: LimitThroughput, Err: errors.New("requests per minute (RPM) limit of 30 exceeded")},
			wantStatus: http.StatusTooManyRequests, wantType: "rate_limit_error", wantRetry: "60",
		},
		{
			// Still 429 for OpenAI-client wire compatibility, but a distinct type
			// and NO Retry-After: a backoff is not what releases this.
			name:       "budget is quota, not throttling",
			err:        &LimitError{Kind: LimitBudget, Err: errors.New("budget limit of $0.50 exceeded for window '5h'")},
			wantStatus: http.StatusTooManyRequests, wantType: "insufficient_quota", wantRetry: "",
		},
		{
			name:       "suspension is not a rate limit at all",
			err:        &LimitError{Kind: LimitSuspended, Err: errors.New("user account is suspended")},
			wantStatus: http.StatusForbidden, wantType: "permission_error", wantRetry: "",
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			h.writeLimitError(recorder, row.err)
			if recorder.Code != row.wantStatus {
				t.Errorf("status = %d, want %d", recorder.Code, row.wantStatus)
			}
			if got := recorder.Header().Get("Retry-After"); got != row.wantRetry {
				t.Errorf("Retry-After = %q, want %q", got, row.wantRetry)
			}
			if body := recorder.Body.String(); !strings.Contains(body, row.wantType) {
				t.Errorf("body does not carry error type %q: %s", row.wantType, body)
			}
		})
	}
}

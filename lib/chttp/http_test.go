package chttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeTransport returns each queued outcome in order, counting attempts.
type fakeTransport struct {
	attempts int
	statuses []int // 0 means return an error instead of a response
}

func (t *fakeTransport) RoundTrip(_ *http.Request) (*http.Response, error) {
	i := min(t.attempts, len(t.statuses)-1)
	t.attempts++
	if t.statuses[i] == 0 {
		return nil, errors.New("connection reset")
	}
	return &http.Response{StatusCode: t.statuses[i], Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestRetryTransportSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		method       string
		markSafe     bool
		statuses     []int
		wantAttempts int
		wantErr      bool
		wantStatus   int
	}{
		// mutations must not be re-sent when their fate is unknown
		{"post 502 not retried", http.MethodPost, false, []int{502}, 1, false, 502},
		{"patch transport error not retried", http.MethodPatch, false, []int{0}, 1, true, 0},
		// 429 was rejected before it was acted on, so mutations may retry it
		{"post 429 retried", http.MethodPost, false, []int{429, 201}, 2, false, 201},
		// reads ride through everything
		{"get 502 retried", http.MethodGet, false, []int{502, 200}, 2, false, 200},
		{"get transport error retried", http.MethodGet, false, []int{0, 200}, 2, false, 200},
		{"marked post 502 retried", http.MethodPost, true, []int{502, 200}, 2, false, 200},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ft := &fakeTransport{statuses: tc.statuses}
			rt := NewRetryTransport("test", ft, 3)
			req, err := http.NewRequestWithContext(context.Background(), tc.method, "https://example.test/", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			if tc.markSafe {
				req = MarkRetrySafe(req)
			}
			resp, err := rt.RoundTrip(req)
			if resp != nil {
				defer func() { _ = resp.Body.Close() }()
			}
			if ft.attempts != tc.wantAttempts {
				t.Errorf("attempts = %d, want %d", ft.attempts, tc.wantAttempts)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
		})
	}
}

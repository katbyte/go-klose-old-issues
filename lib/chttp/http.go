// Package chttp provides a shared HTTP client with request/response debug logging.
package chttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/katbyte/koi/lib/clog"
	"github.com/sirupsen/logrus"
)

type ctxKey int

const retrySafeKey ctxKey = 0

// MarkRetrySafe declares a request safe to re-send even though its method
// isn't idempotent — a GraphQL query is a read that happens to travel as a
// POST. Reads with idempotent methods (GET/HEAD/OPTIONS) need no mark.
func MarkRetrySafe(req *http.Request) *http.Request {
	return req.WithContext(context.WithValue(req.Context(), retrySafeKey, true))
}

// retrySafe reports whether a request may be re-sent when its outcome is
// unknown (a transport error or a 5xx): true for idempotent methods and
// marked reads. A mutation that gets no response may still have been applied
// server-side, so re-sending it risks doing the work twice.
func retrySafe(req *http.Request) bool {
	if v, _ := req.Context().Value(retrySafeKey).(bool); v {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return false
}

// NewBaseTransport returns http.DefaultTransport tuned with per-attempt timeouts so a
// stalled connection or unresponsive server fails fast and gets retried by the
// RetryTransport instead of hanging the command.
func NewBaseTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport // unreachable, but degrade gracefully
	}

	c := t.Clone()
	c.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	c.TLSHandshakeTimeout = 10 * time.Second
	c.ResponseHeaderTimeout = 30 * time.Second
	return c
}

func NewHTTPClient(name string) *http.Client {
	return &http.Client{
		Transport: NewRetryTransport(name, NewTransport(name, NewBaseTransport()), 3),
	}
}

type Transport struct {
	name      string
	transport http.RoundTripper
}

func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if clog.Log.IsLevelEnabled(logrus.TraceLevel) {
		reqData, err := httputil.DumpRequestOut(req, true)
		if err == nil {
			clog.Log.Tracef(logReqMsg, t.name, prettyPrintJSON(reqData))
		} else {
			clog.Log.Debugf("%s API Request error: %#v", t.name, err)
		}
	}

	resp, err := t.transport.RoundTrip(req)
	if err != nil {
		return resp, err
	}

	if clog.Log.IsLevelEnabled(logrus.TraceLevel) {
		respData, err := httputil.DumpResponse(resp, true)
		if err == nil {
			clog.Log.Tracef(logRespMsg, t.name, prettyPrintJSON(respData))
		} else {
			clog.Log.Debugf("%s API Response error: %#v", t.name, err)
		}
	}

	return resp, nil
}

func NewTransport(name string, t http.RoundTripper) *Transport {
	return &Transport{name, t}
}

// RetryTransport wraps an http.RoundTripper with retry logic for transient
// failures: 429 (rate limited) for every request, plus connection errors and
// 5xx (server error) responses for retry-safe requests only (see retrySafe).
type RetryTransport struct {
	name      string
	transport http.RoundTripper
	maxRetry  int
}

func NewRetryTransport(name string, t http.RoundTripper, maxRetry int) *RetryTransport {
	return &RetryTransport{name: name, transport: t, maxRetry: maxRetry}
}

func (t *RetryTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var resp *http.Response
	var err error

	// a request body is consumed by each attempt, so it must be rewound via GetBody
	// before a retry; requests with a body but no GetBody cannot be retried safely.
	// http.NoBody counts as bodyless — NewRequest leaves GetBody nil for it
	rewind := func() bool {
		if req.Body == nil || req.Body == http.NoBody {
			return true
		}
		if req.GetBody == nil {
			return false
		}
		body, gbErr := req.GetBody()
		if gbErr != nil {
			return false
		}
		req.Body = body
		return true
	}

	safe := retrySafe(req)
	for attempt := range t.maxRetry {
		resp, err = t.transport.RoundTrip(req)
		if err != nil {
			// a transport error can land after the server committed the write
			// (the response just never made it back), so only retry-safe
			// requests go again — re-posting a comment would duplicate it
			if attempt < t.maxRetry-1 && safe && rewind() {
				wait := time.Duration(1<<attempt) * time.Second
				clog.Log.Debugf("%s request failed (attempt %d/%d), retrying in %s: %v", t.name, attempt+1, t.maxRetry, wait, err)
				time.Sleep(wait)
				continue
			}
			return nil, err
		}

		// 429 (rate limited) was rejected before it was acted on, so every
		// request may retry it; a 5xx leaves a mutation's fate unknown, so
		// only retry-safe requests ride through those
		if resp.StatusCode == http.StatusTooManyRequests || (safe && resp.StatusCode >= 500) {
			if attempt < t.maxRetry-1 && rewind() {
				wait := time.Duration(1<<attempt) * time.Second
				clog.Log.Debugf("%s got status %d (attempt %d/%d), retrying in %s", t.name, resp.StatusCode, attempt+1, t.maxRetry, wait)
				_ = resp.Body.Close()
				time.Sleep(wait)
				continue
			}
		}

		return resp, nil
	}

	return resp, err
}

// prettyPrintJSON iterates through a []byte line-by-line,
// transforming any lines that are complete json into pretty-printed json.
func prettyPrintJSON(b []byte) string {
	parts := strings.Split(string(b), "\n")
	for i, p := range parts {
		if b := []byte(p); json.Valid(b) {
			var out bytes.Buffer
			//nolint:errcheck,gosec // error is intentionally ignored for pretty printing
			json.Indent(&out, b, "", " ")
			parts[i] = out.String()
		}
	}

	return strings.Join(parts, "\n")
}

const logReqMsg = `%s API Request Details:
---[ REQUEST ]---------------------------------------
%s
-----------------------------------------------------`

const logRespMsg = `%s API Response Details:
---[ RESPONSE ]--------------------------------------
%s
-----------------------------------------------------`

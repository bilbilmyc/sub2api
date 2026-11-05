package repository

import (
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/google/uuid"
)

func (s *httpUpstreamService) withOutboundEvidence(client *http.Client, proxyURL string, accountID int64) *http.Client {
	if s.cfg == nil || !s.cfg.Gateway.OutboundEvidenceEnabled || accountID <= 0 {
		return client
	}
	ids := s.cfg.Gateway.OutboundEvidenceAccountIDs
	if len(ids) > 0 {
		allowed := false
		for _, id := range ids {
			if id == accountID {
				allowed = true
				break
			}
		}
		if !allowed {
			return client
		}
	}
	return httpClientWithOutboundEvidence(client, proxyURL, accountID)
}

// The wrapper belongs inside the Grok fallback transport: each fallback and
// redirect then produces its own event. Never mutate the shared cached client.
func httpClientWithOutboundEvidence(client *http.Client, proxyURL string, accountID int64) *http.Client {
	if client == nil {
		return nil
	}
	clone := *client
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = &outboundEvidenceTransport{base: base, proxyURL: proxyURL, accountID: accountID}
	return &clone
}

type outboundEvidenceTransport struct {
	base      http.RoundTripper
	proxyURL  string
	accountID int64
}

func (t *outboundEvidenceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := t.base.RoundTrip(req)
	if req == nil || t.accountID <= 0 {
		return resp, err
	}
	requestID, _ := req.Context().Value(ctxkey.RequestID).(string)
	clientRequestID, _ := req.Context().Value(ctxkey.ClientRequestID).(string)
	// Background probes without gateway correlation are outside this event scope.
	if requestID == "" && clientRequestID == "" {
		return resp, err
	}
	// Write directly to the existing bounded, nonblocking Ops queue. Do not use
	// logger.Info: console/file IO and log sampling must not affect forwarding.
	// The whitelist also avoids inheriting request loggers' arbitrary fields.
	logger.WriteSinkEvent("info", "route.outbound", "outbound.http_attempt", outboundEvidenceFields(req, resp, err, t.proxyURL, t.accountID, uuid.NewString(), time.Since(start)))
	return resp, err
}

// Preserve the optional client cleanup hook on the wrapped transport.
func (t *outboundEvidenceTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func outboundEvidenceFields(req *http.Request, resp *http.Response, transportErr error, proxyURL string, accountID int64, attemptID string, elapsed time.Duration) map[string]any {
	requestID, _ := req.Context().Value(ctxkey.RequestID).(string)
	clientRequestID, _ := req.Context().Value(ctxkey.ClientRequestID).(string)
	upstreamHost := ""
	if req.URL != nil {
		upstreamHost = req.URL.Host
	}
	effectiveHost := req.Host
	if effectiveHost == "" {
		effectiveHost = upstreamHost
	}
	proxyHost, proxyScheme := "", ""
	// Use the same normalization as client creation (including socks5 -> socks5h).
	if _, parsed, err := normalizeProxyURL(proxyURL); err == nil && parsed != nil {
		proxyHost, proxyScheme = parsed.Host, parsed.Scheme
	}
	statusCode, upstreamRequestID := 0, ""
	if resp != nil {
		statusCode = resp.StatusCode
		upstreamRequestID = strings.TrimSpace(resp.Header.Get("x-request-id"))
	}
	idTruncated := len(upstreamRequestID) > 128
	if idTruncated {
		upstreamRequestID = upstreamRequestID[:128]
		for !utf8.ValidString(upstreamRequestID) {
			upstreamRequestID = upstreamRequestID[:len(upstreamRequestID)-1]
		}
	}
	return map[string]any{
		"component": "route.outbound", "event": "outbound.http_attempt", "schema_version": int64(1),
		"request_id": strings.TrimSpace(requestID), "client_request_id": strings.TrimSpace(clientRequestID),
		"account_id": accountID, "attempt_id": attemptID,
		"upstream_url": sanitizedOutboundEvidenceURL(req.URL), "upstream_host": upstreamHost,
		"request_host": req.Host, "effective_host": effectiveHost,
		"proxy_host": proxyHost, "proxy_scheme": proxyScheme,
		"status_code": int64(statusCode), "response_received": resp != nil,
		"upstream_request_id_header": "x-request-id", "upstream_request_id": upstreamRequestID,
		"upstream_request_id_truncated": idTruncated, "transport_error": transportErr != nil,
		"headers_elapsed_ms": elapsed.Milliseconds(),
	}
}

func sanitizedOutboundEvidenceURL(src *url.URL) string {
	if src == nil || src.Opaque != "" || src.Host == "" ||
		(!strings.EqualFold(src.Scheme, "http") && !strings.EqualFold(src.Scheme, "https")) {
		return ""
	}
	clean := *src
	clean.User = nil
	clean.RawQuery = ""
	clean.ForceQuery = false
	clean.Fragment = ""
	clean.RawFragment = ""
	value := clean.String()
	if len(value) > 4096 {
		return ""
	}
	return value
}

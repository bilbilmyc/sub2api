package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

func TestOutboundEvidenceRedactionAndCorrelation(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "local-request")
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-request")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://user:secret@provider.example/v1/responses?api_key=secret#secret", strings.NewReader("secret-body"))
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "override.example"
	req.Header.Set("Authorization", "Bearer secret-auth")
	req.Header.Set("Cookie", "secret-cookie")
	resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {"provider-request"}}}
	got := outboundEvidenceFields(req, resp, nil, "http://proxy-user:secret@proxy.example:8080?secret", 23, "attempt-1", 42*time.Millisecond)
	for key, want := range map[string]any{
		"component": "route.outbound", "event": "outbound.http_attempt", "schema_version": int64(1),
		"request_id": "local-request", "client_request_id": "client-request", "account_id": int64(23),
		"upstream_url": "https://provider.example/v1/responses", "upstream_host": "provider.example",
		"request_host": "override.example", "effective_host": "override.example", "proxy_host": "proxy.example:8080",
		"proxy_scheme": "http", "status_code": int64(200), "response_received": true,
		"upstream_request_id_header": "x-request-id", "upstream_request_id": "provider-request",
		"upstream_request_id_truncated": false, "transport_error": false, "headers_elapsed_ms": int64(42),
	} {
		if got[key] != want {
			t.Errorf("%s=%v, want %v", key, got[key], want)
		}
	}
	encoded, _ := json.Marshal(got)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("sensitive field leaked: %s", encoded)
	}
	if req.URL.User == nil || req.URL.RawQuery == "" || req.URL.Fragment == "" || req.Header.Get("Authorization") == "" {
		t.Fatal("request mutated by observation")
	}
	if resp.Header.Get("x-request-id") != "provider-request" {
		t.Fatal("response headers mutated")
	}
}

func TestOutboundEvidenceMissingResponseAndTruncatedID(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://provider.example/v1/responses", nil)
	for _, tc := range []struct {
		name                string
		response            *http.Response
		err                 error
		status              int64
		received, truncated bool
	}{
		{"transport failure", nil, errors.New("sensitive transport text"), 0, false, false},
		{"no selected header", &http.Response{StatusCode: 200}, nil, 200, true, false},
		{"long selected header", &http.Response{StatusCode: 200, Header: http.Header{"X-Request-Id": {strings.Repeat("界", 50)}}}, nil, 200, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := outboundEvidenceFields(req, tc.response, tc.err, "", 1, "attempt", time.Millisecond)
			if fields["status_code"] != tc.status || fields["response_received"] != tc.received || fields["upstream_request_id_truncated"] != tc.truncated {
				t.Fatalf("wrong response metadata: %#v", fields)
			}
			id := fields["upstream_request_id"].(string)
			if len(id) > 128 {
				t.Fatal("ID exceeds storage bound")
			}
			encoded, _ := json.Marshal(fields)
			if strings.Contains(string(encoded), "sensitive") {
				t.Fatal("transport error text leaked")
			}
		})
	}
	if sanitizedOutboundEvidenceURL(&url.URL{Scheme: "https", Opaque: "secret"}) != "" {
		t.Fatal("opaque URL must not be logged")
	}
}

func TestOutboundEvidenceProxyNormalizationMatchesTransport(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "https://provider.example/v1/responses", nil)
	for _, tc := range []struct {
		name, raw, host, scheme string
	}{
		{"direct", " ", "", ""},
		{"trimmed and default port", " HTTP://user:secret@PROXY.example:80 ", "proxy.example", "http"},
		{"remote DNS", "socks5://user:secret@proxy.example:1080", "proxy.example:1080", "socks5h"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fields := outboundEvidenceFields(req, nil, nil, tc.raw, 1, "attempt", time.Millisecond)
			if fields["proxy_host"] != tc.host || fields["proxy_scheme"] != tc.scheme {
				t.Fatalf("proxy metadata differs from transport: %#v", fields)
			}
		})
	}
}

type outboundEvidenceTestSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (s *outboundEvidenceTestSink) WriteLogEvent(event *logger.LogEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func TestOutboundEvidenceRedirectsAndSharedClient(t *testing.T) {
	if err := logger.Init(logger.InitOptions{Level: "info"}); err != nil {
		t.Fatal(err)
	}
	sink := &outboundEvidenceTestSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/first" {
			w.Header().Set("X-Request-ID", "redirect-id")
			http.Redirect(w, r, "/last?secret=query", http.StatusTemporaryRedirect)
			return
		}
		w.Header().Set("X-Request-ID", "final-id")
		_, _ = w.Write([]byte("secret-response-body"))
	}))
	defer server.Close()
	base := server.Client()
	originalTransport := base.Transport
	client := httpClientWithOutboundEvidence(base, "", 6)
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "correlation")
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/first?secret=query", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if base.Transport != originalTransport {
		t.Fatal("shared client transport was changed")
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "secret-response-body" {
		t.Fatal("observer consumed response body")
	}
	if len(sink.events) != 2 {
		t.Fatalf("redirect attempts=%d, want 2", len(sink.events))
	}
	if sink.events[0].Fields["attempt_id"] == sink.events[1].Fields["attempt_id"] {
		t.Fatal("attempt identities reused")
	}
	if sink.events[0].Fields["upstream_request_id"] != "redirect-id" || sink.events[1].Fields["upstream_request_id"] != "final-id" {
		t.Fatal("response IDs not observed per hop")
	}
	encoded, _ := json.Marshal(sink.events)
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("sensitive metadata leaked: %s", encoded)
	}
}

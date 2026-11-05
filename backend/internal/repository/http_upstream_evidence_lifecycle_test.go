package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

func evidenceRequest(t *testing.T, ctx context.Context, target string) *http.Request {
	t.Helper()
	ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "client-case-sensitive")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	require.NoError(t, err)
	return req
}

func installEvidenceSink(t *testing.T) *outboundEvidenceTestSink {
	t.Helper()
	sink := &outboundEvidenceTestSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	return sink
}

func TestOutboundEvidenceOptInAndAccountScope(t *testing.T) {
	base := &http.Client{}
	s := &httpUpstreamService{cfg: &config.Config{}}
	require.Same(t, base, s.withOutboundEvidence(base, "", 6))
	s.cfg.Gateway.OutboundEvidenceEnabled = true
	s.cfg.Gateway.OutboundEvidenceAccountIDs = []int64{6}
	require.Same(t, base, s.withOutboundEvidence(base, "", 29))
	require.NotSame(t, base, s.withOutboundEvidence(base, "", 6))
	s.cfg.Gateway.OutboundEvidenceAccountIDs = nil
	require.NotSame(t, base, s.withOutboundEvidence(base, "", 29))
	require.Same(t, base, s.withOutboundEvidence(base, "", 0))
}

func TestOutboundEvidenceStreamingAndCancellation(t *testing.T) {
	sink := installEvidenceSink(t)
	closed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "stream-provider-id")
		_, _ = io.WriteString(w, "data: first\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(closed)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	client := httpClientWithOutboundEvidence(server.Client(), "", 6)
	resp, err := client.Do(evidenceRequest(t, ctx, server.URL))
	require.NoError(t, err)
	require.Len(t, sink.events, 1, "event must be available before the stream ends")
	buf := make([]byte, len("data: first\n\n"))
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err)
	require.Equal(t, "data: first\n\n", string(buf))
	cancel()
	_, err = resp.Body.Read(make([]byte, 1))
	require.Error(t, err)
	_ = resp.Body.Close()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	// Receiving HTTP 200 is intentionally not recorded as a completed stream.
	require.Equal(t, int64(200), sink.events[0].Fields["status_code"])
	require.NotContains(t, sink.events[0].Fields, "success")
}

func TestOutboundEvidenceHeaderTimeoutAndMissingCorrelation(t *testing.T) {
	sink := installEvidenceSink(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	client := httpClientWithOutboundEvidence(server.Client(), "", 6)
	client.Timeout = 60 * time.Millisecond
	_, err := client.Do(evidenceRequest(t, context.Background(), server.URL))
	require.Error(t, err)
	require.Len(t, sink.events, 1)
	require.Equal(t, true, sink.events[0].Fields["transport_error"])
	require.Equal(t, false, sink.events[0].Fields["response_received"])
	_, err = client.Get(server.URL)
	require.Error(t, err)
	require.Len(t, sink.events, 1, "background requests must not enter the route feed")
}

func TestOutboundEvidenceConcurrentAccountsReuseSharedTransport(t *testing.T) {
	sink := installEvidenceSink(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", r.URL.Path)
		_, _ = io.WriteString(w, "unchanged")
	}))
	defer server.Close()
	base := server.Client()
	original := base.Transport
	var wg sync.WaitGroup
	for id := int64(1); id <= 32; id++ {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			client := httpClientWithOutboundEvidence(base, "http://private:password@proxy.example:8080", id)
			resp, err := client.Do(evidenceRequest(t, context.Background(), fmt.Sprintf("%s/%d", server.URL, id)))
			if err != nil {
				t.Error(err)
				return
			}
			body, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if err != nil || string(body) != "unchanged" {
				t.Error("body changed or unreadable")
			}
		}(id)
	}
	wg.Wait()
	require.Same(t, original, base.Transport)
	require.Len(t, sink.events, 32)
	seen := map[any]bool{}
	for _, event := range sink.events {
		id := event.Fields["account_id"].(int64)
		require.Equal(t, fmt.Sprintf("/%d", id), event.Fields["upstream_request_id"])
		require.Equal(t, "proxy.example:8080", event.Fields["proxy_host"])
		require.False(t, seen[event.Fields["attempt_id"]])
		seen[event.Fields["attempt_id"]] = true
	}
}

func TestOutboundEvidenceBypassesLogLevelAndSampling(t *testing.T) {
	sink := installEvidenceSink(t)
	// Evidence bypasses global level/sampling and file/stdout IO entirely.
	require.NoError(t, logger.Init(logger.InitOptions{Level: "error", Sampling: logger.SamplingOptions{Enabled: true, Initial: 1, Thereafter: 1000}}))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer server.Close()
	client := httpClientWithOutboundEvidence(server.Client(), "", 6)
	for range 5 {
		resp, err := client.Do(evidenceRequest(t, context.Background(), server.URL))
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	require.Len(t, sink.events, 5)
	t.Cleanup(func() { _ = logger.Init(logger.InitOptions{Level: "info"}) })
}

func TestOutboundEvidenceHTTP2AndProxy(t *testing.T) {
	t.Run("http2 response remains http2", func(t *testing.T) {
		sink := installEvidenceSink(t)
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ProtoMajor != 2 {
				t.Errorf("request protocol changed: %s", r.Proto)
			}
			_, _ = io.WriteString(w, "h2-body")
		}))
		server.EnableHTTP2 = true
		server.StartTLS()
		defer server.Close()
		client := httpClientWithOutboundEvidence(server.Client(), "", 6)
		resp, err := client.Do(evidenceRequest(t, context.Background(), server.URL))
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, "h2-body", string(body))
		require.Equal(t, 2, resp.ProtoMajor)
		require.Len(t, sink.events, 1)
	})
	t.Run("actual proxy and target are distinct", func(t *testing.T) {
		sink := installEvidenceSink(t)
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "proxied-body") }))
		defer target.Close()
		forward := &http.Transport{}
		defer forward.CloseIdleConnections()
		proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Proxy-Authorization") == "" {
				t.Error("proxy credentials not sent")
			}
			copy := r.Clone(r.Context())
			copy.RequestURI = ""
			copy.Header.Del("Proxy-Authorization")
			resp, err := forward.RoundTrip(copy)
			if err != nil {
				t.Error(err)
				w.WriteHeader(502)
				return
			}
			defer resp.Body.Close()
			_, _ = io.Copy(w, resp.Body)
		}))
		defer proxy.Close()
		proxyURL, err := url.Parse(proxy.URL)
		require.NoError(t, err)
		proxyURL.User = url.UserPassword("private-user", "private-password")
		transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
		defer transport.CloseIdleConnections()
		client := httpClientWithOutboundEvidence(&http.Client{Transport: transport}, proxyURL.String(), 6)
		resp, err := client.Do(evidenceRequest(t, context.Background(), target.URL+"/responses?private-key=value"))
		require.NoError(t, err)
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		require.NoError(t, err)
		require.Equal(t, "proxied-body", string(body))
		require.Len(t, sink.events, 1)
		require.Equal(t, target.URL+"/responses", sink.events[0].Fields["upstream_url"])
		require.Equal(t, proxyURL.Host, sink.events[0].Fields["proxy_host"])
	})
}

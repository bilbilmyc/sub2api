//go:build unit

package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type outboundEvidenceOAuthUpstream struct {
	*httpUpstreamRecorder
	accountID int64
}

func (u *outboundEvidenceOAuthUpstream) Do(req *http.Request, proxyURL string, accountID int64, concurrency int) (*http.Response, error) {
	u.accountID = accountID
	return u.httpUpstreamRecorder.Do(req, proxyURL, accountID, concurrency)
}

func (u *outboundEvidenceOAuthUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

// Exercise the OAuth service boundary without making an authenticated upstream
// call. Both forwarding modes must preserve the context consumed by the observer.
func TestOutboundEvidenceOAuthForwardingCorrelation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprintf("passthrough=%t", passthrough), func(t *testing.T) {
			sse := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_synthetic\",\"model\":\"gpt-5\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\ndata: [DONE]\n\n"
			upstream := &outboundEvidenceOAuthUpstream{httpUpstreamRecorder: &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"text/event-stream"}, "X-Request-Id": {"synthetic-oauth-provider"}},
				Body:       io.NopCloser(strings.NewReader(sse)),
			}}}
			svc := newOpenAIImageGenerationControlTestService(upstream.httpUpstreamRecorder)
			svc.httpUpstream = upstream
			c, _ := newOpenAIImageGenerationControlTestContext(true, "codex_cli_rs/0.144.1")
			ctx := context.WithValue(c.Request.Context(), ctxkey.RequestID, "synthetic-local-id")
			ctx = context.WithValue(ctx, ctxkey.ClientRequestID, "synthetic-client-id")
			c.Request = c.Request.WithContext(ctx)
			account := newOpenAIImageGenerationControlTestAccount()
			account.Type = AccountTypeOAuth
			account.Credentials = map[string]any{"access_token": "synthetic-oauth-token", "chatgpt_account_id": "synthetic-chatgpt-account"}
			account.Extra = map[string]any{"openai_passthrough": passthrough}
			result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5","stream":true,"instructions":"synthetic probe","input":"hello"}`))
			require.NoError(t, err)
			require.NotNil(t, upstream.lastReq)
			require.Equal(t, account.ID, upstream.accountID)
			require.Equal(t, "https://chatgpt.com/backend-api/codex/responses", upstream.lastReq.URL.String())
			require.Equal(t, "chatgpt.com", upstream.lastReq.Host)
			require.Equal(t, "synthetic-local-id", upstream.lastReq.Context().Value(ctxkey.RequestID))
			require.Equal(t, "synthetic-client-id", upstream.lastReq.Context().Value(ctxkey.ClientRequestID))
			require.Equal(t, "synthetic-oauth-provider", result.RequestID)
		})
	}
}

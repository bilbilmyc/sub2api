package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOutboundEvidenceConfig(t *testing.T) {
	resetViperWithJWTSecret(t)
	cfg, err := Load()
	require.NoError(t, err)
	require.False(t, cfg.Gateway.OutboundEvidenceEnabled)
	require.Empty(t, cfg.Gateway.OutboundEvidenceAccountIDs)
	t.Setenv("GATEWAY_OUTBOUND_EVIDENCE_ENABLED", "true")
	t.Setenv("GATEWAY_OUTBOUND_EVIDENCE_ACCOUNT_IDS", "6,29")
	cfg, err = Load()
	require.NoError(t, err)
	require.True(t, cfg.Gateway.OutboundEvidenceEnabled)
	require.Equal(t, []int64{6, 29}, cfg.Gateway.OutboundEvidenceAccountIDs)
	t.Setenv("GATEWAY_OUTBOUND_EVIDENCE_ACCOUNT_IDS", "6,-1")
	_, err = Load()
	require.ErrorContains(t, err, "positive account IDs")
}

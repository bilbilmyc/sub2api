package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCustomBuildCannotOverwriteItselfFromOfficialUpdates(t *testing.T) {
	// Nil clients prove the guard runs before any fetch, cache or file operation.
	s := NewUpdateService(nil, nil, "0.2.19-route.1", "custom")
	info, err := s.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.False(t, info.HasUpdate)
	require.Equal(t, "custom", info.BuildType)
	require.NotEmpty(t, info.Warning)
	require.ErrorIs(t, s.PerformUpdate(context.Background()), ErrCustomBuildManaged)
	require.ErrorIs(t, s.Rollback(), ErrCustomBuildManaged)
	require.ErrorIs(t, s.RollbackToVersion(context.Background(), "0.2.18"), ErrCustomBuildManaged)
	versions, err := s.ListRollbackVersions(context.Background())
	require.NoError(t, err)
	require.Empty(t, versions)
}

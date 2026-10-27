package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMapUserErrorCategoryCyber(t *testing.T) {
	require.Equal(t, "safety_policy", MapUserErrorCategory("request", "cyber_policy"))
	phases, types := CategoryToFilter("cyber")
	require.Empty(t, phases)
	require.Contains(t, types, "cyber_policy")
	require.Contains(t, types, "bio_policy")
	newPhases, newTypes := CategoryToFilter("safety_policy")
	require.Equal(t, phases, newPhases)
	require.Equal(t, types, newTypes)
}

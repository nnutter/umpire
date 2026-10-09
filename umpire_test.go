package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, version)
}

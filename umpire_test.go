package umpire_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nnutter/umpire"
)

func TestVersion(t *testing.T) {
	t.Parallel()

	require.NotEmpty(t, umpire.Version)
}

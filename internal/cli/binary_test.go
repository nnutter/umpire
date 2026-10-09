package cli

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func buildUmpire(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "umpire")
	cmd := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../..")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", out)
	return binary
}

func executeBinary(t *testing.T, binary, dir string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

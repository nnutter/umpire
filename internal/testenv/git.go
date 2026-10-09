// Package testenv provides process isolation for Umpire's test suites.
// It is imported only by test files, never by the command or review library.
package testenv

import (
	"errors"
	"log"
	"os"
	"strings"
	"testing"
)

// Run clears inherited Git settings before tests start, including parallel tests.
// In-process review calls and child processes then share the isolated environment.
func Run(m *testing.M) {
	if err := isolateGit(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

func isolateGit() error {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "GIT_") {
			if err := os.Unsetenv(key); err != nil {
				return err
			}
		}
	}
	// Identity and other repository settings are supplied by each local fixture.
	return errors.Join(os.Setenv("GIT_CONFIG_GLOBAL", os.DevNull), os.Setenv("GIT_CONFIG_NOSYSTEM", "1"))
}

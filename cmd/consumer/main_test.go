package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// requiredEnvVars are the env vars that config.Load() requires.
// Clearing them forces run() to fail immediately at the config step.
var requiredEnvVars = []string{
	"NATS_URL", "NATS_TLS_CA", "NATS_TLS_CERT", "NATS_TLS_KEY",
	"DB_HOST", "DB_NAME", "DB_USER", "DB_PASSWORD_FILE",
}

// TestRunReturnsErrorOnMissingConfig verifies that run() fails fast and returns
// a wrapped error when required environment variables are absent.
func TestRunReturnsErrorOnMissingConfig(t *testing.T) {
	for _, key := range requiredEnvVars {
		t.Setenv(key, "")
	}

	err := run()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "load config")
}

// TestMainExitsOneOnError verifies that main() calls os.Exit(1) when run() fails.
// Uses the subprocess-exec pattern: the child sets BE_MAIN=1 and calls main(),
// which exits 1; the parent asserts the exit code.
func TestMainExitsOneOnError(t *testing.T) {
	if os.Getenv("BE_MAIN") == "1" {
		main()
		return
	}

	// Strip infrastructure vars so run() fails at config, not at NATS/DB connect.
	skip := make(map[string]bool, len(requiredEnvVars))
	for _, k := range requiredEnvVars {
		skip[k] = true
	}
	var filtered []string
	for _, e := range os.Environ() {
		if key := strings.SplitN(e, "=", 2)[0]; !skip[key] {
			filtered = append(filtered, e)
		}
	}

	cmd := exec.CommandContext(context.Background(), os.Args[0], "-test.run=TestMainExitsOneOnError")
	cmd.Env = append(filtered, "BE_MAIN=1")

	err := cmd.Run()
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "main() must exit non-zero when run() fails")
	assert.Equal(t, 1, exitErr.ExitCode())
}

package brokerexec

import (
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/errs"
)

func TestNewRequiresCommand(t *testing.T) {
	cases := []string{"", "   ", "\t"}
	for _, cmd := range cases {
		t.Run(cmd, func(t *testing.T) {
			_, err := New(Config{Command: cmd})
			require.ErrorIs(t, err, errs.ErrBadConfiguration)
		})
	}
}

func TestNewRejectsBadEnv(t *testing.T) {
	cases := []string{"=value", "NO_EQUALS", "="}
	for _, e := range cases {
		t.Run(e, func(t *testing.T) {
			_, err := New(Config{
				Command: "/bin/true",
				Env:     []string{e},
			})
			require.ErrorIs(t, err, errs.ErrBadConfiguration)
		})
	}
}

func TestNewDefaultsCallTimeout(t *testing.T) {
	r, err := New(Config{Command: "/bin/true"})
	require.NoError(t, err)
	require.Equal(t, 30*time.Second, r.callTimeout)
}

func TestNewKeepsExplicitCallTimeout(t *testing.T) {
	r, err := New(Config{
		Command:     "/bin/true",
		CallTimeout: 5 * time.Second,
	})
	require.NoError(t, err)
	require.Equal(t, 5*time.Second, r.callTimeout)
}

func TestNewDefaultsLogger(t *testing.T) {
	r, err := New(Config{Command: "/bin/true"})
	require.NoError(t, err)
	require.NotNil(t, r.logger, "logger must default to a non-nil discard logger")
}

func TestNewKeepsExplicitLogger(t *testing.T) {
	custom := slog.Default()
	r, err := New(Config{Command: "/bin/true", Logger: custom})
	require.NoError(t, err)
	require.Same(t, custom, r.logger)
}

func TestBuildEnvOverlay(t *testing.T) {
	r := require.New(t)
	t.Setenv("FB_BROKEREXEC_TEST_BASE", "base")
	t.Setenv("FB_BROKEREXEC_TEST_OVERLAY", "from_env")

	reg, err := New(Config{
		Command: "/bin/true",
		Env: []string{
			"FB_BROKEREXEC_TEST_OVERLAY=from_config",
			"FB_BROKEREXEC_TEST_NEW=created",
		},
	})
	r.NoError(err)

	env := reg.buildEnv()
	r.Contains(env, "FB_BROKEREXEC_TEST_BASE=base",
		"inherited base value preserved")
	r.Contains(env, "FB_BROKEREXEC_TEST_OVERLAY=from_config",
		"configured override wins over inherited")
	r.NotContains(env, "FB_BROKEREXEC_TEST_OVERLAY=from_env",
		"inherited overlay must be replaced, not duplicated")
	r.Contains(env, "FB_BROKEREXEC_TEST_NEW=created",
		"configured-only key is appended")
}

func TestBuildEnvNoOverridesReturnsBase(t *testing.T) {
	t.Setenv("FB_BROKEREXEC_TEST_NOENV", "keep")
	reg, err := New(Config{Command: "/bin/true"})
	require.NoError(t, err)
	env := reg.buildEnv()
	require.Contains(t, env, "FB_BROKEREXEC_TEST_NOENV=keep")
}

// TestNewLeavesRunCmdNil pins the contract that the runFunc seam is
// nil after New so that the production execOnce path (added in a
// follow-up change) can detect "use the real runner" and same-package
// tests can install fakes by direct field assignment.
func TestNewLeavesRunCmdNil(t *testing.T) {
	reg, err := New(Config{Command: "/bin/true"})
	require.NoError(t, err)
	require.Nil(t, reg.runCmd)
}

// TestBuildEnvAppendedKeysAreSorted verifies the load-bearing sorted-key
// iteration in buildEnv. Configured-only keys are appended to the base
// in alphabetical order — without sort.Strings, map iteration could
// produce them in any order, breaking determinism that downstream tests
// (Task 6 captureEnv assertions) rely on.
func TestBuildEnvAppendedKeysAreSorted(t *testing.T) {
	r := require.New(t)
	reg, err := New(Config{
		Command: "/bin/true",
		Env: []string{
			"FB_BROKEREXEC_TEST_ZULU=z",
			"FB_BROKEREXEC_TEST_ALPHA=a",
			"FB_BROKEREXEC_TEST_MIKE=m",
		},
	})
	r.NoError(err)

	env := reg.buildEnv()
	iAlpha := slices.Index(env, "FB_BROKEREXEC_TEST_ALPHA=a")
	iMike := slices.Index(env, "FB_BROKEREXEC_TEST_MIKE=m")
	iZulu := slices.Index(env, "FB_BROKEREXEC_TEST_ZULU=z")
	r.GreaterOrEqual(iAlpha, 0)
	r.Less(iAlpha, iMike, "alpha must precede mike in sorted append order")
	r.Less(iMike, iZulu, "mike must precede zulu in sorted append order")
}

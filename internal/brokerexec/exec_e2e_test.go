package brokerexec_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/brokerexec"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
	"github.com/wesm/fotobank/internal/testutil/brokerhelper"
)

func TestMain(m *testing.M) {
	if brokerhelper.IsHelper() {
		brokerhelper.Run()
		return
	}
	os.Exit(m.Run())
}

// helperCommand returns the path of the running test binary, which
// becomes the broker CLI when invoked with brokerhelper.EnvVar=1.
func helperCommand(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	require.NoError(t, err)
	return self
}

func sampleScope() share.Scope {
	return share.Scope{
		UUID:    "e2e-uuid",
		Owner:   owners.Principal{Hub: "h", UserID: "alice"},
		Grantee: owners.Principal{Hub: "h", UserID: "bob"},
	}
}

func TestE2EPublishSuccess(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e exec tests skipped under -short")
	}
	r, err := brokerexec.New(brokerexec.Config{
		Command: helperCommand(t),
		Env: []string{
			brokerhelper.EnvVar + "=1",
			"BROKEREXEC_TEST_EXIT=0",
			"BROKEREXEC_TEST_EXPECT_OPERATION=publish",
		},
	})
	require.NoError(t, err)
	require.NoError(t, r.PublishScope(context.Background(), sampleScope()))
}

func TestE2EPermanentExit(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e exec tests skipped under -short")
	}
	r, err := brokerexec.New(brokerexec.Config{
		Command: helperCommand(t),
		Env: []string{
			brokerhelper.EnvVar + "=1",
			"BROKEREXEC_TEST_EXIT=65",
			"BROKEREXEC_TEST_STDERR=scope already exists",
		},
	})
	require.NoError(t, err)

	err = r.PublishScope(context.Background(), sampleScope())
	require.ErrorIs(t, err, broker.ErrBrokerPermanent)
	require.ErrorContains(t, err, "scope already exists")
}

func TestE2ETimeoutKillsChild(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e exec tests skipped under -short")
	}
	r := require.New(t)
	reg, err := brokerexec.New(brokerexec.Config{
		Command:     helperCommand(t),
		CallTimeout: 50 * time.Millisecond,
		Env: []string{
			brokerhelper.EnvVar + "=1",
			"BROKEREXEC_TEST_SLEEP=5s",
			"BROKEREXEC_TEST_EXIT=0",
		},
	})
	r.NoError(err)

	start := time.Now()
	err = reg.PublishScope(context.Background(), sampleScope())
	elapsed := time.Since(start)

	r.ErrorIs(err, broker.ErrBrokerTransient)
	r.NotErrorIs(err, context.DeadlineExceeded,
		"per-call timeout must not propagate as DeadlineExceeded")
	r.Less(elapsed, 1*time.Second,
		"child must be killed promptly when call timeout fires")
}

func TestE2EParentCtxCancelKillsChild(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e exec tests skipped under -short")
	}
	r, err := brokerexec.New(brokerexec.Config{
		Command: helperCommand(t),
		Env: []string{
			brokerhelper.EnvVar + "=1",
			"BROKEREXEC_TEST_SLEEP=5s",
			"BROKEREXEC_TEST_EXIT=0",
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err = r.PublishScope(ctx, sampleScope())
	elapsed := time.Since(start)

	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, elapsed, 1*time.Second,
		"child must be killed promptly when caller ctx is cancelled")
}

func TestE2EEnvReachesChild(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e exec tests skipped under -short")
	}
	r, err := brokerexec.New(brokerexec.Config{
		Command: helperCommand(t),
		Env: []string{
			brokerhelper.EnvVar + "=1",
			"BROKEREXEC_TEST_EXIT=65",
			"BROKEREXEC_TEST_ECHO_ENV=FB_BROKER_ENV",
			"FB_BROKER_ENV=prod",
		},
	})
	require.NoError(t, err)

	err = r.PublishScope(context.Background(), sampleScope())
	require.ErrorIs(t, err, broker.ErrBrokerPermanent)
	require.ErrorContains(t, err, "prod",
		"configured env must be passed to the child process")
}

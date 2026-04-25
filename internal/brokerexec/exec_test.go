package brokerexec

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/share"
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

// fakeResult drives the unexported runFunc seam.
//
// stdoutBytes / stderrBytes are written to the supplied writers
// before return. exit is the returned exit code; runErr is the
// returned error. waitForCtx makes the fake block on ctx.Done()
// and return ctx.Err() — used to test per-call timeout behaviour
// without a real process.
//
// captureStdin / captureEnv / captureArgs collect inputs for assertion.
type fakeResult struct {
	stdoutBytes []byte
	stderrBytes []byte
	exit        int
	runErr      error
	waitForCtx  bool

	captureStdin *bytes.Buffer
	captureEnv   *[]string
	captureArgs  *[]string
}

func newFakeRun(r *fakeResult) runFunc {
	return func(ctx context.Context, command string, args, env []string,
		stdin io.Reader, stdout, stderr io.Writer) (int, error) {
		if r.captureStdin != nil {
			_, _ = io.Copy(r.captureStdin, stdin)
		} else {
			_, _ = io.Copy(io.Discard, stdin)
		}
		if r.captureEnv != nil {
			*r.captureEnv = append([]string(nil), env...)
		}
		if r.captureArgs != nil {
			*r.captureArgs = append([]string(nil), args...)
		}
		if r.waitForCtx {
			<-ctx.Done()
			return 0, ctx.Err()
		}
		if len(r.stdoutBytes) > 0 {
			_, _ = stdout.Write(r.stdoutBytes)
		}
		if len(r.stderrBytes) > 0 {
			_, _ = stderr.Write(r.stderrBytes)
		}
		return r.exit, r.runErr
	}
}

// newTestRegistrar builds a Registrar with the provided Config (Command
// defaults to /bin/true) and installs the fake runner.
func newTestRegistrar(t *testing.T, cfg Config, fr *fakeResult) *Registrar {
	t.Helper()
	if cfg.Command == "" {
		cfg.Command = "/bin/true"
	}
	r, err := New(cfg)
	require.NoError(t, err)
	r.runCmd = newFakeRun(fr)
	return r
}

func sampleScope() share.Scope {
	return share.Scope{
		UUID:          "test-uuid",
		Owner:         owners.Principal{Hub: "h", UserID: "alice"},
		Grantee:       owners.Principal{Hub: "h", UserID: "bob"},
		AllowDownload: true,
		Label:         "Trip",
	}
}

func TestPublishScopeSuccess(t *testing.T) {
	r := require.New(t)
	var stdin bytes.Buffer
	var args []string
	fr := &fakeResult{exit: 0, captureStdin: &stdin, captureArgs: &args}
	reg := newTestRegistrar(t, Config{
		PublishScopeArgs: []string{"scope", "publish"},
	}, fr)

	r.NoError(reg.PublishScope(context.Background(), sampleScope()))
	r.Equal([]string{"scope", "publish"}, args)

	var got map[string]any
	r.NoError(json.Unmarshal(stdin.Bytes(), &got))
	r.EqualValues(1, got["schema_version"])
	r.Equal("publish", got["operation"])
	scope := got["scope"].(map[string]any)
	r.Equal("test-uuid", scope["uuid"])
	r.Equal(true, scope["allow_download"])
}

func TestPublishScopeExitPermanent(t *testing.T) {
	fr := &fakeResult{exit: 65, stderrBytes: []byte("scope rejected")}
	reg := newTestRegistrar(t, Config{}, fr)
	err := reg.PublishScope(context.Background(), sampleScope())
	require.ErrorIs(t, err, broker.ErrBrokerPermanent)
	require.Contains(t, err.Error(), "scope rejected")
}

func TestPublishScopeExitTransient(t *testing.T) {
	fr := &fakeResult{exit: 75}
	reg := newTestRegistrar(t, Config{}, fr)
	err := reg.PublishScope(context.Background(), sampleScope())
	require.ErrorIs(t, err, broker.ErrBrokerTransient)
	require.NotErrorIs(t, err, broker.ErrBrokerPermanent)
}

func TestPublishScopeExitUnknown(t *testing.T) {
	fr := &fakeResult{exit: 1, stderrBytes: []byte("boom")}
	reg := newTestRegistrar(t, Config{}, fr)
	err := reg.PublishScope(context.Background(), sampleScope())
	require.ErrorIs(t, err, broker.ErrBrokerTransient)
	require.Contains(t, err.Error(), "exit=1")
}

func TestPublishScopeStderrTruncated(t *testing.T) {
	r := require.New(t)
	big := bytes.Repeat([]byte("X"), 8192) // 8 KiB
	fr := &fakeResult{exit: 65, stderrBytes: big}

	var captured bytes.Buffer
	handler := slog.NewTextHandler(&captured, nil)
	reg := newTestRegistrar(t, Config{Logger: slog.New(handler)}, fr)

	err := reg.PublishScope(context.Background(), sampleScope())
	r.ErrorIs(err, broker.ErrBrokerPermanent)

	// Logged stderr is bounded by the prefixBuffer cap (4 KiB), not
	// the full 8 KiB the fake wrote.
	logged := captured.String()
	r.Contains(logged, "stderr=")
	r.Less(len(logged), 6000,
		"logged stderr must not contain the full 8 KiB payload")
	// Wrapped error tail folds in only the last 256 bytes worth of X's
	// (the input is plain ASCII so 256 chars == 256 bytes).
	xRun := strings.Repeat("X", 256)
	r.Contains(err.Error(), xRun, "tail should hold 256 X's")
	r.NotContains(err.Error(), strings.Repeat("X", 257),
		"tail must be capped at 256 chars")
}

func TestPublishScopeStderrNormalized(t *testing.T) {
	fr := &fakeResult{exit: 65, stderrBytes: []byte("hello\x00\nworld\x07\t!")}
	reg := newTestRegistrar(t, Config{}, fr)
	err := reg.PublishScope(context.Background(), sampleScope())
	require.Contains(t, err.Error(), "hello world !")
}

func TestPublishScopeOmitsExpiresAt(t *testing.T) {
	var stdin bytes.Buffer
	fr := &fakeResult{exit: 0, captureStdin: &stdin}
	reg := newTestRegistrar(t, Config{}, fr)

	s := sampleScope()
	s.ExpiresAt = nil
	require.NoError(t, reg.PublishScope(context.Background(), s))

	var got map[string]any
	require.NoError(t, json.Unmarshal(stdin.Bytes(), &got))
	scope := got["scope"].(map[string]any)
	_, has := scope["expires_at"]
	require.False(t, has)
}

func TestPublishScopeRedactsMembership(t *testing.T) {
	r := require.New(t)
	var stdin bytes.Buffer
	fr := &fakeResult{exit: 0, captureStdin: &stdin}
	reg := newTestRegistrar(t, Config{}, fr)

	albumID := "album-1"
	s := sampleScope()
	s.TargetType = share.TargetMediaSet
	s.TargetAlbumID = &albumID
	r.NoError(reg.PublishScope(context.Background(), s))

	var got map[string]any
	r.NoError(json.Unmarshal(stdin.Bytes(), &got))
	scope := got["scope"].(map[string]any)
	for _, k := range []string{"target_type", "album_id", "media_ids"} {
		_, has := scope[k]
		r.Falsef(has, "scope payload must not include %q", k)
	}
}

func TestRevokeScopePayload(t *testing.T) {
	r := require.New(t)
	var stdin bytes.Buffer
	var args []string
	fr := &fakeResult{exit: 0, captureStdin: &stdin, captureArgs: &args}
	reg := newTestRegistrar(t, Config{
		PublishScopeArgs: []string{"scope", "publish"},
		RevokeScopeArgs:  []string{"scope", "revoke"},
	}, fr)

	r.NoError(reg.RevokeScope(context.Background(), "abc"))
	r.Equal([]string{"scope", "revoke"}, args)

	var got map[string]any
	r.NoError(json.Unmarshal(stdin.Bytes(), &got))
	r.EqualValues(1, got["schema_version"])
	r.Equal("revoke", got["operation"])
	r.Equal("abc", got["uuid"])
	_, has := got["scope"]
	r.False(has, "revoke payload must not contain a scope object")
}

func TestParentCtxCancellation(t *testing.T) {
	r := require.New(t)
	fr := &fakeResult{runErr: context.Canceled, exit: -1}
	reg := newTestRegistrar(t, Config{}, fr)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := reg.PublishScope(ctx, sampleScope())
	r.ErrorIs(err, context.Canceled)
	r.NotErrorIs(err, broker.ErrBrokerTransient,
		"ctx cancellation must NOT classify as transient — worker isCtxErr must match")
}

func TestPerCallTimeoutClassifiedTransient(t *testing.T) {
	r := require.New(t)
	fr := &fakeResult{waitForCtx: true}
	reg := newTestRegistrar(t, Config{
		CallTimeout: 10 * time.Millisecond,
	}, fr)
	err := reg.PublishScope(context.Background(), sampleScope())
	r.ErrorIs(err, broker.ErrBrokerTransient)
	r.NotErrorIs(err, context.DeadlineExceeded,
		"per-call timeout must not propagate as DeadlineExceeded")
	r.NotErrorIs(err, context.Canceled)
}

func TestRegistrarSatisfiesBrokerClient(t *testing.T) {
	var _ broker.BrokerClient = (*Registrar)(nil)
}

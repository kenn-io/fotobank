package cli

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/broker"
	"go.kenn.io/fotobank/internal/brokerexec"
	"go.kenn.io/fotobank/internal/config"
)

func TestNewBrokerClientStub(t *testing.T) {
	c, err := newBrokerClient(config.Broker{Mode: "stub"}, slog.Default())
	require.NoError(t, err)
	_, ok := c.(broker.NoopBroker)
	require.True(t, ok, "mode=stub must return NoopBroker, got %T", c)
}

func TestNewBrokerClientExec(t *testing.T) {
	c, err := newBrokerClient(config.Broker{
		Mode: "exec",
		Exec: config.BrokerExec{Command: "/bin/true"},
	}, slog.Default())
	require.NoError(t, err)
	_, ok := c.(*brokerexec.Registrar)
	require.True(t, ok, "mode=exec must return *brokerexec.Registrar, got %T", c)
}

func TestNewBrokerClientExecPropagatesConfigError(t *testing.T) {
	_, err := newBrokerClient(config.Broker{
		Mode: "exec",
		Exec: config.BrokerExec{Command: ""}, // missing
	}, slog.Default())
	require.Error(t, err, "exec with empty command must surface error")
}

func TestNewBrokerClientUnknown(t *testing.T) {
	_, err := newBrokerClient(config.Broker{Mode: "weird"}, slog.Default())
	require.Error(t, err)
	require.Contains(t, err.Error(), `unknown broker mode "weird"`)
}

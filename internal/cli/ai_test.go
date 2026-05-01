package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
)

func TestNewAIBackfillRejectsMissingTask(t *testing.T) {
	cmd := newAIBackfillCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--task is required")
}

func TestNewAIRetryFailedRejectsMissingTask(t *testing.T) {
	cmd := newAIRetryFailedCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--task is required")
}

func TestNewAIAcknowledgeRequiresFlag(t *testing.T) {
	cmd := newAIAcknowledgeCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.RunE(cmd, []string{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--hidden-processing flag required")
}

func TestParseTaskList(t *testing.T) {
	r := require.New(t)
	got, err := parseTaskList([]string{"tag,caption"})
	r.NoError(err)
	r.Equal([]ai.Task{ai.TaskTag, ai.TaskCaption}, got)

	got, err = parseTaskList([]string{"tag", "caption"})
	r.NoError(err)
	r.Equal([]ai.Task{ai.TaskTag, ai.TaskCaption}, got)

	// Unknown tasks are now an error rather than silently dropped.
	_, err = parseTaskList([]string{" tag , bogus "})
	r.Error(err)
	r.Contains(err.Error(), `"bogus"`)

	_, err = parseTaskList([]string{"bogus"})
	r.Error(err)

	got, err = parseTaskList(nil)
	r.NoError(err)
	r.Empty(got)

	// Duplicate tokens are deduplicated rather than running the op twice.
	got, err = parseTaskList([]string{"tag", "tag"})
	r.NoError(err)
	r.Equal([]ai.Task{ai.TaskTag}, got)
}

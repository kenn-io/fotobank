package checkout

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
)

func TestPublishRefusesExistingDestination(t *testing.T) {
	r := require.New(t)
	root, err := os.OpenRoot(t.TempDir())
	r.NoError(err)
	t.Cleanup(func() { r.NoError(root.Close()) })
	temp, err := root.OpenFile("copy.tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	r.NoError(err)
	_, err = temp.WriteString("new bytes")
	r.NoError(err)
	r.NoError(temp.Close())
	r.NoError(root.WriteFile("photo.jpg", []byte("existing bytes"), 0o600))

	err = publish(root, "copy.tmp", "photo.jpg")
	r.ErrorIs(err, errs.ErrAlreadyExists)
	got, err := root.ReadFile("photo.jpg")
	r.NoError(err)
	r.Equal([]byte("existing bytes"), got)
}

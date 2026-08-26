package content_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/errs"
)

const (
	testOwnerKey = "550e8400-e29b-41d4-a716-446655440000"
	testFileID   = "7d9b9b0e-77d4-4f80-87eb-7bfbe74716b3"
)

func TestVirtualPath(t *testing.T) {
	got, err := content.VirtualPath(testOwnerKey, testFileID, "IMG_0001.JPG")
	require.NoError(t, err)
	require.Equal(t,
		"/owners/550e8400-e29b-41d4-a716-446655440000/media/"+
			"7d9b9b0e-77d4-4f80-87eb-7bfbe74716b3/IMG_0001.JPG",
		got,
	)
}

func TestVirtualPathNormalizesBasenameToNFC(t *testing.T) {
	got, err := content.VirtualPath(testOwnerKey, testFileID, "Cafe\u0301.JPG")
	require.NoError(t, err)
	require.Equal(t,
		"/owners/"+testOwnerKey+"/media/"+testFileID+"/Caf\u00e9.JPG",
		got,
	)
}

func TestVirtualPathRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name     string
		ownerKey string
		fileID   string
		basename string
	}{
		{name: "invalid owner UUID", ownerKey: "owner", fileID: testFileID, basename: "IMG.JPG"},
		{name: "noncanonical owner UUID", ownerKey: "550E8400-E29B-41D4-A716-446655440000", fileID: testFileID, basename: "IMG.JPG"},
		{name: "invalid file UUID", ownerKey: testOwnerKey, fileID: "file", basename: "IMG.JPG"},
		{name: "noncanonical file UUID", ownerKey: testOwnerKey, fileID: "7D9B9B0E-77D4-4F80-87EB-7BFBE74716B3", basename: "IMG.JPG"},
		{name: "invalid UTF-8", ownerKey: testOwnerKey, fileID: testFileID, basename: string([]byte{0xff})},
		{name: "empty basename", ownerKey: testOwnerKey, fileID: testFileID, basename: ""},
		{name: "dot basename", ownerKey: testOwnerKey, fileID: testFileID, basename: "."},
		{name: "dot-dot basename", ownerKey: testOwnerKey, fileID: testFileID, basename: ".."},
		{name: "NUL", ownerKey: testOwnerKey, fileID: testFileID, basename: "IMG\x00.JPG"},
		{name: "slash", ownerKey: testOwnerKey, fileID: testFileID, basename: "dir/IMG.JPG"},
		{name: "backslash", ownerKey: testOwnerKey, fileID: testFileID, basename: `dir\IMG.JPG`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := content.VirtualPath(tt.ownerKey, tt.fileID, tt.basename)
			require.ErrorIs(t, err, errs.ErrInvalidArgument)
		})
	}
}

package checkout

import (
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/errs"
)

func TestWorkingPathRejectsNonPortableFilenames(t *testing.T) {
	invalid := []string{
		"", ".", "..", "dir/photo.jpg", `dir\photo.jpg`, "photo\x00.jpg", string([]byte{0xff}),
		"photo<.jpg", "photo>.jpg", "photo:.jpg", `photo".jpg`, "photo|.jpg", "photo?.jpg", "photo*.jpg",
		"photo\x1f.jpg", "photo.jpg.", "photo.jpg ",
		"CON", "con.jpg", "PRN.raw", "AUX", "NUL.tar.gz", "COM1", "com9.jpg", "COM¹.jpg",
		"LPT1", "lpt9.xmp", "LPT².raw", "CONIN$", "conout$.txt",
	}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := workingPath(Candidate{AssetID: "asset-id", OriginalFilename: name})
			require.ErrorIs(t, err, errs.ErrInvalidArgument)
		})
	}
}

func TestWorkingPathAcceptsPortableFilenames(t *testing.T) {
	valid := []string{"IMG_0042.JPG", ".photo.jpg", "COM0.jpg", "COM10.jpg", "LPT0", "auxiliary.jpg", "Café.jpg"}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			got, err := workingPath(Candidate{AssetID: "asset-id", OriginalFilename: name})
			require.NoError(t, err)
			require.Equal(t, "undated/asset-id/"+name, got)
		})
	}
}

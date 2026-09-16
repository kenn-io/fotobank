package client

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"uuid"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/httpapi"
)

// MediaFileSelection selects the primary file unless FileID names an attachment.
type MediaFileSelection struct {
	MediaID uuid.UUID
	FileID  *uuid.UUID
}

// DownloadMedia streams through the proven daemon and checks the bytes against
// its metadata response. The caller must discard the output on any error.
// Concurrent content changes fail verification rather than mixing versions.
func DownloadMedia(ctx context.Context, configPath, version string, selection MediaFileSelection, dst io.Writer) (file httpapi.FileDTO, err error) {
	rec, _, found, err := findDaemon(ctx, configPath)
	if err != nil {
		return file, err
	}
	if !found || rec.Version != version {
		return file, fmt.Errorf("no matching Fotobank server; run fotobank daemon start")
	}
	var item httpapi.MediaDTO
	if err := callRecord(ctx, rec, &item, "retry media download", func(c *generated.Client) (*generated.GetMediaResponse, error) {
		return c.GetMedia(ctx, &generated.GetMediaRequestOptions{PathParams: &generated.GetMediaPath{ID: selection.MediaID.String()}})
	}); err != nil {
		return file, err
	}
	file = httpapi.FileDTO{Role: "primary", MimeType: item.MimeType,
		OriginalFilename: item.OriginalFilename, Size: item.Size, SHA256: item.SHA256}
	if selection.FileID != nil {
		matched := false
		if item.Files != nil {
			for _, attached := range *item.Files {
				if attached.ID == selection.FileID.String() {
					file, matched = attached, true
					break
				}
			}
		}
		if !matched {
			return file, fmt.Errorf("download attachment: %w", errs.ErrNotFound)
		}
	}
	api, transport, err := recordAPI(ctx, rec)
	if err != nil {
		return file, err
	}
	c := generated.NewClient(downloadAPI{api, transport})
	var response *http.Response
	if selection.FileID == nil {
		result, requestErr := c.DownloadMediaOriginalWithResponse(ctx, &generated.DownloadMediaOriginalRequestOptions{PathParams: &generated.DownloadMediaOriginalPath{ID: selection.MediaID}})
		if requestErr != nil {
			if result != nil && result.HTTPResponse != nil {
				_ = result.HTTPResponse.Body.Close()
			}
			return file, requestErr
		}
		response = result.HTTPResponse
	} else {
		result, requestErr := c.DownloadMediaFileWithResponse(ctx, &generated.DownloadMediaFileRequestOptions{PathParams: &generated.DownloadMediaFilePath{ID: selection.MediaID, FileID: *selection.FileID}})
		if requestErr != nil {
			if result != nil && result.HTTPResponse != nil {
				_ = result.HTTPResponse.Body.Close()
			}
			return file, requestErr
		}
		response = result.HTTPResponse
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	err = verifyDownload(dst, response, file)
	return file, err
}

func verifyDownload(dst io.Writer, response *http.Response, file httpapi.FileDTO) error {
	want, err := hex.DecodeString(file.SHA256)
	if err != nil || len(want) != sha256.Size || file.Size < 0 {
		return fmt.Errorf("download metadata: %w", errs.ErrContentIdentityMismatch)
	}
	if response.StatusCode != http.StatusOK || (response.ContentLength >= 0 && response.ContentLength != file.Size) {
		return fmt.Errorf("download response does not describe the full file: %w", errs.ErrContentIdentityMismatch)
	}
	hash := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(dst, hash), response.Body, file.Size); err != nil {
		return fmt.Errorf("read download: %w", err)
	}
	// Read through EOF, but do not allow an oversized response to fill the disk.
	extra, err := io.Copy(io.Discard, io.LimitReader(response.Body, 1))
	if err != nil {
		return fmt.Errorf("finish download: %w", err)
	}
	if extra != 0 || !bytes.Equal(hash.Sum(nil), want) {
		return fmt.Errorf("verify download size and SHA-256: %w; retry if the stored file changed", errs.ErrContentIdentityMismatch)
	}
	return nil
}

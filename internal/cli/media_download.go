package cli

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"uuid"

	"github.com/spf13/cobra"
	"go.kenn.io/fotobank/internal/client"
	"go.kenn.io/fotobank/internal/version"
)

type mediaDownloadResult struct {
	MediaID uuid.UUID  `json:"media_id"`
	FileID  *uuid.UUID `json:"file_id,omitempty"`
	Output  string     `json:"output"`
	Size    int64      `json:"size"`
	SHA256  string     `json:"sha256"`
}

func newMediaDownloadCmd() *cobra.Command {
	var cfgPath, output, fileID string
	var asJSON bool
	cmd := &cobra.Command{
		Use: "download <id>", Short: "Save a verified original or attached file",
		Long: "Download the primary file, or an attachment listed by media show. Requires an explicit local output path with an existing parent directory. Never overwrites a file. Hidden media is not included. The daemon starts when needed.",
		Example: `  fotobank media download <photo-id> --output photo.jpg
  fotobank media download <photo-id> --file <file-id> --output photo.xmp --json`,
		Args: usageArgs(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := uuid.Parse(args[0])
			if err != nil {
				return newUsageError("media id must be a UUID")
			}
			selection := client.MediaFileSelection{MediaID: id}
			if cmd.Flags().Changed("file") {
				parsed, err := uuid.Parse(fileID)
				if err != nil {
					return newUsageError("--file must be an attachment UUID from media show")
				}
				selection.FileID = &parsed
			}
			if output == "" || output == "-" {
				return newUsageError("--output must name a local file; binary stdout is not supported")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result, err := saveMediaDownload(ctx, cfgPath, selection, output)
			if err != nil {
				return err
			}
			if asJSON {
				return json.MarshalWrite(cmd.OutOrStdout(), result)
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "Saved %q (%d bytes, SHA-256 %s)\n", result.Output, result.Size, result.SHA256)
			return err
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", "", "path to config file")
	cmd.Flags().StringVar(&output, "output", "", "destination file (required; never overwritten)")
	cmd.Flags().StringVar(&fileID, "file", "", "attached file UUID from media show (default: primary file)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit the saved path, IDs, size and SHA-256 as JSON")
	return cmd
}

func saveMediaDownload(ctx context.Context, cfgPath string, selection client.MediaFileSelection, output string) (result mediaDownloadResult, err error) {
	path, err := localOperatorPath(output)
	if err != nil {
		return result, err
	}
	directory, name := filepath.Split(path)
	if !filepath.IsLocal(name) || name == "." {
		return result, newUsageError("--output must name a file")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return result, fmt.Errorf("resolve output directory: %w", err)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return result, fmt.Errorf("open output directory: %w", err)
	}
	defer root.Close()
	if _, err := root.Lstat(name); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			err = os.ErrExist
		}
		return result, fmt.Errorf("download destination %q: %w", output, err)
	}
	// Open before automatic startup so path and permission errors create no daemon.
	tempName := ".fotobank-download-" + uuid.New().String() + ".tmp"
	file, err := root.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, fmt.Errorf("create temporary download: %w", err)
	}
	defer func() {
		_ = file.Close()
		err = errors.Join(err, root.Remove(tempName))
	}()
	resolved, _, err := localOperatorConfig(ctx, cfgPath)
	if err != nil {
		return result, err
	}
	metadata, err := client.DownloadMedia(ctx, resolved, version.Short, selection, file)
	if err != nil {
		return result, err
	}
	if err := file.Sync(); err != nil {
		return result, fmt.Errorf("sync download: %w", err)
	}
	if err := file.Close(); err != nil {
		return result, fmt.Errorf("close download: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	// Link publishes a completed local copy without replacing a concurrent output.
	// This is never a link to Docbank storage. Filesystems without hardlinks fail.
	if err := root.Link(tempName, name); err != nil {
		return result, fmt.Errorf("publish download (requires hardlink support): %w", err)
	}
	return mediaDownloadResult{MediaID: selection.MediaID, FileID: selection.FileID,
		Output: filepath.Join(directory, name), Size: metadata.Size, SHA256: metadata.SHA256}, nil
}

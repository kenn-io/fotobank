package cli_test

import (
	"bytes"
	"crypto/sha256"
	json "encoding/json/v2"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/backup"
	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/httpapi"
)

// Exercise the recovered catalog through the daemon, not through a second
// database connection. A restored file alone does not prove a usable library.
func TestRecoveredLibraryWorkflows(t *testing.T) {
	r := require.New(t)
	dir := t.TempDir()
	sourceStorage := filepath.Join(dir, "source")
	r.NoError(os.Mkdir(sourceStorage, 0o700))
	sourceConfig := writeBackupConfig(t, sourceStorage)
	// Keep configuration separately from the storage we will lose.
	configPath := filepath.Join(dir, "saved.toml")
	r.NoError(os.Rename(sourceConfig, configPath))
	t.Setenv("FOTOBANK_DB_PATH", "")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", "")
	input := filepath.Join(sourceStorage, "input")
	r.NoError(os.Mkdir(input, 0o700))
	originals := map[string][]byte{}
	for name, shade := range map[string]color.RGBA{
		"coast.jpg": {R: 30, G: 90, B: 160, A: 255},
		"woods.jpg": {R: 40, G: 120, B: 50, A: 255},
	} {
		photo := image.NewRGBA(image.Rect(0, 0, 48, 32))
		draw.Draw(photo, photo.Bounds(), image.NewUniform(shade), image.Point{}, draw.Src)
		var encoded bytes.Buffer
		r.NoError(jpeg.Encode(&encoded, photo, nil))
		originals[name] = bytes.Clone(encoded.Bytes())
		r.NoError(os.WriteFile(filepath.Join(input, name), originals[name], 0o600))
	}
	sidecar := []byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"><rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"/></x:xmpmeta>`)
	r.NoError(os.WriteFile(filepath.Join(input, "coast.jpg.xmp"), sidecar, 0o600))

	run := func(t *testing.T, cfg string, args ...string) []byte {
		t.Helper()
		var out, diagnostics bytes.Buffer
		code := cli.RunContext(t.Context(), append(args, "--config", cfg), &out, &diagnostics)
		require.Zero(t, code, "%v: %s", args, diagnostics.String())
		return out.Bytes()
	}
	httpClient := &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(httpClient.CloseIdleConnections)
	get := func(t *testing.T, url string) []byte {
		t.Helper()
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
		require.NoError(t, err)
		response, err := httpClient.Do(request)
		require.NoError(t, err)
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, "%s", body)
		return body
	}
	type photoItem struct {
		ID               string `json:"id"`
		OriginalFilename string `json:"original_filename"`
		SHA256           string `json:"sha256"`
		ThumbStatus      string `json:"thumb_status"`
		ThumbVersion     int    `json:"thumb_version"`
		Files            []struct {
			ID               string `json:"id"`
			Role             string `json:"role"`
			OriginalFilename string `json:"original_filename"`
		} `json:"files"`
	}
	type photoList struct {
		Items []photoItem `json:"items"`
	}
	var before photoList
	var albumID string
	var working httpapi.CheckoutCreateResult
	repository := filepath.Join(dir, "archive")
	if !t.Run("capture", func(t *testing.T) {
		r := require.New(t)
		record := startCheckoutServer(t, configPath, filepath.Join(sourceStorage, "flash", "fotobank.sqlite"))
		run(t, configPath, "import", input)
		r.NoError(json.Unmarshal(get(t, record.Metadata["web_url"]+"/api/v1/media"), &before))
		r.Len(before.Items, 2)
		albumID = strings.Fields(string(run(t, configPath, "albums", "create", "Recovery trip")))[0]
		run(t, configPath, "albums", "add", albumID, before.Items[0].ID, before.Items[1].ID)
		root := filepath.Join(sourceStorage, "working")
		r.NoError(os.Mkdir(root, 0o700))
		r.NoError(json.Unmarshal(run(t, configPath, "checkout", "create", root, "--album", albumID, "--json"), &working))
		r.Equal(3, working.Materialized, "both photos and the attached sidecar")
		// This edit is deliberately not committed to Docbank. Recovery must
		// return the archived original, not pretend to save working files.
		item := before.Items[0]
		r.NoError(os.WriteFile(filepath.Join(working.Root, "undated", item.ID, item.OriginalFilename), []byte("uncommitted edit"), 0o600))
		run(t, configPath, "backup", "init", "--repo", repository)
		run(t, configPath, "backup", "create", "--repo", repository)
	}) {
		return
	}

	// The capture daemon has joined shutdown. Remove only this test's source
	// tree, including its originals, vault, catalog, artifacts and checkout.
	r.NoError(os.RemoveAll(sourceStorage))
	var restored backup.ArchiveRestoreReport
	if !t.Run("restore without source storage", func(t *testing.T) {
		startRecoveryServer(t, configPath)
		run(t, configPath, "backup", "verify", "--repo", repository, "--all")
		require.NoError(t, json.Unmarshal(run(t, configPath, "backup", "restore", "--repo", repository,
			"--target", filepath.Join(dir, "restored"), "--json"), &restored))
		require.NoDirExists(t, sourceStorage)
	}) {
		return
	}

	recovered := filepath.Join(dir, "recovered-runtime")
	r.NoError(os.Mkdir(recovered, 0o700))
	recoveredConfig := writeBackupConfig(t, recovered)
	configBytes, err := os.ReadFile(recoveredConfig)
	r.NoError(err)
	configBytes = fmt.Appendf(configBytes, "\n[docbank]\nroot = %q\n[thumbs]\npoll_interval = '50ms'\n", restored.VaultRoot)
	r.NoError(os.WriteFile(recoveredConfig, configBytes, 0o600))
	t.Setenv("FOTOBANK_DB_PATH", restored.CatalogPath)
	record := startCheckoutServer(t, recoveredConfig, restored.CatalogPath)
	base := record.Metadata["web_url"]
	var status httpapi.CheckoutStatusOutput
	r.NoError(json.Unmarshal(run(t, recoveredConfig, "checkout", "status", working.CheckoutID, "--json"), &status))
	r.Equal(working.CheckoutID, status.Checkout.ID)
	r.Equal(3, status.Checkout.Entries.Total)
	r.NoError(json.Unmarshal(run(t, recoveredConfig, "checkout", "retire", working.CheckoutID, "--confirm", "--json"), &status))
	r.Equal(checkout.StateRetired, status.Checkout.State)
	r.Equal(3, status.Checkout.Entries.Total)
	r.NoDirExists(sourceStorage)

	var after photoList
	r.NoError(json.Unmarshal(get(t, base+"/api/v1/media"), &after))
	r.Len(after.Items, 2)
	var album httpapi.AlbumDTO
	r.NoError(json.Unmarshal(get(t, base+"/api/v1/albums/"+albumID), &album))
	r.Equal("Recovery trip", album.Name)
	r.Equal(2, album.ItemCount)
	var members photoList
	r.NoError(json.Unmarshal(get(t, base+"/api/v1/albums/"+albumID+"/media"), &members))
	r.Len(members.Items, 2)
	r.ElementsMatch([]string{before.Items[0].ID, before.Items[1].ID}, []string{members.Items[0].ID, members.Items[1].ID})
	for _, item := range after.Items {
		original, ok := originals[item.OriginalFilename]
		r.True(ok, "unexpected photo %q", item.OriginalFilename)
		r.Equal(fmt.Sprintf("%x", sha256.Sum256(original)), item.SHA256)
		r.Equal(original, get(t, base+"/api/v1/media/"+item.ID+"/original"))
		if item.OriginalFilename == "coast.jpg" {
			var detail photoItem
			r.NoError(json.Unmarshal(get(t, base+"/api/v1/media/"+item.ID), &detail))
			r.Len(detail.Files, 1)
			r.Equal("sidecar", detail.Files[0].Role)
			r.Equal("coast.jpg.xmp", detail.Files[0].OriginalFilename)
			r.Equal(sidecar, get(t, base+"/api/v1/media/"+item.ID+"/files/"+detail.Files[0].ID+"/content"))
		}
	}

	// The fresh NAS and cache contain no source thumbnails. Regenerate through
	// the operator API, then fetch usable images from the normal photo API.
	run(t, recoveredConfig, "thumbs", "regenerate", "--all")
	r.Eventually(func() bool {
		request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/api/v1/media", nil)
		if err != nil {
			return false
		}
		response, err := httpClient.Do(request)
		if err != nil {
			return false
		}
		defer response.Body.Close()
		var current photoList
		if response.StatusCode != http.StatusOK || json.UnmarshalRead(response.Body, &current) != nil {
			return false
		}
		for _, item := range current.Items {
			if item.ThumbStatus != "ready" {
				return false
			}
		}
		after = current
		return len(current.Items) == 2
	}, 30*time.Second, 50*time.Millisecond)
	for _, item := range after.Items {
		body := get(t, fmt.Sprintf("%s/api/v1/media/%s/thumb?size=grid&v=%d", base, item.ID, item.ThumbVersion))
		thumbnail, _, err := image.Decode(bytes.NewReader(body))
		r.NoError(err)
		r.Positive(thumbnail.Bounds().Dx())
		r.Positive(thumbnail.Bounds().Dy())
	}
	r.NoDirExists(sourceStorage, "normal recovered operations must not recreate lost storage")
}

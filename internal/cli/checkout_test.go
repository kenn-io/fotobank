package cli_test

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/fotobank/internal/checkout"
	"go.kenn.io/fotobank/internal/cli"
	"go.kenn.io/fotobank/internal/content"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/media"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/testutil/assetfixture"
)

func startCheckoutServer(t *testing.T, cfgPath, dbPath string) daemon.RuntimeRecord {
	t.Helper()
	r := require.New(t)
	serverCtx, stopServer := context.WithCancel(t.Context())
	serverDone := make(chan int, 1)
	var serverErrors lockedBuffer
	go func() {
		serverDone <- cli.RunContext(serverCtx, []string{
			"serve", "--config", cfgPath, "--listen", "127.0.0.1:0",
		}, io.Discard, &serverErrors)
	}()
	t.Cleanup(func() {
		stopServer()
		select {
		case code := <-serverDone:
			r.Zero(code, "%s", serverErrors.String())
		case <-time.After(10 * time.Second):
			r.Fail("server did not stop", "%s", serverErrors.String())
		}
	})
	r.Eventually(func() bool {
		paths, err := filepath.Glob(dbPath + ".operator/daemon.*.json")
		return err == nil && len(paths) == 1
	}, 10*time.Second, 20*time.Millisecond, "%s", serverErrors.String())
	store := daemon.RuntimeStore{Dir: dbPath + ".operator"}
	records, err := store.List()
	r.NoError(err)
	r.Len(records, 1)
	return records[0]
}

func TestCheckoutListAndStatus(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	other := owners.Principal{Hub: "h", UserID: "other"}
	for _, principal := range []owners.Principal{owner, other} {
		_, err = database.WriteDB().ExecContext(t.Context(),
			`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
			principal.Hub, principal.UserID, uuid.NewString(), time.Now().UTC())
		r.NoError(err)
	}
	repo := checkout.NewRepo(database.WriteDB(), database.ReadDB())
	now := time.Date(2026, time.September, 6, 14, 0, 0, 0, time.UTC)
	checkoutID := uuid.NewString()
	r.NoError(repo.Insert(t.Context(), checkout.Checkout{
		ID: checkoutID, Owner: owner, Root: filepath.Join(tmp, "working"),
		Layout: "capture_date", Selection: checkout.Selection{Years: []checkout.YearRange{{Start: 2025, End: 2026}}},
		State: checkout.StateError, CreatedAt: now, UpdatedAt: now,
	}))
	for index, state := range []checkout.EntryState{checkout.EntryPending, checkout.EntryConflict} {
		fileID := uuid.NewString()
		r.NoError(repo.InsertEntry(t.Context(), checkout.Entry{
			CheckoutID: checkoutID, FileID: fileID, RelativePath: "2025/photo-" + fileID + ".jpg",
			BaseVersionID: "version-1", BaseSHA256: strings.Repeat("a", 64), BaseSize: 10,
			ObservedSize: 10, ObservedMTime: now, ObservedIdentity: fmt.Sprintf("identity-%d", index),
			ObservedSHA256: strings.Repeat("b", 64), State: state,
			LastError: "newer authority exists", CreatedAt: now, UpdatedAt: now,
		}))
	}
	_, err = database.WriteDB().ExecContext(t.Context(), `UPDATE checkout_entries
		SET last_error = 'newer authority exists' WHERE checkout_id = ? AND state = 'conflict'`, checkoutID)
	r.NoError(err)
	otherID := uuid.NewString()
	r.NoError(repo.Insert(t.Context(), checkout.Checkout{
		ID: otherID, Owner: other, Root: filepath.Join(tmp, "other-working"),
		Layout: "capture_date", Selection: checkout.Selection{All: true}, State: checkout.StateError,
		CreatedAt: now, UpdatedAt: now,
	}))
	r.NoError(database.Close())
	addressFile := filepath.Join(tmp, "listen-address")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", addressFile)
	record := startCheckoutServer(t, cfgPath, dbPath)
	r.Eventually(func() bool { _, err := os.Stat(addressFile); return err == nil }, 5*time.Second, 10*time.Millisecond)
	address, err := os.ReadFile(addressFile)
	r.NoError(err)
	for _, tc := range []struct {
		name, base, token, hub string
		status                 int
	}{
		{"operator inspection", record.Endpoint().BaseURL(), record.Metadata["token"], "h", http.StatusOK},
		{"missing credential", record.Endpoint().BaseURL(), "", "h", http.StatusUnauthorized},
		{"wrong owner", record.Endpoint().BaseURL(), record.Metadata["token"], "other", http.StatusForbidden},
		{"photo listener", "http://" + string(address), record.Metadata["token"], "h", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := require.New(t)
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				tc.base+"/api/v1/operator/checkouts?hub="+tc.hub+"&user_id=u", nil)
			r.NoError(err)
			request.Header.Set("Authorization", "Bearer "+tc.token)
			response, err := http.DefaultClient.Do(request)
			r.NoError(err)
			defer response.Body.Close()
			r.Equal(tc.status, response.StatusCode)
			if tc.status == http.StatusOK {
				var listed []struct {
					ID string `json:"id"`
				}
				r.NoError(json.UnmarshalRead(response.Body, &listed))
				r.Len(listed, 1)
				r.Equal(checkoutID, listed[0].ID)
			}
		})
	}

	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "list", "--config", cfgPath, "--json",
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	var listed []struct {
		ID      string `json:"id"`
		Entries struct {
			Total    int `json:"total"`
			Pending  int `json:"pending"`
			Conflict int `json:"conflict"`
		} `json:"entries"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &listed))
	r.Len(listed, 1)
	r.Equal(checkoutID, listed[0].ID)
	r.Equal(2, listed[0].Entries.Total)
	r.Equal(1, listed[0].Entries.Pending)
	r.Equal(1, listed[0].Entries.Conflict)

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "status", checkoutID, "--config", cfgPath, "--json",
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	var status struct {
		Checkout struct {
			ID      string `json:"id"`
			Entries struct {
				Total    int `json:"total"`
				Pending  int `json:"pending"`
				Conflict int `json:"conflict"`
			} `json:"entries"`
		} `json:"checkout"`
		Selection struct {
			Years []struct {
				Start int `json:"start"`
				End   int `json:"end"`
			} `json:"years"`
		} `json:"selection"`
		Problems []struct {
			State     checkout.EntryState `json:"state"`
			LastError string              `json:"last_error"`
		} `json:"problems"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &status))
	r.Equal(checkoutID, status.Checkout.ID)
	r.Equal(2, status.Checkout.Entries.Total)
	r.Equal(1, status.Checkout.Entries.Pending)
	r.Equal(1, status.Checkout.Entries.Conflict)
	r.Equal([]struct {
		Start int `json:"start"`
		End   int `json:"end"`
	}{{Start: 2025, End: 2026}}, status.Selection.Years)
	r.Len(status.Problems, 2)
	r.Equal("newer authority exists", status.Problems[0].LastError+status.Problems[1].LastError)

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "status", otherID, "--config", cfgPath, "--json",
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "not found")

}

func TestCheckoutEstimateAndCreate(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)

	database, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err = database.WriteDB().ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	contentStore, err := content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	body := []byte("checkout bytes")
	item := assetfixture.InsertContent(t,
		media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore, body,
		media.Media{Owner: owner, OriginalFilename: "IMG_0100.JPG"})
	invalid := assetfixture.InsertContent(t,
		media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore, []byte("invalid filename"),
		media.Media{Owner: owner, OriginalFilename: "bad:name.jpg"})
	r.NoError(contentStore.Close())
	r.NoError(database.Close())
	configBytes, err := os.ReadFile(cfgPath)
	r.NoError(err)
	r.NoError(os.WriteFile(cfgPath, append(configBytes, []byte("\n[checkouts]\nscan_interval = '100ms'\nsettle_interval = '0s'\n")...), 0o600))
	record := startCheckoutServer(t, cfgPath, dbPath)

	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "estimate", "--config", cfgPath, "--asset", item.ID,
	}, &stdout, &stderr)
	r.Equal(0, code, "stderr=%s", stderr.String())
	r.Equal("files=1\tbytes=14\n", stdout.String())

	root := filepath.Join(tmp, "checkout")
	r.NoError(os.Mkdir(root, 0o700))
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "estimate", "--config", cfgPath, "--asset", item.ID, "--json",
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	r.JSONEq(`{"files":1,"bytes":14}`, stdout.String())
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root, "--max-bytes", "1", "--json",
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stdout.String(), "limit is 1")
	stdout.Reset()
	stderr.Reset()
	// Relative destinations are interpreted by the CLI, not the server.
	t.Chdir(tmp)
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, "checkout", "--json",
	}, &stdout, &stderr)
	r.Equal(0, code, "stderr=%s", stderr.String())
	var created struct {
		CheckoutID   string `json:"checkout_id"`
		Materialized int    `json:"materialized"`
		Files        int    `json:"files"`
		Bytes        int64  `json:"bytes"`
		Root         string `json:"root"`
		Error        string `json:"error"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &created))
	r.NotEmpty(created.CheckoutID)
	r.Equal(1, created.Materialized)
	r.Equal(1, created.Files)
	r.Equal(int64(14), created.Bytes)
	// Canonical paths may expand Windows short names or temporary-directory aliases.
	r.True(filepath.IsAbs(created.Root))
	wantedRoot, err := os.Stat(root)
	r.NoError(err)
	reportedRoot, err := os.Stat(created.Root)
	r.NoError(err)
	r.True(os.SameFile(wantedRoot, reportedRoot), "creation must report the requested directory")
	checkoutID := created.CheckoutID
	got, err := os.ReadFile(filepath.Join(root, "undated", item.ID, "IMG_0100.JPG"))
	r.NoError(err)
	r.Equal(body, got)
	t.Run("relative symlink", func(t *testing.T) {
		check := require.New(t)
		target := filepath.Join(tmp, "target")
		check.NoError(os.MkdirAll(filepath.Join(target, "child"), 0o700))
		check.NoError(os.Mkdir(filepath.Join(target, "child", "working"), 0o700))
		if err := os.Symlink(filepath.Join(target, "child"), filepath.Join(tmp, "shortcut")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		var output, errors bytes.Buffer
		code := cli.RunContext(t.Context(), []string{
			"checkout", "create", "--config", cfgPath, "--asset", item.ID, "shortcut/working", "--json",
		}, &output, &errors)
		check.Zero(code, "stderr=%s", errors.String())
		copied, err := os.ReadFile(filepath.Join(target, "child", "working", "undated", item.ID, "IMG_0100.JPG"))
		check.NoError(err)
		check.Equal(body, copied)
		t.Run("Unix symlink parent", func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("Windows cleans parent components before following symlinks")
			}
			check := require.New(t)
			check.NoError(os.Mkdir(filepath.Join(target, "working"), 0o700))
			var output, errors bytes.Buffer
			code := cli.RunContext(t.Context(), []string{
				"checkout", "create", "--config", cfgPath, "--asset", item.ID, "shortcut/../working", "--json",
			}, &output, &errors)
			check.Zero(code, "stderr=%s", errors.String())
			copied, err := os.ReadFile(filepath.Join(target, "working", "undated", item.ID, "IMG_0100.JPG"))
			check.NoError(err)
			check.Equal(body, copied)
		})
	})
	for _, rejectedRoot := range []string{root, filepath.Join(tmp, "nas"), filepath.Join(tmp, "flash", "docbank")} {
		stdout.Reset()
		stderr.Reset()
		code = cli.RunContext(t.Context(), []string{
			"checkout", "create", "--config", cfgPath, "--asset", item.ID, rejectedRoot, "--json",
		}, &stdout, &stderr)
		r.NotZero(code)
		var failure struct {
			Error string `json:"error"`
		}
		r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
		r.NotEmpty(failure.Error)
	}

	failedRoot := filepath.Join(tmp, "failed-checkout")
	r.NoError(os.Mkdir(failedRoot, 0o700))
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", invalid.ID, failedRoot, "--json",
	}, &stdout, &stderr)
	r.NotZero(code)
	r.NoError(json.Unmarshal(stdout.Bytes(), &created))
	r.NotEmpty(created.CheckoutID)
	r.NotEqual(checkoutID, created.CheckoutID)
	r.Zero(created.Materialized)
	r.NotEmpty(created.Error)

	edited := []byte("edited checkout bytes")
	r.NoError(os.WriteFile(
		filepath.Join(root, "undated", item.ID, "IMG_0100.JPG"), edited, 0o600))
	database, err = db.Open(dbPath)
	r.NoError(err)
	var failedState string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state FROM checkouts WHERE id = ?`, created.CheckoutID).Scan(&failedState))
	r.Equal("error", failedState)
	r.Eventually(func() bool {
		var pending int
		err := database.ReadDB().QueryRowContext(t.Context(),
			`SELECT COUNT(*) FROM checkout_entries WHERE checkout_id = ? AND state = 'pending'`, checkoutID).Scan(&pending)
		return err == nil && pending == 1
	}, 10*time.Second, 20*time.Millisecond)
	r.NoError(database.Close())
	for _, denied := range []struct {
		name   string
		token  string
		hub    string
		status int
	}{
		{name: "no operator token", hub: "h", status: http.StatusUnauthorized},
		{name: "different configured owner", token: record.Metadata["token"], hub: "other", status: http.StatusForbidden},
	} {
		t.Run(denied.name, func(t *testing.T) {
			check := require.New(t)
			for _, endpoint := range []string{"/api/v1/operator/checkouts/" + checkoutID + "/commit", "/api/v1/operator/checkouts/estimate", "/api/v1/operator/checkouts"} {
				body := map[string]any{"hub": denied.hub, "user_id": "u"}
				if endpoint == "/api/v1/operator/checkouts/estimate" || endpoint == "/api/v1/operator/checkouts" {
					body["selection"] = map[string]any{"all": true, "asset_ids": []string{}, "album_ids": []string{}, "years": []any{}}
				}
				if endpoint == "/api/v1/operator/checkouts" {
					body["root"] = root
					body["max_bytes"] = 100
				}
				encoded, err := json.Marshal(body)
				check.NoError(err)
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
					record.Endpoint().BaseURL()+endpoint,
					bytes.NewReader(encoded))
				check.NoError(err)
				request.Header.Set("Content-Type", "application/json")
				if denied.token != "" {
					request.Header.Set("Authorization", "Bearer "+denied.token)
				}
				response, err := record.Endpoint().HTTPClient(daemon.HTTPClientOptions{Timeout: 5 * time.Second, DisableKeepAlives: true}).Do(request)
				check.NoError(err)
				defer response.Body.Close()
				check.Equal(denied.status, response.StatusCode)
			}
		})
	}

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "commit", "--config", cfgPath, checkoutID, "--json",
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	r.JSONEq(fmt.Sprintf(`{"checkout_id":%q,"pending":1,"committed":1,"conflicts":0}`, checkoutID), stdout.String())
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "commit", "--config", cfgPath, checkoutID,
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	r.Equal("pending=0\tcommitted=0\tconflicts=0\n", stdout.String())
	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "commit", "--config", cfgPath, "missing", "--json",
	}, &stdout, &stderr)
	r.NotZero(code)
	var failure struct {
		CheckoutID string `json:"checkout_id"`
		Error      string `json:"error"`
	}
	r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
	r.Equal("missing", failure.CheckoutID)
	r.Contains(failure.Error, "not found")
	database, err = db.Open(dbPath)
	r.NoError(err)
	updated, err := media.NewRepo(database.WriteDB(), database.ReadDB()).GetByID(t.Context(), item.ID)
	r.NoError(err)
	r.NotEqual(item.CurrentVersionID, updated.CurrentVersionID)
	r.NoError(database.Close())
}

func TestCheckoutCommandsDoNotOpenStorageWhenLaunchFails(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "new.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	for _, args := range [][]string{
		{"checkout", "list"},
		{"checkout", "status", "missing"},
		{"checkout", "commit", "missing"},
		{"checkout", "estimate", "--all"},
		{"checkout", "create", filepath.Join(tmp, "working"), "--all", "--max-bytes", "100"},
	} {
		var stdout, stderr bytes.Buffer
		code := cli.RunContext(t.Context(), append(args, "--config", cfgPath, "--json"), &stdout, &stderr)
		r.NotZero(code)
		r.Contains(stderr.String(), "build fotobank")
		var failure struct {
			Error string `json:"error"`
		}
		r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
		r.NotEmpty(failure.Error)
	}
	_, err := os.Stat(dbPath)
	r.ErrorIs(err, os.ErrNotExist)
}

func TestOperatorCommandsRejectAmbiguousWindowsPaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path forms")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	t.Setenv("FOTOBANK_DB_PATH", filepath.Join(tmp, "new.sqlite"))
	for _, root := range []string{`\photos`, `/photos`, `D:photos`} {
		for _, args := range [][]string{
			{"checkout", "create", root, "--all", "--max-bytes", "100"},
			{"backup", "create", "--repo", root},
		} {
			var stdout, stderr bytes.Buffer
			code := cli.RunContext(t.Context(), append(args, "--config", cfgPath, "--json"), &stdout, &stderr)
			r.NotZero(code)
			var failure struct {
				Error string `json:"error"`
			}
			r.NoError(json.Unmarshal(stdout.Bytes(), &failure))
			r.Contains(failure.Error, "fully qualified")
		}
	}
}

func TestCheckoutCreateRecoversInterruptedCheckoutAfterTakingLock(t *testing.T) {
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath := writeBasicConfig(t, tmp)
	dbPath := filepath.Join(tmp, "fotobank.sqlite")
	t.Setenv("FOTOBANK_DB_PATH", dbPath)
	database, err := db.Open(dbPath)
	r.NoError(err)
	owner := owners.Principal{Hub: "h", UserID: "u"}
	_, err = database.WriteDB().ExecContext(t.Context(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES (?, ?, ?, ?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC())
	r.NoError(err)
	contentStore, err := content.Open(t.Context(), content.Config{
		Root: filepath.Join(tmp, "flash", "docbank"),
	})
	r.NoError(err)
	item := assetfixture.InsertContent(t,
		media.NewRepo(database.WriteDB(), database.ReadDB()), contentStore,
		[]byte("checkout bytes"), media.Media{Owner: owner, OriginalFilename: "IMG_0100.JPG"})
	root := filepath.Join(tmp, "checkout")
	r.NoError(os.Mkdir(root, 0o700))
	validatedRoot, err := contentStore.ResolveCheckoutRoot(root)
	r.NoError(err)
	canonicalRoot := validatedRoot.Path()
	r.NoError(validatedRoot.Close())
	r.NoError(contentStore.Close())
	interruptedID := uuid.NewString()
	now := time.Now().UTC()
	r.NoError(checkout.NewRepo(database.WriteDB(), database.ReadDB()).Insert(t.Context(), checkout.Checkout{
		ID: interruptedID, Owner: owner, Root: canonicalRoot, Layout: "capture_date",
		Selection: checkout.Selection{AssetIDs: []string{item.ID}}, State: checkout.StateBuilding,
		CreatedAt: now, UpdatedAt: now,
	}))
	r.NoError(database.Close())

	startCheckoutServer(t, cfgPath, dbPath)
	holder := flock.New(dbPath + ".checkout.lock")
	locked, err := holder.TryLock()
	r.NoError(err)
	r.True(locked)
	t.Cleanup(func() { _ = holder.Unlock() })
	var stdout, stderr bytes.Buffer
	code := cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.NotZero(code)
	r.Contains(stderr.String(), "another checkout creation is in progress")
	database, err = db.Open(dbPath)
	r.NoError(err)
	var state string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state FROM checkouts WHERE id = ?`, interruptedID).Scan(&state))
	r.Equal(string(checkout.StateBuilding), state)
	r.NoError(database.Close())
	r.NoError(holder.Unlock())

	stdout.Reset()
	stderr.Reset()
	code = cli.RunContext(t.Context(), []string{
		"checkout", "create", "--config", cfgPath, "--asset", item.ID, root,
	}, &stdout, &stderr)
	r.Zero(code, "stderr=%s", stderr.String())
	database, err = db.Open(dbPath)
	r.NoError(err)
	t.Cleanup(func() { r.NoError(database.Close()) })
	var lastError string
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT state, last_error FROM checkouts WHERE id = ?`, interruptedID).
		Scan(&state, &lastError))
	r.Equal(string(checkout.StateError), state)
	r.Contains(lastError, "interrupted")
	var active int
	r.NoError(database.ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM checkouts WHERE root = ? AND state = 'active'`, canonicalRoot).Scan(&active))
	r.Equal(1, active)
}

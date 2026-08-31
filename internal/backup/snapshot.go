package backup

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.kenn.io/fotobank/internal/db"
)

// Snapshot writes a defragmented self-contained copy of db's contents to
// dst via VACUUM INTO. The destination must not already exist; collision
// is a hard error to prevent silent overwrite of a prior snapshot.
//
// Sequence:
//  1. mkdir -p parent of dst.
//  2. tmp := dst + ".partial". VACUUM INTO refuses to overwrite, so a
//     stale .partial from a prior crash surfaces here as a clear error;
//     retention sweep deletes .partial files older than 24h.
//  3. fsync the partial file.
//  4. os.Link(tmp, dst): atomic create-if-not-exists. Same filesystem
//     because tmp lives in dst's directory.
//  5. os.Remove(tmp): drop the link source.
//  6. syncDir on dst's parent for durable rename across power loss.
//
// The DB pool must be operating outside any active transaction; VACUUM
// INTO fails inside one.
func Snapshot(ctx context.Context, db *sql.DB, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return fmt.Errorf("mkdir snapshot dir: %w", err)
	}
	tmp := dst + ".partial"

	if _, err := db.ExecContext(ctx, "VACUUM INTO "+sqlQuoteLiteral(tmp)); err != nil {
		return fmt.Errorf("VACUUM INTO %s: %w", tmp, err)
	}

	if err := fsyncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("fsync partial: %w", err)
	}

	if err := os.Link(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("link snapshot to dst: %w", err)
	}
	if err := os.Remove(tmp); err != nil {
		return fmt.Errorf("remove partial after link: %w", err)
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return fmt.Errorf("fsync snapshot dir: %w", err)
	}
	return nil
}

// SnapshotToRoot creates the SQLite snapshot in a private local staging
// directory, then atomically publishes it through root. SQLite VACUUM INTO
// accepts only a pathname, so staging is required to keep NAS publication
// bound to an already-open external root.
func SnapshotToRoot(ctx context.Context, db *sql.DB, root *os.Root, relativeDst string) error {
	if root == nil || !filepath.IsLocal(relativeDst) || relativeDst == "." {
		return fmt.Errorf("snapshot destination must be local to an open root")
	}
	directory := filepath.Dir(relativeDst)
	if err := root.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("mkdir rooted snapshot dir: %w", err)
	}
	scratch, err := os.MkdirTemp("", "fotobank-snapshot-")
	if err != nil {
		return fmt.Errorf("create snapshot staging dir: %w", err)
	}
	defer os.RemoveAll(scratch)
	staged := filepath.Join(scratch, "snapshot.sqlite")
	if err := Snapshot(ctx, db, staged); err != nil {
		return fmt.Errorf("stage rooted snapshot: %w", err)
	}
	source, err := os.Open(staged)
	if err != nil {
		return fmt.Errorf("open staged snapshot: %w", err)
	}
	defer source.Close()
	partial := relativeDst + ".partial"
	destination, err := root.OpenFile(partial, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("open rooted snapshot partial: %w", err)
	}
	if _, err := copySnapshotContext(ctx, destination, source); err != nil {
		_ = destination.Close()
		_ = root.Remove(partial)
		return fmt.Errorf("copy rooted snapshot partial: %w", err)
	}
	if err := destination.Sync(); err != nil {
		_ = destination.Close()
		_ = root.Remove(partial)
		return fmt.Errorf("sync rooted snapshot partial: %w", err)
	}
	if err := destination.Close(); err != nil {
		_ = root.Remove(partial)
		return fmt.Errorf("close rooted snapshot partial: %w", err)
	}
	if err := root.Link(partial, relativeDst); err != nil {
		_ = root.Remove(partial)
		return fmt.Errorf("link rooted snapshot to destination: %w", err)
	}
	if err := root.Remove(partial); err != nil {
		return fmt.Errorf("remove rooted snapshot partial: %w", err)
	}
	if err := syncRootDir(root, directory); err != nil {
		return fmt.Errorf("fsync rooted snapshot dir: %w", err)
	}
	return nil
}

func copySnapshotContext(
	ctx context.Context,
	destination io.WriteCloser,
	source io.ReadCloser,
) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() {
		defer close(cancelDone)
		_ = source.Close()
		_ = destination.Close()
	})
	copied, err := io.Copy(destination, snapshotContextReader{ctx: ctx, reader: source})
	if !stopCancel() {
		<-cancelDone
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return copied, ctxErr
	}
	return copied, err
}

type snapshotContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r snapshotContextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

// SnapshotPath opens its own writable SQLite connection at srcDB
// (without running migrations) and runs Snapshot. Used by the CLI when
// the caller has only a path, not an existing pool.
func SnapshotPath(ctx context.Context, srcDB, dst string) error {
	conn, err := openSnapshotSource(srcDB)
	if err != nil {
		return err
	}
	defer conn.Close()
	return Snapshot(ctx, conn, dst)
}

// SnapshotPathToRoot opens srcDB without migrations and publishes its snapshot
// through an already-open filesystem root.
func SnapshotPathToRoot(
	ctx context.Context,
	srcDB string,
	root *os.Root,
	relativeDst string,
) error {
	conn, err := openSnapshotSource(srcDB)
	if err != nil {
		return err
	}
	defer conn.Close()
	return SnapshotToRoot(ctx, conn, root, relativeDst)
}

func openSnapshotSource(srcDB string) (*sql.DB, error) {
	// The source DB may carry vec0 schema objects; register the
	// sqlite-vec extension before opening so SQLite can resolve
	// vec0 virtual tables when CLI tools drive snapshots without
	// going through db.Open first.
	db.RegisterSqliteVec()
	if _, err := os.Stat(srcDB); err != nil {
		return nil, fmt.Errorf("stat source DB: %w", err)
	}
	dsn := buildDSN(srcDB)
	conn, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("open source DB: %w", err)
	}
	return conn, nil
}

// buildDSN returns a writable file: URI for path with the project's
// standard pragmas. Any reserved characters in path are percent-escaped
// via net/url so paths containing '?', '#', or whitespace are safe.
// Relative paths are resolved to absolute so url.URL.String() does not
// emit them as authority components (file://rel.sqlite is malformed).
//
// mode=rw is set so SQLite refuses to create an empty database when the
// source file is missing. SnapshotPath stats the source first, but
// without mode=rw a TOCTOU window between stat and open could otherwise
// silently produce an "ok" snapshot of a freshly-created empty DB.
func buildDSN(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	uriPath := filepath.ToSlash(abs)
	if filepath.VolumeName(abs) != "" && !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	u := url.URL{Scheme: "file", Path: uriPath}
	q := u.Query()
	q.Set("mode", "rw")
	q.Add("_busy_timeout", "5000")
	q.Add("_fk", "1")
	u.RawQuery = q.Encode()
	return u.String()
}

func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

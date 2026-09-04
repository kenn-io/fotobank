package content

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const sqliteHeader = "SQLite format 3\x00"

// InspectVault checks that root contains an initialized Docbank vault without
// opening the vault lifecycle or changing its contents.
func InspectVault(path string) (retErr error) {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, root.Close()) }()

	info, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("vault root is not a directory")
	}
	blobs, err := root.Stat("blobs")
	if err != nil {
		return fmt.Errorf("inspect blobs directory: %w", err)
	}
	if !blobs.IsDir() {
		return errors.New("blobs is not a directory")
	}
	catalogInfo, err := root.Stat("docbank.db")
	if err != nil {
		return fmt.Errorf("inspect catalog: %w", err)
	}
	if !catalogInfo.Mode().IsRegular() {
		return errors.New("catalog is not a regular file")
	}
	catalog, err := root.Open("docbank.db")
	if err != nil {
		return fmt.Errorf("open catalog: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, catalog.Close()) }()
	header := make([]byte, len(sqliteHeader))
	if _, err := io.ReadFull(catalog, header); err != nil {
		return fmt.Errorf("read catalog SQLite header: %w", err)
	}
	if string(header) != sqliteHeader {
		return errors.New("catalog is not a SQLite database")
	}
	return nil
}

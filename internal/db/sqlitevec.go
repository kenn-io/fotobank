package db

import (
	"sync"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

// registerOnce guards sqlite_vec.Auto so the extension is registered
// exactly once per process. Calling Auto twice would re-register and
// return an error from sqlite3 on the second registration; the
// sync.Once turns extra calls into no-ops without surfacing the error
// to callers.
var registerOnce sync.Once

// RegisterSqliteVec registers the sqlite-vec extension as an
// auto-extension on the mattn/go-sqlite3 driver. After this call,
// every subsequent sql.Open("sqlite3", …) loads sqlite-vec into the
// new connection.
//
// MUST be called before the first sql.Open in a process. Calling it
// after some connections have been opened would not retroactively
// register the extension on those connections.
func RegisterSqliteVec() {
	registerOnce.Do(sqlite_vec.Auto)
}

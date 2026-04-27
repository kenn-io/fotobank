# Fotobank Web — F1: Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the Vite + Svelte 5 SPA embedded in the fotobank Go binary, with the three-column shell, Library + Sessions browse modes, justified-row virtualized grid, theme system, identity stub, SSE skeleton, user-settings persistence, and the dev/test/build pipeline that all later sub-plans (F2–F6) build on top of.

**Architecture:** Single-binary deploy; frontend lives in `frontend/`, builds to `frontend/dist/`, and is copied into `internal/web/dist/` which is `//go:embed`-ed by `internal/web/embed.go`. The same `http.ServeMux` serves `/api/v1/*` (existing huma routes) and `/*` (the SPA). Air drives backend live-reload; Vite drives frontend live-reload via an `/api`-proxied dev server. Per-user UI prefs persist via a new `user_settings` table reached through a small `GET|PUT /api/v1/settings/user/{key}` API. SSE at `/api/v1/events` is wired with a single `hello` event in F1; later sub-plans add domain events.

**Tech Stack:** Go 1.26 backend (existing); Svelte 5 + Vite 8 + TypeScript + Bun frontend; pattern reference `~/code/middleman` for build wiring; `~/code/agentsview` for layout and theme variables. Tests: existing testify on the Go side; Vitest + Playwright on the frontend. E2E uses a `cmd/e2e-server` binary backed by a **temp-file SQLite DB** (not `:memory:` — `internal/db.Open` opens RW/RO pools and the RO pool uses `mode=ro`, which won't work in-memory).

---

## Schema policy reminder

This plan edits `internal/db/migrations/000001_initial_schema.{up,down}.sql` directly per the spec's §12.1 pre-prod policy. No new migration files until first prod deployment. The pre-commit hook `prevent edits to main-branch migrations` is a safeguard against post-deployment edits — for now we are still in pre-prod and the squash-into-000001 path is intentional. If the hook blocks, the operator should temporarily bypass with explicit acknowledgement; do **not** add `--no-verify` reflexively.

---

## Section A — Schema and backend foundation

### Task 1: Add `user_settings` table to initial schema

**Files:**
- Modify: `internal/db/migrations/000001_initial_schema.up.sql`
- Modify: `internal/db/migrations/000001_initial_schema.down.sql`
- Create: `internal/service/usersettings/repo.go`
- Create: `internal/service/usersettings/repo_test.go`

- [ ] **Step 1: Write the failing test for repo upsert/get/delete**

```go
// internal/service/usersettings/repo_test.go
package usersettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestRepoUpsertAndGet(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(db)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "theme", `"dark"`))

	val, ok, err := repo.Get(context.Background(), p, "theme")
	r.NoError(err)
	r.True(ok)
	r.JSONEq(`"dark"`, val)
}

func TestRepoGetMissing(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(db)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	_, ok, err := repo.Get(context.Background(), p, "missing")
	r.NoError(err)
	r.False(ok)
}

func TestRepoUpsertReplaces(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(db)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "density.library", `"comfortable"`))
	r.NoError(repo.Upsert(context.Background(), p, "density.library", `"compact"`))

	val, _, _ := repo.Get(context.Background(), p, "density.library")
	r.JSONEq(`"compact"`, val)
}

func TestRepoDelete(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	repo := usersettings.NewRepo(db)
	p := owners.Principal{Hub: "local", UserID: "alice"}

	r.NoError(repo.Upsert(context.Background(), p, "theme", `"dark"`))
	r.NoError(repo.Delete(context.Background(), p, "theme"))

	_, ok, _ := repo.Get(context.Background(), p, "theme")
	r.False(ok)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/service/usersettings/...`
Expected: FAIL with build error (`usersettings.Repo` undefined / migration missing).

- [ ] **Step 3: Add the table to `000001_initial_schema.up.sql`**

Append below the existing `albums` / `album_media` block, before any later content:

```sql
-- Per-user, non-secret UI preferences.
-- Keys are dotted strings (e.g. "theme", "density.library"); values are JSON.
CREATE TABLE user_settings (
    principal_hub        TEXT NOT NULL,
    principal_user_id    TEXT NOT NULL,
    key                  TEXT NOT NULL,
    value                TEXT NOT NULL,
    updated_at           TIMESTAMP NOT NULL,
    PRIMARY KEY (principal_hub, principal_user_id, key)
);
```

Mirror the deletion in `000001_initial_schema.down.sql`: since the down file is a complete pre-change schema snapshot, the table is simply absent. No `DROP TABLE` needed in the down file beyond what was already there.

- [ ] **Step 4: Implement the repo**

```go
// internal/service/usersettings/repo.go
package usersettings

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/wesm/fotobank/internal/owners"
)

// Repo persists per-principal UI preferences. Values are opaque JSON
// strings; the caller is responsible for marshalling/unmarshalling.
type Repo struct {
	db *sqlx.DB
}

func NewRepo(db *sqlx.DB) *Repo { return &Repo{db: db} }

// Upsert writes or replaces (principal, key) -> value.
func (r *Repo) Upsert(ctx context.Context, p owners.Principal, key, valueJSON string) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO user_settings (principal_hub, principal_user_id, key, value, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (principal_hub, principal_user_id, key)
		DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at
	`, p.Hub, p.UserID, key, valueJSON, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("user_settings upsert: %w", err)
	}
	return nil
}

// Get returns (value, true) when the row exists, otherwise ("", false).
func (r *Repo) Get(ctx context.Context, p owners.Principal, key string) (string, bool, error) {
	var v string
	err := r.db.GetContext(ctx, &v, `
		SELECT value FROM user_settings
		WHERE principal_hub = ? AND principal_user_id = ? AND key = ?
	`, p.Hub, p.UserID, key)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("user_settings get: %w", err)
	}
	return v, true, nil
}

// Delete removes (principal, key). Missing keys are not an error.
func (r *Repo) Delete(ctx context.Context, p owners.Principal, key string) error {
	_, err := r.db.ExecContext(ctx, `
		DELETE FROM user_settings
		WHERE principal_hub = ? AND principal_user_id = ? AND key = ?
	`, p.Hub, p.UserID, key)
	if err != nil {
		return fmt.Errorf("user_settings delete: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Run tests and commit**

Run: `go test ./internal/service/usersettings/... -v`
Expected: PASS (4 tests).

```bash
git add internal/db/migrations/000001_initial_schema.up.sql \
        internal/db/migrations/000001_initial_schema.down.sql \
        internal/service/usersettings/
git commit -m "feat(web/f1): add user_settings table and repo"
```

---

### Task 2: Add `usersettings.Service` with caller scoping

**Files:**
- Create: `internal/service/usersettings/service.go`
- Create: `internal/service/usersettings/service_test.go`

- [ ] **Step 1: Write the failing service test**

```go
// internal/service/usersettings/service_test.go
package usersettings_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestServiceScopesByCaller(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	svc := usersettings.NewService(usersettings.NewRepo(db))
	alice := owners.Principal{Hub: "local", UserID: "alice"}
	bob := owners.Principal{Hub: "local", UserID: "bob"}

	r.NoError(svc.Set(context.Background(), alice, "theme", `"dark"`))
	r.NoError(svc.Set(context.Background(), bob, "theme", `"light"`))

	a, _, _ := svc.Get(context.Background(), alice, "theme")
	b, _, _ := svc.Get(context.Background(), bob, "theme")
	r.JSONEq(`"dark"`, a)
	r.JSONEq(`"light"`, b)
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/service/usersettings/... -run TestServiceScopesByCaller`
Expected: FAIL (NewService undefined).

- [ ] **Step 3: Implement the service**

```go
// internal/service/usersettings/service.go
package usersettings

import (
	"context"

	"github.com/wesm/fotobank/internal/owners"
)

// Service is a thin caller-scoped wrapper around Repo. Every method
// takes the caller principal and forwards to the repo as that principal.
type Service struct {
	repo *Repo
}

func NewService(repo *Repo) *Service { return &Service{repo: repo} }

func (s *Service) Set(ctx context.Context, caller owners.Principal, key, valueJSON string) error {
	return s.repo.Upsert(ctx, caller, key, valueJSON)
}

func (s *Service) Get(ctx context.Context, caller owners.Principal, key string) (string, bool, error) {
	return s.repo.Get(ctx, caller, key)
}

func (s *Service) Delete(ctx context.Context, caller owners.Principal, key string) error {
	return s.repo.Delete(ctx, caller, key)
}
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/service/usersettings/...`
Expected: PASS.

```bash
git add internal/service/usersettings/service.go internal/service/usersettings/service_test.go
git commit -m "feat(web/f1): add usersettings.Service caller-scoped wrapper"
```

---

### Task 3: Add `GET|PUT|DELETE /api/v1/settings/user/{key}` huma handlers

**Files:**
- Create: `internal/httpapi/user_settings.go`
- Create: `internal/httpapi/user_settings_test.go`
- Modify: `internal/httpapi/api.go` (register the new routes)
- Modify: `internal/httpapi/api.go` Deps struct (add `*usersettings.Service`)

- [ ] **Step 1: Write the failing handler test**

```go
// internal/httpapi/user_settings_test.go
package httpapi_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/service/usersettings"
	"github.com/wesm/fotobank/internal/testutil"
)

func TestUserSettingsRoundtrip(t *testing.T) {
	r := require.New(t)
	db := testutil.OpenTestDB(t)
	svc := usersettings.NewService(usersettings.NewRepo(db))
	prov := identity.NewStubProvider(owners.Principal{Hub: "local", UserID: "alice"}, "Alice")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: prov,
		UserSettings:     svc,
	})
	r.NoError(err)

	put := httptest.NewRequest(http.MethodPut, "/api/v1/settings/user/theme",
		bytes.NewBufferString(`{"value":"\"dark\""}`))
	put.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, put)
	r.Equal(http.StatusNoContent, rr.Code)

	get := httptest.NewRequest(http.MethodGet, "/api/v1/settings/user/theme", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, get)
	r.Equal(http.StatusOK, rr.Code)
	r.JSONEq(`{"value":"\"dark\""}`, rr.Body.String())

	get404 := httptest.NewRequest(http.MethodGet, "/api/v1/settings/user/missing", nil)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, get404)
	r.Equal(http.StatusNotFound, rr.Code)
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/httpapi/... -run TestUserSettingsRoundtrip`
Expected: FAIL (Deps.UserSettings undefined; route unregistered).

- [ ] **Step 3: Implement the handler and register routes**

```go
// internal/httpapi/user_settings.go
package httpapi

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/wesm/fotobank/internal/errs"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/service/usersettings"
)

type userSettingValue struct {
	Body struct {
		Value string `json:"value"`
	}
}

type userSettingPathInput struct {
	Key string `path:"key" maxLength:"128" pattern:"^[A-Za-z0-9_.-]+$"`
}

type userSettingPutInput struct {
	userSettingPathInput
	Body struct {
		Value string `json:"value"`
	}
}

func registerUserSettings(api huma.API, svc *usersettings.Service) {
	if svc == nil {
		return
	}
	huma.Register(api, huma.Operation{
		OperationID: "getUserSetting",
		Method:      http.MethodGet,
		Path:        "/api/v1/settings/user/{key}",
		Summary:     "Get a per-user UI preference",
	}, func(ctx context.Context, in *userSettingPathInput) (*userSettingValue, error) {
		caller, err := identity.RequireCaller(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		v, ok, err := svc.Get(ctx, caller, in.Key)
		if err != nil {
			return nil, Translate(err)
		}
		if !ok {
			return nil, Translate(errs.ErrNotFound)
		}
		out := &userSettingValue{}
		out.Body.Value = v
		return out, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "putUserSetting",
		Method:        http.MethodPut,
		Path:          "/api/v1/settings/user/{key}",
		Summary:       "Set a per-user UI preference",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *userSettingPutInput) (*struct{}, error) {
		caller, err := identity.RequireCaller(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.Set(ctx, caller, in.Key, in.Body.Value); err != nil {
			return nil, Translate(err)
		}
		return nil, nil
	})

	huma.Register(api, huma.Operation{
		OperationID:   "deleteUserSetting",
		Method:        http.MethodDelete,
		Path:          "/api/v1/settings/user/{key}",
		Summary:       "Delete a per-user UI preference",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, in *userSettingPathInput) (*struct{}, error) {
		caller, err := identity.RequireCaller(ctx)
		if err != nil {
			return nil, Translate(err)
		}
		if err := svc.Delete(ctx, caller, in.Key); err != nil {
			return nil, Translate(err)
		}
		return nil, nil
	})
}
```

In `internal/httpapi/api.go`, add to `Deps`:

```go
	// UserSettings backs /api/v1/settings/user/{key}. Nil means those
	// routes aren't registered; the OpenAPI dumper passes nil.
	UserSettings *usersettings.Service
```

And in `buildAPI`, add `registerUserSettings(api, deps.UserSettings)` alongside the other `register*` calls.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/httpapi/... -run TestUserSettingsRoundtrip -v`
Expected: PASS.

```bash
git add internal/httpapi/user_settings.go internal/httpapi/user_settings_test.go internal/httpapi/api.go
git commit -m "feat(web/f1): add user-settings huma routes"
```

---

### Task 4: Add SSE skeleton at `GET /api/v1/events`

**Files:**
- Create: `internal/httpapi/events.go`
- Create: `internal/httpapi/events_test.go`
- Modify: `internal/httpapi/api.go` (register raw mux route, since SSE is not a huma JSON route)

- [ ] **Step 1: Write the failing handler test**

```go
// internal/httpapi/events_test.go
package httpapi_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

func TestEventsHelloOnConnect(t *testing.T) {
	r := require.New(t)
	prov := identity.NewStubProvider(owners.Principal{Hub: "local", UserID: "alice"}, "Alice")
	h, err := httpapi.New(httpapi.Deps{IdentityProvider: prov, EventBus: httpapi.NewEventBus()})
	r.NoError(err)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/events", nil)
	resp, err := http.DefaultClient.Do(req)
	r.NoError(err)
	defer resp.Body.Close()
	r.Equal(http.StatusOK, resp.StatusCode)
	r.Equal("text/event-stream", resp.Header.Get("Content-Type"))

	br := bufio.NewReader(resp.Body)
	var sawHello, sawData bool
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && !(sawHello && sawData) {
		line, err := br.ReadString('\n')
		if err != nil {
			break
		}
		line = strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(line, "event: hello") {
			sawHello = true
		}
		if strings.HasPrefix(line, "data: ") && sawHello {
			sawData = true
		}
	}
	r.True(sawHello, "expected hello event")
	r.True(sawData, "expected data line after hello")
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/httpapi/... -run TestEventsHelloOnConnect`
Expected: FAIL (`EventBus` undefined; route unregistered).

- [ ] **Step 3: Implement the SSE handler and ring-buffered EventBus**

```go
// internal/httpapi/events.go
package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"

	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
)

// EventBus fans events out to per-principal subscribers and keeps a
// small ring buffer per principal so reconnecting clients can resume
// via Last-Event-ID. F1 emits only the synthetic "hello" event on
// connect; later sub-plans (F3 in particular) publish domain events
// such as ai.tag.completed and import.progress through Publish.
type EventBus struct {
	mu     sync.Mutex
	bufs   map[owners.Principal]*ring
	bufLen int
}

type Event struct {
	ID    int64           `json:"id"`
	Type  string          `json:"type"`
	Data  json.RawMessage `json:"data"`
}

type ring struct {
	events []Event
	next   int
	subs   []chan Event
}

func NewEventBus() *EventBus { return &EventBus{bufs: map[owners.Principal]*ring{}, bufLen: 256} }

// Publish appends an event to the per-principal buffer and pushes it to
// every active subscriber. Drops to slow subscribers (full chan) are
// silently swallowed; subscribers see catchup-required on reconnect.
func (b *EventBus) Publish(p owners.Principal, ev Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	if len(r.events) < b.bufLen {
		r.events = append(r.events, ev)
	} else {
		r.events[r.next] = ev
		r.next = (r.next + 1) % b.bufLen
	}
	for _, ch := range r.subs {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (b *EventBus) subscribe(p owners.Principal) (chan Event, func()) {
	ch := make(chan Event, 16)
	b.mu.Lock()
	r := b.bufs[p]
	if r == nil {
		r = &ring{events: make([]Event, 0, b.bufLen)}
		b.bufs[p] = r
	}
	r.subs = append(r.subs, ch)
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		r := b.bufs[p]
		if r == nil {
			return
		}
		for i, c := range r.subs {
			if c == ch {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				close(ch)
				return
			}
		}
	}
}

func (b *EventBus) replayAfter(p owners.Principal, lastID int64) []Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.bufs[p]
	if r == nil {
		return nil
	}
	out := make([]Event, 0, len(r.events))
	for _, ev := range r.events {
		if ev.ID > lastID {
			out = append(out, ev)
		}
	}
	return out
}

func eventsHandler(bus *EventBus, prov identity.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		caller, err := identity.RequireCaller(r.Context())
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Hello event: snapshot of state the SPA needs at boot.
		hello, _ := json.Marshal(map[string]any{"principal": caller.UserID})
		writeSSE(w, Event{ID: 0, Type: "hello", Data: hello})
		flusher.Flush()

		// Replay buffered events after Last-Event-ID, if any.
		var lastID int64
		if v := r.Header.Get("Last-Event-ID"); v != "" {
			if n, perr := strconv.ParseInt(v, 10, 64); perr == nil {
				lastID = n
			}
		}
		if lastID > 0 {
			missed := bus.replayAfter(caller, lastID)
			if len(missed) == 0 {
				// Ring-buffer miss: client must refetch via REST.
				writeSSE(w, Event{ID: lastID, Type: "catchup-required", Data: json.RawMessage(`{}`)})
				flusher.Flush()
			} else {
				for _, ev := range missed {
					writeSSE(w, ev)
				}
				flusher.Flush()
			}
		}

		ch, unsub := bus.subscribe(caller)
		defer unsub()
		for {
			select {
			case <-r.Context().Done():
				return
			case ev, open := <-ch:
				if !open {
					return
				}
				writeSSE(w, ev)
				flusher.Flush()
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, ev Event) {
	fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.ID, ev.Type, string(ev.Data))
}
```

In `internal/httpapi/api.go`:
- Add `EventBus *EventBus` to `Deps`.
- In `buildAPI`, after the huma routes, register the SSE route via the raw mux: `mux.Handle("/api/v1/events", eventsHandler(deps.EventBus, deps.IdentityProvider))`.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/httpapi/... -run TestEventsHelloOnConnect -v`
Expected: PASS.

```bash
git add internal/httpapi/events.go internal/httpapi/events_test.go internal/httpapi/api.go
git commit -m "feat(web/f1): add SSE skeleton with ring-buffered EventBus"
```

---

## Section B — Frontend scaffold and embed

### Task 5: Scaffold `frontend/` with Vite + Svelte 5 + Bun

**Files:**
- Create: `frontend/package.json`
- Create: `frontend/tsconfig.json`
- Create: `frontend/vite.config.ts`
- Create: `frontend/index.html`
- Create: `frontend/src/main.ts`
- Create: `frontend/src/App.svelte`
- Create: `frontend/src/app.css`
- Create: `frontend/.gitignore`

- [ ] **Step 1: Write `frontend/package.json`**

Pin the same versions middleman is currently on so we drift together. Confirm with `cat ~/code/middleman/frontend/package.json` for current pins; this template uses representative values that the implementer must verify.

```json
{
  "name": "fotobank-frontend",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "packageManager": "bun@1.3.11",
  "scripts": {
    "dev": "vite",
    "build": "vite build --logLevel warn",
    "preview": "vite preview",
    "check": "svelte-check --tsconfig ./tsconfig.json --fail-on-warnings",
    "lint": "eslint .",
    "lint:fix": "eslint . --fix",
    "typecheck": "bun run check && bunx tsc --noEmit -p ./tsconfig.json",
    "test": "vitest run",
    "test:e2e": "playwright test --config=playwright-e2e.config.ts"
  },
  "dependencies": {
    "openapi-fetch": "^0.17.0"
  },
  "devDependencies": {
    "@playwright/test": "1.55.1",
    "@sveltejs/vite-plugin-svelte": "7.0.0",
    "@testing-library/svelte": "5.2.8",
    "@tsconfig/svelte": "5.0.8",
    "@types/node": "^24.9.2",
    "eslint": "^10.1.0",
    "eslint-config-prettier": "^10.1.8",
    "eslint-plugin-svelte": "^3.16.0",
    "globals": "^17.4.0",
    "jsdom": "26.1.0",
    "openapi-typescript": "^7.13.0",
    "playwright": "1.55.1",
    "prettier": "^3.8.1",
    "svelte": "5.55.0",
    "svelte-check": "4.4.5",
    "typescript": "5.9.3",
    "typescript-eslint": "^8.58.0",
    "vite": "8.0.3",
    "vitest": "3.2.4"
  }
}
```

- [ ] **Step 2: Write `frontend/tsconfig.json`**

```json
{
  "extends": "@tsconfig/svelte/tsconfig.json",
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "strict": true,
    "noUncheckedIndexedAccess": true,
    "exactOptionalPropertyTypes": true,
    "noImplicitOverride": true,
    "verbatimModuleSyntax": true,
    "isolatedModules": true,
    "allowImportingTsExtensions": true,
    "noEmit": true,
    "types": ["svelte", "vite/client", "node", "vitest/globals"]
  },
  "include": ["src/**/*", "tests/**/*"]
}
```

- [ ] **Step 3: Write `frontend/vite.config.ts`**

```ts
import { svelte } from "@sveltejs/vite-plugin-svelte";
import type { UserConfig } from "vite";
import type { InlineConfig } from "vitest/node";

const apiUrl = process.env.FOTOBANK_DEV_API_URL ?? "http://127.0.0.1:8080";

const config = {
  base: "/",
  plugins: [svelte()],
  server: {
    host: "127.0.0.1",
    port: 5181,
    proxy: {
      "/api": { target: apiUrl, changeOrigin: true, ws: true },
    },
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.{test,spec}.?(c|m)[jt]s?(x)"],
    exclude: ["tests/e2e/**", "node_modules/**"],
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    chunkSizeWarningLimit: 1500,
  },
} satisfies UserConfig & { test: InlineConfig };

export default config;
```

- [ ] **Step 4: Write `frontend/index.html`, `src/main.ts`, `src/App.svelte`, `src/app.css`, `src/test/setup.ts`**

```html
<!-- frontend/index.html -->
<!doctype html>
<html lang="en">
  <head>
    <meta charset="UTF-8" />
    <meta name="viewport" content="width=device-width,initial-scale=1" />
    <title>Fotobank</title>
    <link rel="stylesheet" href="/src/app.css" />
  </head>
  <body>
    <div id="app"></div>
    <script type="module" src="/src/main.ts"></script>
  </body>
</html>
```

```ts
// frontend/src/main.ts
import { mount } from "svelte";
import App from "./App.svelte";
import "./app.css";

const target = document.getElementById("app");
if (!target) throw new Error("missing #app root");
mount(App, { target });
```

```svelte
<!-- frontend/src/App.svelte -->
<script lang="ts">
  // Replaced in Task 13 with ThreeColumnLayout.
  let booted = $state(true);
</script>

<main>
  <h1>Fotobank</h1>
  <p>Frontend boot OK ({booted ? "yes" : "no"}).</p>
</main>
```

```css
/* frontend/src/app.css */
/* Real theme variables added in Task 12. */
:root {
  --bg-primary: #ffffff;
  --text-primary: #111111;
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
}
body { margin: 0; background: var(--bg-primary); color: var(--text-primary); }
main { padding: 2rem; }
```

```ts
// frontend/src/test/setup.ts
// Vitest global setup placeholder; expanded as components grow.
```

```gitignore
# frontend/.gitignore
node_modules/
dist/
.vite/
*.log
playwright-report/
test-results/
```

- [ ] **Step 5: Smoke build and commit**

Run: `cd frontend && bun install && bun run build && cd ..`
Expected: `frontend/dist/index.html` and `assets/` exist.

```bash
git add frontend/
git commit -m "feat(web/f1): scaffold Vite + Svelte 5 frontend"
```

---

### Task 6: Add `internal/web/` embed

**Files:**
- Create: `internal/web/embed.go`
- Create: `internal/web/embed_test.go`
- Create: `internal/web/dist/stub.html`
- Create: `internal/web/dist/.gitkeep`
- Create: `internal/web/dist/.gitignore` (ignore everything except stub.html and .gitkeep)

- [ ] **Step 1: Write the failing test**

```go
// internal/web/embed_test.go
package web_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/web"
)

func TestHandlerServesIndex(t *testing.T) {
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "<html") // index.html OR stub.html
}

func TestHandlerSPAFallback(t *testing.T) {
	r := require.New(t)
	h := web.Handler()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/library", nil))
	r.Equal(http.StatusOK, rr.Code)
	r.Contains(rr.Body.String(), "<html")
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/web/...`
Expected: FAIL (package missing).

- [ ] **Step 3: Implement the embed and SPA fallback**

```go
// internal/web/embed.go
// Package web embeds the built SPA and serves it. The same handler
// also implements the SPA fallback: any GET that does not match a
// real file in dist falls back to index.html so client-side routes
// (e.g. /library, /sessions) resolve correctly on hard reload.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded SPA. It
// must be registered after all /api/v1/* routes; the SPA fallback is
// path-agnostic and would otherwise swallow API requests.
func Handler() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic("web: failed to scope embed.FS to dist: " + err.Error())
	}
	fsHandler := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Try the file; on 404, fall back to index.html for SPA routes.
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			r.URL.Path = "/"
			fsHandler.ServeHTTP(w, r)
			return
		}
		fsHandler.ServeHTTP(w, r)
	})
}
```

```html
<!-- internal/web/dist/stub.html -->
<!doctype html><html><body>fotobank web stub — run `make frontend`</body></html>
```

```gitignore
# internal/web/dist/.gitignore
*
!.gitignore
!.gitkeep
!stub.html
```

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/web/...`
Expected: PASS.

```bash
git add internal/web/
git commit -m "feat(web/f1): embed dist with SPA fallback"
```

---

### Task 7: Wire embed handler into the server mux

**Files:**
- Modify: `internal/cli/server.go`
- Modify: `internal/cli/e2e_observability_test.go` (or new test) — verify GET / returns the SPA stub

- [ ] **Step 1: Write the failing integration test**

Add a new e2e test in `internal/cli/e2e_web_test.go`:

```go
package cli_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/cli"
)

func TestE2EServerServesSPAOnRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e tests skipped under -short")
	}
	r := require.New(t)
	tmp := t.TempDir()
	cfgPath, _ := writeObsConfig(t, tmp)
	mainSink := filepath.Join(tmp, "main-addr")
	t.Setenv("FOTOBANK_TEST_LISTEN_ADDR_SINK", mainSink)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan int, 1)
	go func() {
		var so, se bytes.Buffer
		done <- cli.RunContext(ctx, []string{"server", "--config", cfgPath}, &so, &se)
	}()

	addr := waitForSink(t, mainSink)
	r.NotEmpty(addr)

	resp, err := http.Get("http://" + addr + "/")
	r.NoError(err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	r.Equal(200, resp.StatusCode)
	r.Contains(string(body), "<html")

	cancel()
	select {
	case code := <-done:
		r.Equal(0, code)
	case <-time.After(10 * time.Second):
		r.Fail("server did not exit")
	}
}
```

- [ ] **Step 2: Run to confirm fail**

Run: `go test ./internal/cli/... -run TestE2EServerServesSPAOnRoot`
Expected: FAIL (404 — embed handler not yet mounted).

- [ ] **Step 3: Mount the embed handler in `server.go`**

Locate the section of `cli/server.go` that constructs the API mux. After `mux := buildAPI(...)` (or after the existing `httpapi.New(...)` call) wrap or augment the handler so the SPA serves on non-API paths. The current pattern returns `http.Handler` from `httpapi.New`; add a new outer mux:

```go
import "github.com/wesm/fotobank/internal/web"

// ... where the api handler is built ...
apiHandler, err := httpapi.New(deps)
if err != nil { return ... }

outer := http.NewServeMux()
outer.Handle("/api/", apiHandler)
outer.Handle("/", web.Handler())

handler := http.Handler(outer)
// pass handler to http.Server{Handler: handler} as before
```

Verify: `/api/v1/healthz` still returns the JSON it always did; `/` returns the SPA stub; `/library` (a future client route) also returns the SPA stub via the fallback.

- [ ] **Step 4: Run and commit**

Run: `go test ./internal/cli/... -run TestE2EServerServesSPAOnRoot -v`
Expected: PASS. Existing observability tests still pass.

```bash
git add internal/cli/server.go internal/cli/e2e_web_test.go
git commit -m "feat(web/f1): mount SPA handler on the server mux"
```

---

### Task 8: Add `.air.toml` and dev scripts

**Files:**
- Create: `.air.toml`
- Create: `scripts/dev-backend-build.sh`
- Create: `scripts/frontend-dev.sh`

- [ ] **Step 1: Write `.air.toml`** (mirroring middleman, fotobank paths)

```toml
root = "."
tmp_dir = "tmp"

[build]
cmd = "./scripts/dev-backend-build.sh"
entrypoint = "./tmp/fotobank"
delay = 500
exclude_dir = ["frontend", "internal/web/dist", "tmp", "vendor", ".git", "docs"]
include_ext = ["go", "sql", "html"]
stop_on_error = true
send_interrupt = true
kill_delay = "2s"

[screen]
clear_on_rebuild = false
```

- [ ] **Step 2: Write `scripts/dev-backend-build.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail
mkdir -p tmp
go build -o tmp/fotobank ./cmd/fotobank
```

`chmod +x scripts/dev-backend-build.sh`.

- [ ] **Step 3: Write `scripts/frontend-dev.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../frontend"
mkdir -p ../tmp/logs
bun install
exec bun run dev "$@" 2>&1 | tee -a ../tmp/logs/frontend-dev.log
```

`chmod +x scripts/frontend-dev.sh`.

- [ ] **Step 4: Smoke and commit**

Manual smoke (developer-side, not automated): `make dev` (after Task 9 lands the make target) should rebuild on Go change; `make frontend-dev` should boot Vite at 127.0.0.1:5181 with `/api` proxy.

```bash
git add .air.toml scripts/dev-backend-build.sh scripts/frontend-dev.sh
git commit -m "feat(web/f1): air config + dev scripts"
```

---

### Task 9: Extend `Makefile` with frontend, dev, and check targets

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Add the new targets**

Append to the existing Makefile (preserve current targets):

```make
.PHONY: ensure-embed-dir frontend frontend-dev frontend-check dev air-install

# Ensure go:embed has at least one file (no-op if frontend is built).
ensure-embed-dir:
	@mkdir -p internal/web/dist
	@test -n "$$(ls internal/web/dist/ 2>/dev/null)" \
		|| echo ok > internal/web/dist/stub.html

# Build the frontend SPA into internal/web/dist for embedding.
frontend:
	cd frontend && bun install && bun run build
	rm -rf internal/web/dist
	mkdir -p internal/web/dist
	cp -r frontend/dist/* internal/web/dist/
	printf 'ok\n' > internal/web/dist/stub.html

# Run vite with /api proxy. Use alongside `make dev`.
frontend-dev:
	./scripts/frontend-dev.sh $(ARGS)

# Lint + typecheck + unit-test the frontend.
frontend-check:
	cd frontend && bun install && bun run typecheck && bun run lint && bun run test

# Install air for backend live reload.
air-install:
	go install github.com/air-verse/air@latest

# Run server with backend live reload (use alongside `make frontend-dev`).
dev: ensure-embed-dir
	@if ! command -v air >/dev/null 2>&1; then \
		echo "air not found. Install with: make air-install" >&2; exit 1; \
	fi
	air -c .air.toml -- $(ARGS)
```

Modify the existing `build` target to depend on `frontend`:

```make
build: frontend
	go build -ldflags="$(LDFLAGS)" -o bin/fotobank ./cmd/fotobank
```

(Adjust based on the actual existing build recipe — preserve `LDFLAGS`.)

Modify `test` and `test-short` to include `ensure-embed-dir` so `go test` doesn't choke on an empty embed:

```make
test: ensure-embed-dir
	go test ./... -shuffle=on

test-short: ensure-embed-dir
	go test ./... -short -shuffle=on
```

- [ ] **Step 2: Run targets to verify**

Run: `make ensure-embed-dir && make test-short`
Expected: passes (embed has stub.html; tests pass).

Run: `make frontend`
Expected: `internal/web/dist/index.html` exists.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "feat(web/f1): make targets for frontend, dev, frontend-check"
```

---

### Task 10: Extend `make api-generate` to emit TypeScript schema

**Files:**
- Modify: `Makefile` (api-generate recipe)
- Create: `frontend/src/lib/api/generated/.gitkeep`
- Create: `frontend/src/lib/api/client.ts` (thin wrapper)
- Create: `frontend/src/lib/api/generated/.gitignore` (allow only schema.ts and .gitkeep)

- [ ] **Step 1: Update the existing `api-generate` recipe**

The current recipe dumps `openapi.json`. Extend to also emit TS types:

```make
api-generate:
	go run ./cmd/fotobank-openapi -out openapi.json
	cd frontend && bun install && bunx openapi-typescript ../openapi.json -o src/lib/api/generated/schema.ts
```

- [ ] **Step 2: Write the thin client wrapper**

```ts
// frontend/src/lib/api/client.ts
import createClient, { type ClientOptions } from "openapi-fetch";
import type { paths } from "./generated/schema";

export type Client = ReturnType<typeof createClient<paths>>;

export function createApiClient(
  baseUrl: string = "",
  options: Pick<ClientOptions, "fetch" | "querySerializer"> = {},
): Client {
  return createClient<paths>({ baseUrl, ...options });
}

// Default singleton used at app boot. Empty baseUrl = same-origin.
export const api: Client = createApiClient("");
```

- [ ] **Step 3: Run `make api-generate` and confirm**

Run: `make api-generate`
Expected: `openapi.json` regenerated, `frontend/src/lib/api/generated/schema.ts` exists with `paths` types.

- [ ] **Step 4: Commit**

```bash
git add Makefile frontend/src/lib/api/
git commit -m "feat(web/f1): TypeScript client codegen via openapi-typescript"
```

---

## Section C — Theme, shell, and identity stub

### Task 11: CSS variables for light/dark theme

**Files:**
- Modify: `frontend/src/app.css`

- [ ] **Step 1: Replace stub with the full theme palette**

```css
/* frontend/src/app.css */
:root {
  --bg-primary:   #ffffff;
  --bg-surface:   #f6f7f9;
  --bg-elevated:  #ffffff;
  --text-primary: #1a1d22;
  --text-secondary: #5a6068;
  --text-muted:   #8b929c;
  --accent:       #2563eb;
  --border:       #e3e6eb;
  --shadow:       0 1px 3px rgba(0,0,0,0.06), 0 1px 2px rgba(0,0,0,0.04);
  --radius:       6px;
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
}

@media (prefers-color-scheme: dark) {
  :root {
    --bg-primary:   #0f1115;
    --bg-surface:   #14171c;
    --bg-elevated:  #1a1e25;
    --text-primary: #e9ecf1;
    --text-secondary: #a9b1bc;
    --text-muted:   #6c7480;
    --accent:       #4f8bff;
    --border:       #232831;
    --shadow:       0 1px 3px rgba(0,0,0,0.4);
  }
}

/* Manual override class set by themeStore. */
:root.theme-light { color-scheme: light; }
:root.theme-light {
  --bg-primary: #ffffff; --bg-surface: #f6f7f9; --bg-elevated: #ffffff;
  --text-primary: #1a1d22; --text-secondary: #5a6068; --text-muted: #8b929c;
  --accent: #2563eb; --border: #e3e6eb;
  --shadow: 0 1px 3px rgba(0,0,0,0.06), 0 1px 2px rgba(0,0,0,0.04);
}
:root.theme-dark { color-scheme: dark; }
:root.theme-dark {
  --bg-primary: #0f1115; --bg-surface: #14171c; --bg-elevated: #1a1e25;
  --text-primary: #e9ecf1; --text-secondary: #a9b1bc; --text-muted: #6c7480;
  --accent: #4f8bff; --border: #232831;
  --shadow: 0 1px 3px rgba(0,0,0,0.4);
}

body {
  margin: 0;
  background: var(--bg-primary);
  color: var(--text-primary);
}

* { box-sizing: border-box; }
```

- [ ] **Step 2: Commit**

```bash
git add frontend/src/app.css
git commit -m "feat(web/f1): theme variables + light/dark + manual override"
```

---

### Task 12: `themeStore` reading and writing `user_settings`

**Files:**
- Create: `frontend/src/lib/theme/themeStore.svelte.ts`
- Create: `frontend/src/lib/theme/themeStore.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/theme/themeStore.test.ts
import { describe, it, expect, vi } from "vitest";
import { ThemeStore } from "./themeStore.svelte";

describe("ThemeStore", () => {
  it("defaults to system when no override is loaded", () => {
    const store = new ThemeStore({
      get: vi.fn().mockResolvedValue({ data: undefined, error: { status: 404 } }),
      put: vi.fn(),
    } as never);
    expect(store.theme).toBe("system");
  });

  it("applies override after load", async () => {
    const get = vi.fn().mockResolvedValue({ data: { value: '"dark"' }, error: undefined });
    const store = new ThemeStore({ get, put: vi.fn() } as never);
    await store.load();
    expect(store.theme).toBe("dark");
  });

  it("persists override on set", async () => {
    const put = vi.fn().mockResolvedValue({ error: undefined });
    const store = new ThemeStore({ get: vi.fn(), put } as never);
    await store.set("light");
    expect(put).toHaveBeenCalledWith(
      "/settings/user/{key}",
      expect.objectContaining({
        params: { path: { key: "theme" } },
        body: { value: '"light"' },
      }),
    );
    expect(store.theme).toBe("light");
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the store**

```ts
// frontend/src/lib/theme/themeStore.svelte.ts
import type { Client } from "../api/client";

export type Theme = "system" | "light" | "dark";

export class ThemeStore {
  theme = $state<Theme>("system");
  loaded = $state(false);

  constructor(private client: Pick<Client, "GET" | "PUT">) {}

  async load() {
    const res = await this.client.GET("/api/v1/settings/user/{key}", {
      params: { path: { key: "theme" } },
    });
    if (res.data?.value) {
      try {
        const parsed = JSON.parse(res.data.value);
        if (parsed === "light" || parsed === "dark" || parsed === "system") {
          this.theme = parsed;
        }
      } catch {
        // Ignore malformed value; stay on system.
      }
    }
    this.loaded = true;
    this.apply();
  }

  async set(theme: Theme) {
    this.theme = theme;
    this.apply();
    await this.client.PUT("/api/v1/settings/user/{key}", {
      params: { path: { key: "theme" } },
      body: { value: JSON.stringify(theme) },
    });
  }

  private apply() {
    const root = document.documentElement;
    root.classList.remove("theme-light", "theme-dark");
    if (this.theme === "light") root.classList.add("theme-light");
    if (this.theme === "dark") root.classList.add("theme-dark");
  }
}
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/theme/
git commit -m "feat(web/f1): themeStore with user_settings persistence"
```

---

### Task 13: Three-column shell with sidebar + header

**Files:**
- Create: `frontend/src/lib/components/ThreeColumnLayout.svelte`
- Create: `frontend/src/lib/components/AppHeader.svelte`
- Create: `frontend/src/lib/components/Sidebar.svelte`
- Modify: `frontend/src/App.svelte`

- [ ] **Step 1: Write `ThreeColumnLayout.svelte`**

```svelte
<!-- frontend/src/lib/components/ThreeColumnLayout.svelte -->
<script lang="ts">
  import type { Snippet } from "svelte";

  let { sidebar, main, detail }: {
    sidebar: Snippet;
    main: Snippet;
    detail?: Snippet;
  } = $props();
</script>

<div class="shell">
  <aside class="sidebar">{@render sidebar()}</aside>
  <section class="main">{@render main()}</section>
  {#if detail}
    <aside class="detail">{@render detail()}</aside>
  {/if}
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: 220px 1fr auto;
    height: 100vh;
    width: 100vw;
  }
  .sidebar {
    background: var(--bg-surface);
    border-right: 1px solid var(--border);
    overflow-y: auto;
  }
  .main {
    overflow: auto;
    background: var(--bg-primary);
  }
  .detail {
    background: var(--bg-surface);
    border-left: 1px solid var(--border);
    overflow-y: auto;
  }
</style>
```

- [ ] **Step 2: Write `AppHeader.svelte`** (basic header with identity stub + search input stub bound to ⌘K)

```svelte
<!-- frontend/src/lib/components/AppHeader.svelte -->
<script lang="ts">
  let searchEl: HTMLInputElement | null = $state(null);

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key === "k") {
        e.preventDefault();
        searchEl?.focus();
      }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>

<header class="strip">
  <div class="brand">fotobank</div>
  <div class="identity">stub: alice</div>
  <input
    bind:this={searchEl}
    class="search"
    type="search"
    placeholder="Search ⌘K"
    aria-label="Search"
  />
  <button class="account" aria-label="Account menu">⋯</button>
</header>

<style>
  .strip {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 6px 12px;
    border-bottom: 1px solid var(--border);
    background: var(--bg-elevated);
    height: 44px;
    box-shadow: var(--shadow);
  }
  .brand { font-weight: 600; font-size: 14px; }
  .identity { font-size: 12px; color: var(--text-secondary); }
  .search {
    flex: 1;
    max-width: 540px;
    height: 28px;
    padding: 0 10px;
    border: 1px solid var(--border);
    border-radius: 14px;
    background: var(--bg-surface);
    color: var(--text-primary);
    font-size: 13px;
    margin-left: auto;
  }
  .account {
    background: transparent;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    padding: 4px 10px;
    color: var(--text-primary);
    cursor: pointer;
  }
</style>
```

- [ ] **Step 3: Write `Sidebar.svelte`** (Library + Sessions + Settings only)

```svelte
<!-- frontend/src/lib/components/Sidebar.svelte -->
<script lang="ts">
  let { active }: { active: string } = $props();

  const items = [
    { group: "BROWSE", entries: [
      { id: "library", label: "Library", href: "/library" },
      { id: "sessions", label: "Sessions", href: "/sessions" },
    ]},
    { group: "", entries: [
      { id: "settings", label: "Settings", href: "/settings" },
    ]},
  ];
</script>

<nav>
  {#each items as section (section.group + section.entries.map(e => e.id).join(','))}
    {#if section.group}
      <div class="group">{section.group}</div>
    {/if}
    {#each section.entries as entry (entry.id)}
      <a class="entry" class:active={active === entry.id} href={entry.href}>{entry.label}</a>
    {/each}
  {/each}
</nav>

<style>
  nav { padding: 12px 8px; display: flex; flex-direction: column; gap: 2px; }
  .group {
    font-size: 10px;
    text-transform: uppercase;
    letter-spacing: 0.6px;
    color: var(--text-muted);
    padding: 12px 8px 4px;
  }
  .entry {
    display: block;
    padding: 5px 10px;
    border-radius: var(--radius);
    color: var(--text-primary);
    text-decoration: none;
    font-size: 13px;
  }
  .entry:hover { background: var(--bg-elevated); }
  .entry.active { background: var(--accent); color: white; }
</style>
```

- [ ] **Step 4: Wire into `App.svelte`** with a basic hash-based router (one page per route in F1; replace with a real router only if it earns its keep)

```svelte
<!-- frontend/src/App.svelte -->
<script lang="ts">
  import ThreeColumnLayout from "./lib/components/ThreeColumnLayout.svelte";
  import AppHeader from "./lib/components/AppHeader.svelte";
  import Sidebar from "./lib/components/Sidebar.svelte";
  import { ThemeStore } from "./lib/theme/themeStore.svelte";
  import { api } from "./lib/api/client";

  const themeStore = new ThemeStore(api);
  themeStore.load();

  let route = $state(window.location.pathname || "/library");

  $effect(() => {
    const onPop = () => (route = window.location.pathname || "/library");
    window.addEventListener("popstate", onPop);
    return () => window.removeEventListener("popstate", onPop);
  });

  function activeId(path: string): string {
    if (path.startsWith("/sessions")) return "sessions";
    if (path.startsWith("/settings")) return "settings";
    return "library";
  }
</script>

<AppHeader />
<ThreeColumnLayout>
  {#snippet sidebar()}
    <Sidebar active={activeId(route)} />
  {/snippet}
  {#snippet main()}
    {#if route.startsWith("/settings")}
      <div style="padding:20px">Settings (placeholder; theme = {themeStore.theme})</div>
    {:else if route.startsWith("/sessions")}
      <div style="padding:20px">Sessions route — Task 27</div>
    {:else}
      <div style="padding:20px">Library route — Task 23</div>
    {/if}
  {/snippet}
</ThreeColumnLayout>
```

- [ ] **Step 5: Smoke and commit**

Run: `cd frontend && bun run build`
Expected: build succeeds; opening `frontend/dist/index.html` shows shell + sidebar.

```bash
git add frontend/src/
git commit -m "feat(web/f1): three-column shell with sidebar + header"
```

---

### Task 14: SSE bootstrap connection

**Files:**
- Create: `frontend/src/lib/events/eventsStore.svelte.ts`
- Create: `frontend/src/lib/events/eventsStore.test.ts`
- Modify: `frontend/src/App.svelte` (call `eventsStore.connect()` on mount)

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/events/eventsStore.test.ts
import { describe, it, expect } from "vitest";
import { EventsStore, type SSEMessage } from "./eventsStore.svelte";

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  url: string;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onerror: (() => void) | null = null;
  listeners: Record<string, ((ev: MessageEvent) => void)[]> = {};
  constructor(url: string) {
    this.url = url;
    FakeEventSource.instances.push(this);
  }
  addEventListener(name: string, fn: (ev: MessageEvent) => void) {
    (this.listeners[name] ??= []).push(fn);
  }
  fire(name: string, ev: MessageEvent) {
    (this.listeners[name] ?? []).forEach((f) => f(ev));
  }
  close() {}
}

describe("EventsStore", () => {
  it("captures the hello event", () => {
    const store = new EventsStore({ EventSourceCtor: FakeEventSource as never });
    store.connect();
    const inst = FakeEventSource.instances.at(-1)!;
    inst.fire("hello", new MessageEvent("hello", { data: '{"principal":"alice"}', lastEventId: "0" }));
    expect(store.lastEvent?.type).toBe("hello");
  });

  it("stores incoming events with type and id", () => {
    const store = new EventsStore({ EventSourceCtor: FakeEventSource as never });
    store.connect();
    const inst = FakeEventSource.instances.at(-1)!;
    inst.fire("import.progress", new MessageEvent("import.progress", { data: '{"processed":3}', lastEventId: "12" }));
    const msg = store.lastEvent as SSEMessage;
    expect(msg.type).toBe("import.progress");
    expect(msg.id).toBe("12");
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the store**

```ts
// frontend/src/lib/events/eventsStore.svelte.ts
export type SSEMessage = { id: string; type: string; data: unknown };

const KNOWN_EVENTS = [
  "hello",
  "import.progress",
  "ai.tag.completed",
  "ai.caption.completed",
  "ai.embed.completed",
  "share.status.changed",
  "ai.health.changed",
  "catchup-required",
] as const;

type EventSourceLike = {
  addEventListener(name: string, fn: (ev: MessageEvent) => void): void;
  close(): void;
};

export class EventsStore {
  lastEvent = $state<SSEMessage | null>(null);
  catchupRequired = $state(false);
  private es: EventSourceLike | null = null;
  private ctor: new (url: string) => EventSourceLike;

  constructor(opts: { EventSourceCtor?: new (url: string) => EventSourceLike } = {}) {
    this.ctor = opts.EventSourceCtor ?? (EventSource as never);
  }

  connect(url = "/api/v1/events") {
    if (this.es) return;
    this.es = new this.ctor(url);
    for (const name of KNOWN_EVENTS) {
      this.es.addEventListener(name, (ev) => {
        let data: unknown = ev.data;
        try { data = JSON.parse(ev.data); } catch { /* leave as string */ }
        this.lastEvent = { id: ev.lastEventId ?? "", type: name, data };
        if (name === "catchup-required") this.catchupRequired = true;
      });
    }
  }

  disconnect() {
    this.es?.close();
    this.es = null;
  }
}
```

In `App.svelte`, add:

```ts
  import { EventsStore } from "./lib/events/eventsStore.svelte";
  const events = new EventsStore();
  events.connect();
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/events/ frontend/src/App.svelte
git commit -m "feat(web/f1): SSE bootstrap and known-event dispatch"
```

---

## Section D — Library grid

### Task 15: `justifiedLayout` pure function

**Files:**
- Create: `frontend/src/lib/grid/justifiedLayout.ts`
- Create: `frontend/src/lib/grid/justifiedLayout.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/grid/justifiedLayout.test.ts
import { describe, it, expect } from "vitest";
import { computeJustified } from "./justifiedLayout";

describe("computeJustified", () => {
  it("packs items at target row height when widths fit", () => {
    const items = [
      { aspect: 1.5 }, { aspect: 1.5 }, { aspect: 1.5 },
    ];
    const layout = computeJustified(items, { containerWidth: 900, targetRowHeight: 200, gap: 0 });
    expect(layout.rows).toHaveLength(1);
    expect(layout.rows[0]?.height).toBeCloseTo(200, 0);
    expect(layout.rows[0]?.items).toHaveLength(3);
  });

  it("breaks rows when width exceeds container", () => {
    const items = Array.from({ length: 10 }, () => ({ aspect: 1.5 }));
    const layout = computeJustified(items, { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(layout.rows.length).toBeGreaterThan(1);
    for (const row of layout.rows) {
      const totalW = row.items.reduce((s, it) => s + it.width, 0) + (row.items.length - 1) * 4;
      expect(totalW).toBeLessThanOrEqual(900 + 1);
    }
  });

  it("preserves aspect ratios within ±2px after rounding", () => {
    const items = [{ aspect: 1.5 }, { aspect: 0.66 }, { aspect: 1.0 }];
    const layout = computeJustified(items, { containerWidth: 800, targetRowHeight: 180, gap: 0 });
    for (const row of layout.rows) {
      for (const it of row.items) {
        const expectedAspect = items[it.index]!.aspect;
        const observedAspect = it.width / row.height;
        expect(Math.abs(observedAspect - expectedAspect)).toBeLessThan(0.05);
      }
    }
  });

  it("returns empty rows for empty input", () => {
    expect(computeJustified([], { containerWidth: 800, targetRowHeight: 200, gap: 0 }).rows).toEqual([]);
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the function**

```ts
// frontend/src/lib/grid/justifiedLayout.ts
export type LayoutItem = { aspect: number };

export type LayoutOptions = {
  containerWidth: number;
  targetRowHeight: number;
  gap?: number;
  minRowHeight?: number;
  maxRowHeight?: number;
};

export type Row = {
  y: number;
  height: number;
  items: { index: number; x: number; width: number }[];
};

export type Layout = { rows: Row[]; totalHeight: number };

export function computeJustified(items: LayoutItem[], opts: LayoutOptions): Layout {
  const gap = opts.gap ?? 4;
  const minH = opts.minRowHeight ?? Math.floor(opts.targetRowHeight * 0.6);
  const maxH = opts.maxRowHeight ?? Math.ceil(opts.targetRowHeight * 1.6);
  const rows: Row[] = [];
  let y = 0;

  let pending: { index: number; aspect: number }[] = [];
  let pendingAspectSum = 0;

  const flush = (forceRow: boolean) => {
    if (pending.length === 0) return;
    // Row width budget = containerWidth - total gaps.
    const budget = opts.containerWidth - gap * (pending.length - 1);
    let height = budget / pendingAspectSum;
    if (forceRow) {
      // Final, possibly under-filled row: cap at targetRowHeight.
      height = Math.min(height, opts.targetRowHeight);
    } else {
      height = Math.max(minH, Math.min(maxH, height));
    }
    let x = 0;
    const rowItems: Row["items"] = [];
    for (const p of pending) {
      const w = p.aspect * height;
      rowItems.push({ index: p.index, x, width: w });
      x += w + gap;
    }
    rows.push({ y, height, items: rowItems });
    y += height + gap;
    pending = [];
    pendingAspectSum = 0;
  };

  for (let i = 0; i < items.length; i++) {
    const aspect = Math.max(items[i]!.aspect, 0.05);
    pending.push({ index: i, aspect });
    pendingAspectSum += aspect;
    const widthIfPacked = pendingAspectSum * opts.targetRowHeight + gap * (pending.length - 1);
    if (widthIfPacked >= opts.containerWidth) {
      flush(false);
    }
  }
  flush(true);

  return { rows, totalHeight: rows.length === 0 ? 0 : y - gap };
}
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/grid/
git commit -m "feat(web/f1): justified-row layout function with tests"
```

---

### Task 16: `MonthChunk` component

**Files:**
- Create: `frontend/src/lib/grid/MonthChunk.svelte`
- Create: `frontend/src/lib/grid/MonthChunk.test.ts`

- [ ] **Step 1: Write the test for layout caching and intrinsic height**

```ts
// frontend/src/lib/grid/MonthChunk.test.ts
import { describe, it, expect } from "vitest";
import { computeMonthLayout, type MediaLite } from "./MonthChunk.svelte";

describe("computeMonthLayout", () => {
  it("returns intrinsic height for content-visibility:auto skipping", () => {
    const items: MediaLite[] = Array.from({ length: 8 }, (_, i) => ({
      id: String(i), aspect: 1.5,
    }));
    const out = computeMonthLayout(items, { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(out.intrinsicHeight).toBeGreaterThan(0);
    expect(out.layout.rows.length).toBeGreaterThan(0);
  });

  it("treats empty months as zero-height", () => {
    const out = computeMonthLayout([], { containerWidth: 900, targetRowHeight: 200, gap: 4 });
    expect(out.intrinsicHeight).toBe(0);
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the component plus exported helper**

```svelte
<!-- frontend/src/lib/grid/MonthChunk.svelte -->
<script context="module" lang="ts">
  import { computeJustified, type LayoutOptions } from "./justifiedLayout";

  export type MediaLite = { id: string; aspect: number; thumbUrl?: string };

  export function computeMonthLayout(items: MediaLite[], opts: LayoutOptions) {
    const layout = computeJustified(items.map((m) => ({ aspect: m.aspect })), opts);
    return { layout, intrinsicHeight: layout.totalHeight };
  }
</script>

<script lang="ts">
  import type { Snippet } from "svelte";

  let { items, options, label, renderCell }: {
    items: MediaLite[];
    options: LayoutOptions;
    label?: string;
    renderCell?: Snippet<[MediaLite, { x: number; y: number; w: number; h: number }]>;
  } = $props();

  let computed = $derived(computeMonthLayout(items, options));
</script>

<section class="month" style="min-height: {computed.intrinsicHeight}px;">
  {#if label}<header class="day-header">{label}</header>{/if}
  <div class="cells" style="position: relative; height: {computed.intrinsicHeight}px;">
    {#each computed.layout.rows as row (row.y)}
      {#each row.items as cell (cell.index)}
        {#if items[cell.index]}
          {@const m = items[cell.index]}
          <div
            class="cell"
            style="position: absolute; left: {cell.x}px; top: {row.y}px;
                   width: {cell.width}px; height: {row.height}px;
                   content-visibility: auto;
                   contain-intrinsic-size: {cell.width}px {row.height}px;"
          >
            {#if renderCell}
              {@render renderCell(m, { x: cell.x, y: row.y, w: cell.width, h: row.height })}
            {:else}
              <div class="placeholder"></div>
            {/if}
          </div>
        {/if}
      {/each}
    {/each}
  </div>
</section>

<style>
  .month { display: block; }
  .day-header {
    font-size: 12px;
    color: var(--text-secondary);
    padding: 16px 4px 8px;
    font-weight: 500;
  }
  .placeholder {
    width: 100%;
    height: 100%;
    background: var(--bg-elevated);
    border-radius: 2px;
  }
</style>
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/grid/
git commit -m "feat(web/f1): MonthChunk with stable intrinsic sizing"
```

---

### Task 17: `mediaStore` with month chunking and pagination

**Files:**
- Create: `frontend/src/lib/media/mediaStore.svelte.ts`
- Create: `frontend/src/lib/media/mediaStore.test.ts`

- [ ] **Step 1: Write the test**

```ts
// frontend/src/lib/media/mediaStore.test.ts
import { describe, it, expect, vi } from "vitest";
import { MediaStore, monthKey } from "./mediaStore.svelte";

describe("monthKey", () => {
  it("buckets a date into YYYY-MM", () => {
    expect(monthKey(new Date("2026-04-18T12:00:00Z"))).toBe("2026-04");
    expect(monthKey(new Date("2026-12-31T23:59:00Z"))).toBe("2026-12");
  });
});

describe("MediaStore", () => {
  it("groups loaded media by month, descending", async () => {
    const fakeClient = {
      GET: vi.fn().mockResolvedValue({
        data: {
          items: [
            { id: "1", timestamp: "2026-04-18T12:00:00Z", width: 3, height: 2 },
            { id: "2", timestamp: "2026-03-22T08:00:00Z", width: 4, height: 3 },
            { id: "3", timestamp: "2026-04-19T08:00:00Z", width: 1, height: 1 },
          ],
          next_offset: null,
        },
        error: undefined,
      }),
    };
    const store = new MediaStore(fakeClient as never);
    await store.loadInitial();
    expect(store.months.map((m) => m.key)).toEqual(["2026-04", "2026-03"]);
    expect(store.months[0]?.items.length).toBe(2);
    expect(store.months[1]?.items.length).toBe(1);
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the store**

```ts
// frontend/src/lib/media/mediaStore.svelte.ts
import type { Client } from "../api/client";

export type Media = {
  id: string;
  timestamp: string;
  aspect: number;
  thumbUrl: string;
  taken: Date;
};

export type Month = {
  key: string; // YYYY-MM
  items: Media[];
};

export function monthKey(d: Date): string {
  const y = d.getUTCFullYear();
  const m = String(d.getUTCMonth() + 1).padStart(2, "0");
  return `${y}-${m}`;
}

export class MediaStore {
  months = $state<Month[]>([]);
  loading = $state(false);
  exhausted = $state(false);
  private nextOffset: number | null = 0;

  constructor(private client: Pick<Client, "GET">) {}

  async loadInitial() { await this.loadMore(); }

  async loadMore() {
    if (this.loading || this.exhausted) return;
    this.loading = true;
    try {
      const res = await this.client.GET("/api/v1/media", {
        params: { query: { limit: 200, offset: this.nextOffset ?? 0 } } as never,
      });
      if (res.error || !res.data) return;
      const items = ((res.data as { items?: Array<Record<string, unknown>> }).items ?? [])
        .map(toMedia)
        .filter((m): m is Media => m !== null);
      this.merge(items);
      const next = (res.data as { next_offset?: number | null }).next_offset ?? null;
      this.nextOffset = next;
      if (next === null) this.exhausted = true;
    } finally {
      this.loading = false;
    }
  }

  private merge(items: Media[]) {
    const byMonth = new Map<string, Media[]>();
    for (const m of this.months) byMonth.set(m.key, m.items.slice());
    for (const it of items) {
      const k = monthKey(it.taken);
      const list = byMonth.get(k) ?? [];
      list.push(it);
      byMonth.set(k, list);
    }
    this.months = Array.from(byMonth.entries())
      .map(([key, items]) => ({
        key,
        items: items.sort((a, b) => +b.taken - +a.taken),
      }))
      .sort((a, b) => (a.key < b.key ? 1 : -1));
  }
}

function toMedia(raw: Record<string, unknown>): Media | null {
  const id = raw.id;
  const ts = raw.timestamp;
  const w = raw.width;
  const h = raw.height;
  if (typeof id !== "string" || typeof ts !== "string") return null;
  const taken = new Date(ts);
  if (isNaN(+taken)) return null;
  const wn = typeof w === "number" ? w : 1;
  const hn = typeof h === "number" ? h : 1;
  return {
    id,
    timestamp: ts,
    taken,
    aspect: hn === 0 ? 1 : wn / hn,
    thumbUrl: `/api/v1/media/${id}/thumb`,
  };
}
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/media/
git commit -m "feat(web/f1): MediaStore with month chunking + pagination"
```

---

### Task 18: `VirtualGrid` mounting and unmounting month chunks

**Files:**
- Create: `frontend/src/lib/grid/VirtualGrid.svelte`

- [ ] **Step 1: Implement VirtualGrid using `IntersectionObserver` to mount/unmount month chunks**

Hand-rolled per the spec (no TanStack). Each `MonthChunk` is rendered with stable intrinsic height; `content-visibility: auto` does the heavy lifting on paint, while we use `IntersectionObserver` only to trigger pagination near the bottom.

```svelte
<!-- frontend/src/lib/grid/VirtualGrid.svelte -->
<script lang="ts">
  import MonthChunk, { type MediaLite } from "./MonthChunk.svelte";
  import type { Month, Media } from "../media/mediaStore.svelte";

  let { months, onLoadMore, targetRowHeight = 200 }: {
    months: Month[];
    onLoadMore?: () => void;
    targetRowHeight?: number;
  } = $props();

  let containerEl: HTMLDivElement | null = $state(null);
  let containerWidth = $state(800);
  let sentinel: HTMLDivElement | null = $state(null);

  $effect(() => {
    if (!containerEl) return;
    const ro = new ResizeObserver((entries) => {
      const w = entries[0]?.contentRect.width;
      if (w) containerWidth = w;
    });
    ro.observe(containerEl);
    return () => ro.disconnect();
  });

  $effect(() => {
    if (!sentinel) return;
    const io = new IntersectionObserver((entries) => {
      if (entries[0]?.isIntersecting) onLoadMore?.();
    }, { rootMargin: "800px 0px" });
    io.observe(sentinel);
    return () => io.disconnect();
  });

  function toLite(items: Media[]): MediaLite[] {
    return items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }));
  }
</script>

<div bind:this={containerEl} class="grid">
  {#each months as month (month.key)}
    <MonthChunk
      items={toLite(month.items)}
      label={month.key}
      options={{ containerWidth, targetRowHeight, gap: 4 }}
    >
      {#snippet renderCell(m)}
        <a href={`/media/${m.id}`}>
          <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
        </a>
      {/snippet}
    </MonthChunk>
  {/each}
  <div bind:this={sentinel} style="height:1px"></div>
</div>

<style>
  .grid { padding: 8px; }
</style>
```

- [ ] **Step 2: Smoke and commit**

Run: `cd frontend && bun run typecheck`
Expected: clean.

```bash
git add frontend/src/lib/grid/VirtualGrid.svelte
git commit -m "feat(web/f1): VirtualGrid with month-chunk pagination"
```

---

### Task 19: `Library` route

**Files:**
- Create: `frontend/src/routes/Library.svelte`
- Modify: `frontend/src/App.svelte` (route to Library on `/` and `/library`)

- [ ] **Step 1: Implement Library**

```svelte
<!-- frontend/src/routes/Library.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  store.loadInitial();
</script>

<VirtualGrid
  months={store.months}
  onLoadMore={() => store.loadMore()}
  targetRowHeight={200}
/>

{#if store.loading}
  <div style="padding:12px; color: var(--text-muted)">Loading…</div>
{/if}
{#if store.months.length === 0 && !store.loading}
  <div style="padding:24px; color: var(--text-secondary)">No photos yet.</div>
{/if}
```

In `App.svelte`'s main snippet, replace the placeholder:

```svelte
{:else}
  <Library />
{/if}
```

with `import Library from "./routes/Library.svelte";` at the top.

- [ ] **Step 2: Smoke build**

Run: `cd frontend && bun run build`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add frontend/src/
git commit -m "feat(web/f1): Library route with virtualized grid"
```

---

### Task 20: Selection store and keyboard model

**Files:**
- Create: `frontend/src/lib/selection/selectionStore.svelte.ts`
- Create: `frontend/src/lib/selection/selectionStore.test.ts`
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte` (wire up click handlers)

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/selection/selectionStore.test.ts
import { describe, it, expect } from "vitest";
import { SelectionStore } from "./selectionStore.svelte";

describe("SelectionStore", () => {
  it("toggle adds and removes ids", () => {
    const s = new SelectionStore();
    s.toggle("a"); s.toggle("b"); s.toggle("a");
    expect(s.ids).toEqual(new Set(["b"]));
  });

  it("range selects contiguous slice given an ordering", () => {
    const s = new SelectionStore();
    const order = ["a", "b", "c", "d", "e"];
    s.toggle("b");
    s.range("d", order);
    expect([...s.ids].sort()).toEqual(["b", "c", "d"]);
  });

  it("clear empties selection", () => {
    const s = new SelectionStore();
    s.toggle("a"); s.toggle("b");
    s.clear();
    expect(s.ids.size).toBe(0);
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement the store**

```ts
// frontend/src/lib/selection/selectionStore.svelte.ts
export class SelectionStore {
  ids = $state<Set<string>>(new Set());
  lastAnchor: string | null = $state(null);

  toggle(id: string) {
    const next = new Set(this.ids);
    if (next.has(id)) next.delete(id); else next.add(id);
    this.ids = next;
    this.lastAnchor = id;
  }

  set(id: string, on: boolean) {
    const next = new Set(this.ids);
    if (on) next.add(id); else next.delete(id);
    this.ids = next;
    this.lastAnchor = id;
  }

  range(target: string, ordered: string[]) {
    const anchor = this.lastAnchor ?? target;
    const i = ordered.indexOf(anchor);
    const j = ordered.indexOf(target);
    if (i < 0 || j < 0) { this.toggle(target); return; }
    const [lo, hi] = i < j ? [i, j] : [j, i];
    const next = new Set(this.ids);
    for (let k = lo; k <= hi; k++) {
      const id = ordered[k];
      if (id) next.add(id);
    }
    this.ids = next;
    this.lastAnchor = target;
  }

  clear() {
    this.ids = new Set();
    this.lastAnchor = null;
  }
}
```

- [ ] **Step 4: Wire the store into `VirtualGrid` cells**

Replace the cell `<a>` with a button-like element that:
- Click (no modifier) → navigate (existing behavior)
- ⌘/Ctrl-click → `selection.toggle(id)`, `event.preventDefault()`
- Shift-click → `selection.range(id, allIdsInOrder)`, `preventDefault`

Add a global Esc handler in `App.svelte` that clears selection.

- [ ] **Step 5: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/selection/ frontend/src/lib/grid/VirtualGrid.svelte frontend/src/App.svelte
git commit -m "feat(web/f1): selection store + click/⌘/shift/Esc handlers"
```

---

### Task 21: Sticky month bar + day-header subcomponents

**Files:**
- Create: `frontend/src/lib/components/StickyMonthBar.svelte`
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte` (track current month from scroll position; render sticky bar)

- [ ] **Step 1: Implement StickyMonthBar**

```svelte
<!-- frontend/src/lib/components/StickyMonthBar.svelte -->
<script lang="ts">
  let { label }: { label: string } = $props();
</script>

{#if label}
  <div class="bar" aria-hidden="true">{label}</div>
{/if}

<style>
  .bar {
    position: sticky;
    top: 0;
    z-index: 5;
    padding: 4px 12px;
    font-size: 11px;
    text-transform: uppercase;
    letter-spacing: 0.5px;
    color: var(--text-muted);
    background: color-mix(in srgb, var(--bg-primary) 80%, transparent);
    backdrop-filter: blur(6px);
    border-bottom: 1px solid var(--border);
  }
</style>
```

In `VirtualGrid`, listen for scroll on the parent; for each `MonthChunk`, observe its position. The currently-visible month is the topmost month whose chunk's top is above the viewport top. Update `activeMonth` reactively. Pass it into `<StickyMonthBar label={activeMonth} />` rendered above the chunks.

A pragmatic v1: use `IntersectionObserver` with `rootMargin: "-1px 0px -100% 0px"` per chunk; the chunk that's intersecting the top-1px sliver is the active one.

- [ ] **Step 2: Smoke and commit**

```bash
git add frontend/src/lib/
git commit -m "feat(web/f1): sticky month bar"
```

---

### Task 22: Year scrubber on the right edge

**Files:**
- Create: `frontend/src/lib/components/YearScrubber.svelte`
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte`

- [ ] **Step 1: Implement YearScrubber**

```svelte
<!-- frontend/src/lib/components/YearScrubber.svelte -->
<script lang="ts">
  import type { Month } from "../media/mediaStore.svelte";
  let { months, onJump }: { months: Month[]; onJump: (key: string) => void } = $props();
  let years = $derived(uniqueYears(months));

  function uniqueYears(ms: Month[]): { year: string; firstMonthKey: string }[] {
    const seen = new Map<string, string>();
    for (const m of ms) {
      const y = m.key.slice(0, 4);
      if (!seen.has(y)) seen.set(y, m.key);
    }
    return [...seen.entries()].map(([year, firstMonthKey]) => ({ year, firstMonthKey }));
  }
</script>

<aside class="scrubber" aria-label="Jump to year">
  {#each years as y (y.year)}
    <button onclick={() => onJump(y.firstMonthKey)}>{y.year}</button>
  {/each}
</aside>

<style>
  .scrubber {
    position: fixed;
    top: 60px;
    right: 4px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    z-index: 6;
  }
  .scrubber button {
    background: transparent;
    border: none;
    color: var(--text-muted);
    font-size: 10px;
    padding: 1px 6px;
    cursor: pointer;
    border-radius: 6px;
  }
  .scrubber button:hover { background: var(--bg-elevated); color: var(--text-primary); }
</style>
```

In `VirtualGrid`, expose a `jumpTo(monthKey)` method that scrolls the matching chunk into view; pass it as `onJump` to YearScrubber.

- [ ] **Step 2: Commit**

```bash
git add frontend/src/lib/
git commit -m "feat(web/f1): year scrubber"
```

---

### Task 23: Density control with per-context persistence

**Files:**
- Create: `frontend/src/lib/components/DensityControl.svelte`
- Create: `frontend/src/lib/density/densityStore.svelte.ts`
- Create: `frontend/src/lib/density/densityStore.test.ts`
- Modify: `frontend/src/lib/grid/VirtualGrid.svelte` (use density store to drive `targetRowHeight`)

- [ ] **Step 1: Write the failing test**

```ts
// frontend/src/lib/density/densityStore.test.ts
import { describe, it, expect, vi } from "vitest";
import { DensityStore, ROW_HEIGHTS } from "./densityStore.svelte";

describe("DensityStore", () => {
  it("defaults to comfortable", () => {
    const store = new DensityStore({ get: vi.fn(), put: vi.fn() } as never, "library");
    expect(store.preset).toBe("comfortable");
    expect(store.targetRowHeight).toBe(ROW_HEIGHTS.comfortable);
  });

  it("persists per-context key", async () => {
    const put = vi.fn().mockResolvedValue({ error: undefined });
    const store = new DensityStore({ get: vi.fn(), put } as never, "library");
    await store.set("compact");
    expect(put).toHaveBeenCalledWith(
      "/settings/user/{key}",
      expect.objectContaining({ params: { path: { key: "density.library" } } }),
    );
    expect(store.preset).toBe("compact");
  });

  it("nudge bumps within bounds", () => {
    const store = new DensityStore({ get: vi.fn(), put: vi.fn() } as never, "library");
    store.preset = "compact";
    store.nudge(1);
    expect(store.preset).toBe("comfortable");
    store.nudge(1);
    expect(store.preset).toBe("large");
    store.nudge(1); // already at max, stays
    expect(store.preset).toBe("large");
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement**

```ts
// frontend/src/lib/density/densityStore.svelte.ts
import type { Client } from "../api/client";

export type Preset = "compact" | "comfortable" | "large";

export const ROW_HEIGHTS: Record<Preset, number> = {
  compact: 140,
  comfortable: 200,
  large: 280,
};

const ORDER: Preset[] = ["compact", "comfortable", "large"];

export class DensityStore {
  preset = $state<Preset>("comfortable");
  loaded = $state(false);

  constructor(
    private client: Pick<Client, "GET" | "PUT">,
    private context: string,
  ) {}

  get targetRowHeight(): number { return ROW_HEIGHTS[this.preset]; }

  async load() {
    const res = await this.client.GET("/api/v1/settings/user/{key}", {
      params: { path: { key: `density.${this.context}` } },
    });
    if (res.data?.value) {
      try {
        const v = JSON.parse(res.data.value);
        if (v === "compact" || v === "comfortable" || v === "large") this.preset = v;
      } catch {}
    }
    this.loaded = true;
  }

  async set(p: Preset) {
    this.preset = p;
    await this.client.PUT("/api/v1/settings/user/{key}", {
      params: { path: { key: `density.${this.context}` } },
      body: { value: JSON.stringify(p) },
    });
  }

  nudge(delta: 1 | -1) {
    const i = ORDER.indexOf(this.preset);
    const next = ORDER[Math.max(0, Math.min(ORDER.length - 1, i + delta))];
    if (next && next !== this.preset) this.set(next);
  }
}
```

`DensityControl.svelte`:

```svelte
<!-- frontend/src/lib/components/DensityControl.svelte -->
<script lang="ts">
  import type { DensityStore, Preset } from "../density/densityStore.svelte";
  let { store }: { store: DensityStore } = $props();
  const presets: Preset[] = ["compact", "comfortable", "large"];

  $effect(() => {
    function onKey(e: KeyboardEvent) {
      if ((e.target as HTMLElement)?.tagName === "INPUT") return;
      if (e.key === "+" || e.key === "=") { store.nudge(1); e.preventDefault(); }
      else if (e.key === "-" || e.key === "_") { store.nudge(-1); e.preventDefault(); }
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });
</script>

<div class="density">
  {#each presets as p (p)}
    <button class:active={store.preset === p} onclick={() => store.set(p)}>{p}</button>
  {/each}
</div>

<style>
  .density { display: inline-flex; gap: 0; border: 1px solid var(--border); border-radius: var(--radius); overflow: hidden; }
  .density button {
    background: transparent;
    border: none;
    padding: 4px 10px;
    color: var(--text-secondary);
    font-size: 12px;
    cursor: pointer;
  }
  .density button.active { background: var(--accent); color: white; }
</style>
```

- [ ] **Step 4: Wire into Library**

```svelte
<script>
  import { DensityStore } from "../lib/density/densityStore.svelte";
  const density = new DensityStore(api, "library");
  density.load();
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>
<VirtualGrid months={store.months} onLoadMore={() => store.loadMore()} targetRowHeight={density.targetRowHeight} />
```

- [ ] **Step 5: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/
git commit -m "feat(web/f1): density control with per-context persistence"
```

---

## Section E — Sessions, action bar, media detail

### Task 24: `sessionGrouping` pure function

**Files:**
- Create: `frontend/src/lib/sessions/sessionGrouping.ts`
- Create: `frontend/src/lib/sessions/sessionGrouping.test.ts`

- [ ] **Step 1: Write the failing test**

```ts
import { describe, it, expect } from "vitest";
import { groupIntoSessions } from "./sessionGrouping";
import type { Media } from "../media/mediaStore.svelte";

const m = (id: string, iso: string): Media => ({
  id, timestamp: iso, taken: new Date(iso), aspect: 1, thumbUrl: "",
});

describe("groupIntoSessions", () => {
  it("starts a new session when gap exceeds threshold", () => {
    const items = [
      m("a", "2026-04-18T10:00:00Z"),
      m("b", "2026-04-18T10:30:00Z"),
      m("c", "2026-04-18T18:00:00Z"), // 7.5h gap
      m("d", "2026-04-19T01:00:00Z"), // 7h gap
    ];
    const sessions = groupIntoSessions(items, { gapHours: 4 });
    expect(sessions).toHaveLength(3);
    expect(sessions[0]?.items.map(i => i.id)).toEqual(["a", "b"]);
    expect(sessions[1]?.items.map(i => i.id)).toEqual(["c"]);
    expect(sessions[2]?.items.map(i => i.id)).toEqual(["d"]);
  });

  it("returns empty for empty input", () => {
    expect(groupIntoSessions([], { gapHours: 4 })).toEqual([]);
  });
});
```

- [ ] **Step 2: Run to confirm fail**

Run: `cd frontend && bun run test`
Expected: FAIL.

- [ ] **Step 3: Implement**

```ts
// frontend/src/lib/sessions/sessionGrouping.ts
import type { Media } from "../media/mediaStore.svelte";

export type Session = { id: string; items: Media[] };

export function groupIntoSessions(
  items: Media[],
  opts: { gapHours: number },
): Session[] {
  if (items.length === 0) return [];
  const sorted = [...items].sort((a, b) => +a.taken - +b.taken);
  const gapMs = opts.gapHours * 3600 * 1000;
  const out: Session[] = [];
  let current: Media[] = [];
  let prev: Media | null = null;
  for (const m of sorted) {
    if (prev && +m.taken - +prev.taken > gapMs) {
      out.push({ id: current[0]!.id, items: current });
      current = [];
    }
    current.push(m);
    prev = m;
  }
  if (current.length) out.push({ id: current[0]!.id, items: current });
  return out.reverse();
}
```

- [ ] **Step 4: Run and commit**

Run: `cd frontend && bun run test`
Expected: PASS.

```bash
git add frontend/src/lib/sessions/
git commit -m "feat(web/f1): session grouping function"
```

---

### Task 25: `Sessions` route

**Files:**
- Create: `frontend/src/routes/Sessions.svelte`
- Modify: `frontend/src/App.svelte` (route `/sessions` to Sessions)

- [ ] **Step 1: Implement Sessions**

```svelte
<!-- frontend/src/routes/Sessions.svelte -->
<script lang="ts">
  import VirtualGrid from "../lib/grid/VirtualGrid.svelte";
  import MonthChunk from "../lib/grid/MonthChunk.svelte";
  import { MediaStore } from "../lib/media/mediaStore.svelte";
  import { groupIntoSessions } from "../lib/sessions/sessionGrouping";
  import { DensityStore } from "../lib/density/densityStore.svelte";
  import DensityControl from "../lib/components/DensityControl.svelte";
  import { api } from "../lib/api/client";

  const store = new MediaStore(api);
  store.loadInitial();
  const density = new DensityStore(api, "sessions");
  density.load();
  const flat = $derived(store.months.flatMap((m) => m.items));
  const sessions = $derived(groupIntoSessions(flat, { gapHours: 4 }));
</script>

<header style="display:flex; justify-content: flex-end; padding: 6px 12px;">
  <DensityControl store={density} />
</header>

<div style="padding: 8px;">
  {#each sessions as s (s.id)}
    <MonthChunk
      items={s.items.map((m) => ({ id: m.id, aspect: m.aspect, thumbUrl: m.thumbUrl }))}
      label={s.items[0]?.taken.toUTCString().slice(0, 16) + ` · ${s.items.length} photos`}
      options={{ containerWidth: 1100, targetRowHeight: density.targetRowHeight, gap: 4 }}
    >
      {#snippet renderCell(m)}
        <a href={`/media/${m.id}`}>
          <img src={m.thumbUrl} alt="" loading="lazy" decoding="async" style="width:100%;height:100%;object-fit:cover" />
        </a>
      {/snippet}
    </MonthChunk>
  {/each}
</div>
```

- [ ] **Step 2: Smoke and commit**

```bash
git add frontend/src/
git commit -m "feat(web/f1): Sessions route with time-clustered grouping"
```

---

### Task 26: Shell-strip action bar (placeholder actions)

**Files:**
- Create: `frontend/src/lib/components/ActionBar.svelte`
- Modify: `frontend/src/App.svelte` (render ActionBar in the shell strip when `selection.ids.size > 0`)

- [ ] **Step 1: Implement ActionBar**

Per the spec, F1 shows the action bar but the actions are placeholders that say which sub-plan owns them. Hide / Add to album / Share / Done are all visible; the first three open a tooltip explaining "Coming in F5/F6/F6" for now. Done clears selection.

```svelte
<!-- frontend/src/lib/components/ActionBar.svelte -->
<script lang="ts">
  import type { SelectionStore } from "../selection/selectionStore.svelte";
  let { selection }: { selection: SelectionStore } = $props();

  function unimplemented(label: string) {
    alert(`${label} arrives in a later sub-plan.`);
  }
</script>

{#if selection.ids.size > 0}
  <div class="bar" role="toolbar" aria-label="Selection actions">
    <span class="count">{selection.ids.size} selected</span>
    <button onclick={() => unimplemented("Add to album (F6)")}>Add to album</button>
    <button onclick={() => unimplemented("Hide (F5)")}>Hide</button>
    <button onclick={() => unimplemented("Share (F6)")}>Share</button>
    <button onclick={() => selection.clear()}>Done</button>
  </div>
{/if}

<style>
  .bar {
    position: fixed;
    top: 8px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 10;
    display: flex;
    gap: 8px;
    align-items: center;
    padding: 6px 12px;
    background: var(--bg-elevated);
    border: 1px solid var(--border);
    border-radius: 18px;
    box-shadow: var(--shadow);
  }
  .count { font-size: 12px; color: var(--text-secondary); padding: 0 6px; }
  button {
    background: transparent;
    border: none;
    color: var(--text-primary);
    cursor: pointer;
    font-size: 13px;
    padding: 4px 10px;
    border-radius: 12px;
  }
  button:hover { background: var(--bg-surface); }
</style>
```

In `App.svelte`, instantiate a single `SelectionStore` and pass it to both `VirtualGrid` and `ActionBar`. (Pass via Svelte context if cleaner.)

- [ ] **Step 2: Commit**

```bash
git add frontend/src/lib/components/ActionBar.svelte frontend/src/App.svelte
git commit -m "feat(web/f1): action bar with placeholder actions"
```

---

### Task 27: `MediaDetail` placeholder route

**Files:**
- Create: `frontend/src/routes/MediaDetail.svelte`
- Modify: `frontend/src/App.svelte` (route `/media/:id`)

- [ ] **Step 1: Implement a basic detail view**

F1 shows the preview thumbnail at fit size with "Back" navigation. F2 replaces this with the full lightbox (zoom, pan, info panel, keyboard ladder).

```svelte
<!-- frontend/src/routes/MediaDetail.svelte -->
<script lang="ts">
  let { id }: { id: string } = $props();
</script>

<div class="wrap">
  <a href="/library" class="back" aria-label="Back to library">←</a>
  <img src={`/api/v1/media/${id}/thumb`} alt="" />
  <p class="note">F2 will replace this with the full lightbox.</p>
</div>

<style>
  .wrap {
    background: black;
    min-height: 100vh;
    display: grid;
    place-items: center;
    color: #ccc;
    position: relative;
  }
  .back {
    position: absolute;
    top: 12px; left: 12px;
    color: #ccc; text-decoration: none;
    font-size: 24px;
  }
  .wrap img { max-width: 90vw; max-height: 80vh; object-fit: contain; }
  .note { font-size: 11px; color: #888; }
</style>
```

In `App.svelte`, add a route handler that extracts `id` from `/media/:id`:

```ts
function mediaIdFromRoute(p: string): string | null {
  const m = p.match(/^\/media\/([^/]+)$/);
  return m?.[1] ?? null;
}
```

And in the main snippet, branch:

```svelte
{:else if mediaIdFromRoute(route)}
  <MediaDetail id={mediaIdFromRoute(route)!} />
```

- [ ] **Step 2: Commit**

```bash
git add frontend/src/
git commit -m "feat(web/f1): MediaDetail placeholder route"
```

---

## Section F — E2E harness, perf check, README

### Task 28: `cmd/e2e-server` with temp-file SQLite + fixtures

**Files:**
- Create: `cmd/e2e-server/main.go`
- Create: `cmd/e2e-server/fixture/.gitkeep`

- [ ] **Step 1: Implement the e2e server**

It runs the regular server's `RunContext` against a generated config that uses a temp-file SQLite DB and a small fixture media set. The fixture loader inserts ~50 rows into `media` (no actual files needed for grid render — the SPA loads thumbnails which can 404 in e2e; we test grid layout, selection, density, theme persistence — not pixel-accurate thumbs).

```go
// cmd/e2e-server/main.go
// Build a fotobank with a temp-file SQLite DB and seed enough media
// rows for the Library/Sessions grid to render. Used by Playwright.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/wesm/fotobank/internal/cli"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	tmp, err := os.MkdirTemp("", "fotobank-e2e-")
	if err != nil { return err }
	cfgPath := filepath.Join(tmp, "fotobank.toml")
	nasRoot := filepath.Join(tmp, "nas")
	flashRoot := filepath.Join(tmp, "flash")
	for _, d := range []string{nasRoot, flashRoot} {
		if err := os.MkdirAll(d, 0o700); err != nil { return err }
	}
	cfg := fmt.Sprintf(`
[nas]
root = "%s"
[flash]
root = "%s"
[identity]
mode = "stub"
[identity.stub]
hub = "local"
user_id = "alice"
handle = "Alice"
storage_key = "alice-sk"
[http]
listen_address = "127.0.0.1:0"
[imports]
file_lock_path = "%s"
[backup]
enabled = false
[observability]
admin_listen = "127.0.0.1:0"
`, nasRoot, flashRoot, filepath.Join(tmp, "import.lock"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil { return err }

	// Forward SIGINT/SIGTERM to context cancel.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigCh; cancel() }()

	// Seed a fixture: post-boot, the e2e test can import a fixture
	// directory or POST media records via the API. The simpler path
	// is for the test to call the importer with files in NAS fixture.
	// F1 e2e uses an empty library + a "no photos yet" assertion;
	// later sub-plans inject fixtures. Keep this binary minimal.

	return cli.RunContext(ctx, []string{"server", "--config", cfgPath}, os.Stdout, os.Stderr)
}
```

- [ ] **Step 2: Build it**

```bash
go build -o tmp/e2e-server ./cmd/e2e-server
```

Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add cmd/e2e-server/
git commit -m "feat(web/f1): e2e-server cmd with temp-file SQLite"
```

---

### Task 29: Playwright config + library smoke test

**Files:**
- Create: `frontend/playwright-e2e.config.ts`
- Create: `frontend/tests/e2e/library.spec.ts`

- [ ] **Step 1: Write the playwright config**

```ts
// frontend/playwright-e2e.config.ts
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/e2e",
  fullyParallel: false, // single server
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: [["list"]],
  use: {
    baseURL: "http://127.0.0.1:8080",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"] } },
  ],
  webServer: {
    command: "../tmp/e2e-server",
    port: 8080,
    reuseExistingServer: false,
    timeout: 60_000,
  },
});
```

- [ ] **Step 2: Write a smoke test**

```ts
// frontend/tests/e2e/library.spec.ts
import { test, expect } from "@playwright/test";

test("library route renders shell + sidebar + empty-state", async ({ page }) => {
  await page.goto("/");
  await expect(page.getByText("fotobank")).toBeVisible();
  await expect(page.getByRole("link", { name: "Library" })).toBeVisible();
  await expect(page.getByText(/no photos yet/i)).toBeVisible();
});

test("sessions route renders", async ({ page }) => {
  await page.goto("/sessions");
  await expect(page.getByText("fotobank")).toBeVisible();
});

test("theme override persists via user_settings", async ({ page }) => {
  await page.goto("/settings");
  // Settings UI is placeholder in F1; this asserts the existing
  // GET /settings/user/theme route is wired by writing then reading.
  const put = await page.request.put("/api/v1/settings/user/theme", {
    data: { value: '"dark"' },
  });
  expect(put.status()).toBe(204);
  const get = await page.request.get("/api/v1/settings/user/theme");
  expect(get.status()).toBe(200);
  expect(await get.json()).toMatchObject({ value: '"dark"' });
});
```

- [ ] **Step 3: Update `Makefile` test-e2e target**

```make
test-e2e: frontend
	go build -o tmp/e2e-server ./cmd/e2e-server
	cd frontend && bun run playwright test --config=playwright-e2e.config.ts
```

- [ ] **Step 4: Run and commit**

Run: `make test-e2e`
Expected: tests pass. If Vite dev port conflicts with the e2e server, the webServer command (`../tmp/e2e-server`) drives a real built backend serving the embedded frontend — no Vite needed for e2e.

```bash
git add frontend/playwright-e2e.config.ts frontend/tests/ Makefile
git commit -m "feat(web/f1): Playwright e2e harness"
```

---

### Task 30: Smoke `frontend-check` and `make build` end-to-end

- [ ] **Step 1: Run `make frontend-check`**

Run: `make frontend-check`
Expected: `svelte-check`, `tsc --noEmit`, `eslint`, and `vitest` all pass.

- [ ] **Step 2: Run `make build`**

Run: `make build`
Expected: `bin/fotobank` built; opening it on a free port should serve the SPA at `/` and the API at `/api/v1/*`.

- [ ] **Step 3: Run `make test`**

Run: `make test`
Expected: all Go tests pass with the embed populated.

- [ ] **Step 4: Spot-check perf budget**

Manual smoke (developer only):
- Build for production, serve, open Library in Chromium.
- DevTools → Network: confirm initial bundle (gzipped main.js) is ≤ 250 KB.
- Devtools → Performance: scroll the (empty) library and confirm no obvious main-thread blocking from layout.

If the bundle is larger than 250 KB, investigate (commonly: openapi-fetch tree-shake, accidental imports). Document any deviation in the F1 wrap-up commit.

- [ ] **Step 5: Update README with the new make targets**

Modify `README.md` (or `CLAUDE.md`'s Quick Reference section, depending on which exists in this repo) to list:

```
make dev            — backend live-reload (use alongside `make frontend-dev`)
make frontend-dev   — Vite dev server with /api proxy
make frontend       — build SPA into internal/web/dist
make frontend-check — svelte-check + tsc + eslint + vitest
make api-generate   — regenerate openapi.json + frontend TS schema
make test-e2e       — Playwright e2e against built backend
```

- [ ] **Step 6: Final commit**

```bash
git add README.md
git commit -m "docs(web/f1): README quick-reference for new make targets"
```

---

## F1 done — summary

When this plan is complete, the repo contains:

- A working Vite + Svelte 5 + Bun frontend in `frontend/`, embedded into the Go binary via `internal/web/`.
- `make dev` + `make frontend-dev` running in tandem for backend + frontend live reload.
- `make api-generate` producing both `openapi.json` and a typed TS client.
- `make frontend-check` running `svelte-check`, `tsc`, `eslint`, and `vitest`.
- `make test-e2e` running Playwright against a real built backend (`cmd/e2e-server`, temp-file SQLite).
- A three-column shell with a sidebar (`Library`, `Sessions`, `Settings`), AppHeader (logo + identity stub + ⌘K-bound search input), and a collapsible detail rail slot (unused in F1 but reserved).
- Theme variables + light/dark + system-default + manual override persisted via `user_settings`.
- A Library route with a custom-virtualized justified-row grid (chunked by month, `content-visibility: auto` with stable intrinsic sizing, `loading="lazy"` + `decoding="async"`).
- A Sessions route reusing the grid with time-clustered grouping (4-hour gap default).
- Selection model (click / ⌘-click / shift-click / Esc / long-press) and a placeholder action bar.
- Density control (compact/comfortable/large + `+`/`-`) persisting per browse context.
- Sticky month bar + day headers + year scrubber.
- A placeholder MediaDetail route at `/media/:id` (full lightbox lands in F2).
- An SSE skeleton at `GET /api/v1/events` with `hello` / `catchup-required` and a per-principal ring buffer (domain events land in F3+).
- `user_settings` table folded into `000001_initial_schema.up.sql`; matching down file unchanged from prior state.

What's deliberately not in F1 (and which sub-plan owns it):
- Lightbox UX, preview tier 2560 / large tier 4096, info panel, keyboard ladder, video → **F2**.
- AI gateway, workers, tags/captions/embeddings, hybrid search, FTS, AI panel → **F3**.
- GPS schema slice, EXIF GPS extraction, reverse geocoder, map tile proxy, MapLibre view → **F4**.
- Hidden privacy gate, `auth_hidden_*` schema, `WHERE hidden_at IS NULL` cross-cutting enforcement, eye-strike badge → **F5**.
- Album metadata (description, cover, prefs) + share UX → **F6**.

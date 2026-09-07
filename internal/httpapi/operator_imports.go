package httpapi

import (
	"context"
	json "encoding/json/v2"
	"io"
	"net/http"
	"reflect"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/fotobank/internal/ingest"
)

type ImportRequest struct {
	Hub     string `json:"hub"`
	UserID  string `json:"user_id"`
	Source  string `json:"source" minLength:"1"`
	Workers int    `json:"workers,omitempty" minimum:"0" default:"0"`
	Wait    string `json:"wait,omitempty" default:"0s" doc:"Maximum wait for the import lock, e.g. 30s. Zero fails immediately if busy."`
}

type ImportProgress struct {
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Imported   int    `json:"imported"`
	Duplicates int    `json:"duplicates"`
	Conflicts  int    `json:"conflicts"`
	Failures   int    `json:"failures"`
	Path       string `json:"path,omitempty"`
}

type ImportResult struct {
	Imported   int      `json:"imported"`
	Duplicates int      `json:"duplicates"`
	Conflicts  int      `json:"conflicts"`
	Failures   []string `json:"failures"`
	Error      string   `json:"error,omitempty"`
}

// ImportEvent is one line of the response. Only a result event completes the
// operation; EOF without it is an interrupted response, not a successful import.
type ImportEvent struct {
	Type     string          `json:"type" enum:"progress,result"`
	Progress *ImportProgress `json:"progress,omitempty"`
	Result   *ImportResult   `json:"result,omitempty"`
}

func registerOperatorImports(api huma.API, deps *OperatorDeps) {
	schema := api.OpenAPI().Components.Schemas.Schema(reflect.TypeFor[ImportEvent](), true, "ImportEvent")
	huma.Register(api, huma.Operation{
		OperationID: "import-media", Method: http.MethodPost, Path: "/api/v1/operator/imports",
		Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}}, MaxBodyBytes: 16384,
		Summary:     "Import host files and stream progress",
		Description: "Returns newline-delimited JSON progress followed by one result, including partial counts and errors. HTTP 200 only means the stream started. Disconnect cancels unfinished work; completed files remain imported.",
		Responses: map[string]*huma.Response{"200": {
			Description: "Progress followed by a terminal import result",
			Content:     map[string]*huma.MediaType{"application/x-ndjson": {Schema: schema}},
		}},
	}, func(_ context.Context, in *struct{ Body ImportRequest }) (*huma.StreamResponse, error) {
		if deps == nil || in.Body.Hub != deps.Owner.Hub || in.Body.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("local operator authentication and configured owner required")
		}
		wait, err := time.ParseDuration(in.Body.Wait)
		if err != nil || wait < 0 {
			return nil, huma.Error400BadRequest("wait must be a non-negative duration")
		}
		return &huma.StreamResponse{Body: func(hctx huma.Context) {
			hctx.SetHeader("Content-Type", "application/x-ndjson")
			hctx.SetHeader("Cache-Control", "no-store")
			ctx, cancel := context.WithCancel(hctx.Context())
			defer cancel()
			_, writer := humago.Unwrap(hctx)
			var writeErr error
			// The importer emits progress sequentially from its result collector.
			send := func(event ImportEvent) {
				if writeErr != nil {
					return
				}
				writeErr = json.MarshalWrite(writer, event)
				if writeErr == nil {
					_, writeErr = io.WriteString(writer, "\n")
				}
				if writeErr == nil {
					writeErr = http.NewResponseController(writer).Flush()
				}
				if writeErr != nil {
					cancel()
				}
			}
			result, err := deps.Imports.Import(ctx, deps.Owner, in.Body.Source, in.Body.Workers, wait, func(p ingest.ProgressEvent) {
				send(ImportEvent{Type: "progress", Progress: &ImportProgress{
					Done: p.Done, Total: p.Total, Imported: p.Imported, Duplicates: p.Duplicates,
					Conflicts: p.Conflicts, Failures: p.Failures, Path: p.Path,
				}})
			})
			out := ImportResult{Imported: result.Imported, Duplicates: result.Duplicates, Conflicts: result.Conflicts, Failures: []string{}}
			for _, failure := range result.Failures {
				out.Failures = append(out.Failures, failure.Error())
			}
			if err != nil {
				out.Error = err.Error()
			}
			send(ImportEvent{Type: "result", Result: &out})
		}}, nil
	})
}

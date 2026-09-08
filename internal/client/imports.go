package client

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"

	"go.kenn.io/fotobank/internal/httpapi"
)

// Import streams a single request-bound import. Missing terminal results are
// errors; a broken connection never causes the client to resubmit the request.
func Import(ctx context.Context, dbPath, version string, input httpapi.ImportRequest, progress func(httpapi.ImportProgress)) (httpapi.ImportResult, error) {
	var result httpapi.ImportResult
	rec, _, found, err := findDaemon(ctx, dbPath)
	if err != nil {
		return result, err
	}
	if !found || rec.Version != version {
		return result, fmt.Errorf("no matching Fotobank server; run fotobank daemon start")
	}
	response, err := requestRecord(ctx, rec, http.MethodPost, "/api/v1/operator/imports", input, "rerun the import to reconcile completed files")
	if err != nil {
		return result, err
	}
	defer response.Body.Close()
	decoder := jsontext.NewDecoder(response.Body)
	for {
		var event httpapi.ImportEvent
		value, err := decoder.ReadValue()
		if err == nil {
			err = json.Unmarshal(value, &event)
		}
		if err != nil {
			return result, fmt.Errorf("import response ended without a final result; rerun the import to reconcile completed files: %w", err)
		}
		switch event.Type {
		case "progress":
			if event.Progress == nil {
				return result, errors.New("import progress event has no progress")
			}
			p := *event.Progress
			result.Imported, result.Duplicates, result.Conflicts = p.Imported, p.Duplicates, p.Conflicts
			if progress != nil {
				progress(p)
			}
		case "result":
			if event.Result == nil {
				return result, errors.New("import result event has no result")
			}
			result = *event.Result
			if result.Error != "" {
				return result, errors.New(result.Error)
			}
			return result, nil
		default:
			return result, fmt.Errorf("unexpected import event %q", event.Type)
		}
	}
}

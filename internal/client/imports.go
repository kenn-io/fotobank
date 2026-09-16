package client

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.kenn.io/fotobank/internal/client/generated"
	"go.kenn.io/fotobank/internal/httpapi"
)

// Import streams a single request-bound import. Missing terminal results are
// errors; a broken connection never causes the client to resubmit the request.
func Import(ctx context.Context, configPath, version string, input httpapi.ImportRequest, progress func(httpapi.ImportProgress)) (httpapi.ImportResult, error) {
	var result httpapi.ImportResult
	rec, _, found, err := findDaemon(ctx, configPath)
	if err != nil {
		return result, err
	}
	if !found || rec.Version != version {
		return result, fmt.Errorf("no matching Fotobank server; run fotobank daemon start")
	}
	c, err := recordClient(ctx, rec)
	if err != nil {
		return result, err
	}
	stream, err := c.ImportMediaStream(ctx, &generated.ImportMediaRequestOptions{Body: &input})
	if err != nil {
		return result, fmt.Errorf("operator response unavailable; rerun the import to reconcile completed files: %w", err)
	}
	defer stream.Close()
	for stream.Next() {
		event := stream.Current()
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
	return result, fmt.Errorf("import response ended without a final result; rerun the import to reconcile completed files: %w", errors.Join(io.ErrUnexpectedEOF, stream.Err()))
}

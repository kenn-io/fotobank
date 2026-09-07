package httpapi

import (
	"context"
	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/fotobank/internal/checkout"
	"net/http"
	"time"
)

type CheckoutSummaryOutput struct {
	ID        string                    `json:"id"`
	State     checkout.State            `json:"state"`
	Root      string                    `json:"root"`
	Layout    string                    `json:"layout"`
	LastError string                    `json:"last_error"`
	Entries   CheckoutEntryCountsOutput `json:"entries"`
	CreatedAt time.Time                 `json:"created_at"`
	UpdatedAt time.Time                 `json:"updated_at"`
}

type CheckoutEntryCountsOutput struct {
	Total    int `json:"total"`
	Clean    int `json:"clean"`
	Pending  int `json:"pending"`
	Conflict int `json:"conflict"`
	Missing  int `json:"missing"`
	Error    int `json:"error"`
}

type CheckoutSelectionOutput struct {
	All      bool                 `json:"all"`
	AssetIDs []string             `json:"asset_ids"`
	AlbumIDs []string             `json:"album_ids"`
	Years    []CheckoutYearOutput `json:"years"`
}

type CheckoutYearOutput struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

type CheckoutProblemOutput struct {
	FileID         string              `json:"file_id"`
	Path           string              `json:"path"`
	State          checkout.EntryState `json:"state"`
	LastError      string              `json:"last_error"`
	BaseVersionID  string              `json:"base_version_id"`
	BaseSHA256     string              `json:"base_sha256"`
	ObservedSHA256 string              `json:"observed_sha256"`
	UpdatedAt      time.Time           `json:"updated_at"`
}

type CheckoutStatusOutput struct {
	Checkout  CheckoutSummaryOutput   `json:"checkout"`
	Selection CheckoutSelectionOutput `json:"selection"`
	Problems  []CheckoutProblemOutput `json:"problems"`
}

func projectCheckoutSummary(summary checkout.Summary) CheckoutSummaryOutput {
	return CheckoutSummaryOutput{
		ID: summary.ID, State: summary.State, Root: summary.Root, Layout: summary.Layout,
		LastError: summary.LastError, Entries: CheckoutEntryCountsOutput{
			Total: summary.Entries.Total, Clean: summary.Entries.Clean,
			Pending: summary.Entries.Pending, Conflict: summary.Entries.Conflict,
			Missing: summary.Entries.Missing, Error: summary.Entries.Error,
		},
		CreatedAt: summary.CreatedAt, UpdatedAt: summary.UpdatedAt,
	}
}

func projectCheckoutStatus(status checkout.Status) CheckoutStatusOutput {
	years := make([]CheckoutYearOutput, len(status.Selection.Years))
	for index, yearRange := range status.Selection.Years {
		years[index] = CheckoutYearOutput{Start: yearRange.Start, End: yearRange.End}
	}
	problems := make([]CheckoutProblemOutput, len(status.Problems))
	for index, entry := range status.Problems {
		problems[index] = CheckoutProblemOutput{
			FileID: entry.FileID, Path: entry.RelativePath, State: entry.State,
			LastError: entry.LastError, BaseVersionID: entry.BaseVersionID,
			BaseSHA256: entry.BaseSHA256, ObservedSHA256: entry.ObservedSHA256,
			UpdatedAt: entry.UpdatedAt,
		}
	}
	return CheckoutStatusOutput{
		Checkout: projectCheckoutSummary(status.Checkout),
		Selection: CheckoutSelectionOutput{
			All:      status.Selection.All,
			AssetIDs: append([]string{}, status.Selection.AssetIDs...),
			AlbumIDs: append([]string{}, status.Selection.AlbumIDs...),
			Years:    years,
		},
		Problems: problems,
	}
}

func registerOperatorInspection(api huma.API, deps *OperatorDeps) {
	huma.Register(api, huma.Operation{
		OperationID: "list-checkouts", Method: http.MethodGet, Path: "/api/v1/operator/checkouts",
		Summary: "List saved checkout state", Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, input *struct {
		Hub    string `query:"hub"`
		UserID string `query:"user_id"`
	}) (*struct{ Body []CheckoutSummaryOutput }, error) {
		if deps == nil || input.Hub != deps.Owner.Hub || input.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("local operator authentication and configured owner required")
		}
		rows, err := deps.Checkouts.List(ctx, deps.Owner)
		if err != nil {
			return nil, Translate(err)
		}
		out := make([]CheckoutSummaryOutput, len(rows))
		for i, row := range rows {
			out[i] = projectCheckoutSummary(row)
		}
		return &struct{ Body []CheckoutSummaryOutput }{out}, nil
	})
	huma.Register(api, huma.Operation{
		OperationID: "checkout-status", Method: http.MethodGet, Path: "/api/v1/operator/checkouts/{checkout_id}",
		Summary: "Inspect saved checkout problems", Tags: []string{"operator"}, Security: []map[string][]string{{"localOperator": {}}},
	}, func(ctx context.Context, input *struct {
		CheckoutID string `path:"checkout_id"`
		Hub        string `query:"hub"`
		UserID     string `query:"user_id"`
	}) (*struct{ Body CheckoutStatusOutput }, error) {
		if deps == nil || input.Hub != deps.Owner.Hub || input.UserID != deps.Owner.UserID {
			return nil, huma.Error403Forbidden("local operator authentication and configured owner required")
		}
		status, err := deps.Checkouts.Status(ctx, deps.Owner, input.CheckoutID)
		if err != nil {
			return nil, Translate(err)
		}
		return &struct{ Body CheckoutStatusOutput }{projectCheckoutStatus(status)}, nil
	})
}

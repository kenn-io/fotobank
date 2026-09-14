package httpapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// Raw byte handlers keep HTTP range semantics; their registrations also publish
// this contract through the same Huma API used for JSON operations.
func registerDownloadSchema(api huma.API, path, operationID string, pathParams ...string) {
	params := make([]*huma.Param, 0, len(pathParams)+2)
	for _, name := range pathParams {
		params = append(params, &huma.Param{Name: name, In: "path", Required: true,
			Schema: &huma.Schema{Type: "string", Format: "uuid"}})
	}
	params = append(params,
		&huma.Param{Name: "Range", In: "header", Description: "Optional single byte range", Schema: &huma.Schema{Type: "string"}},
		&huma.Param{Name: "If-None-Match", In: "header", Description: "Optional cached content ETag", Schema: &huma.Schema{Type: "string"}},
	)
	content := map[string]*huma.MediaType{"*/*": {Schema: &huma.Schema{Type: "string", Format: "binary"}}}
	headers := map[string]*huma.Param{
		"Content-Length": {Description: "Number of response bytes", Schema: &huma.Schema{Type: "integer", Format: "int64"}},
		"ETag":           {Description: "Quoted content SHA-256 for visible media; omitted for hidden media", Schema: &huma.Schema{Type: "string"}},
		"Accept-Ranges":  {Schema: &huma.Schema{Type: "string"}},
	}
	api.OpenAPI().AddOperation(&huma.Operation{
		OperationID: operationID, Method: http.MethodGet, Path: path,
		Summary:     "Read original file bytes",
		Description: "Returns the owned file's current bytes with its stored media type. Hidden media requires an owner-matched unlock session. A file attached to a different asset returns 404. Use media details to obtain the expected size and SHA-256, then verify a complete download before using it; concurrent edits can invalidate those details.",
		Parameters:  params,
		Responses: map[string]*huma.Response{
			"200": {Description: "Complete file", Headers: headers, Content: content},
			"206": {Description: "Requested byte range", Headers: map[string]*huma.Param{
				"Content-Range":  {Schema: &huma.Schema{Type: "string"}},
				"Content-Length": headers["Content-Length"],
			}, Content: content},
			"304": {Description: "Cached content still current"},
			"401": {Description: "Authentication required"},
			"404": {Description: "File not found or not visible to the caller"},
			"416": {Description: "Malformed or unsatisfiable byte range"},
			"500": {Description: "Content unavailable or could not be opened"},
		},
	})
}

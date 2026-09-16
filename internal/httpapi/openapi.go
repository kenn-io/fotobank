package httpapi

import (
	"github.com/danielgtaylor/huma/v2"
)

// OpenAPISpec builds the huma API with no runtime dependencies and
// returns the generated OpenAPI document for dumping. It reuses the
// same JSON operation definitions as New. Registration does not open a
// database, vault, or provider. Original, attachment, thumbnail, and event
// stream registrations also publish their browser-facing contracts.
func OpenAPISpec() *huma.OpenAPI {
	_, api := buildAPI(Deps{})
	return api.OpenAPI()
}

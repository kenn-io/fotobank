package httpapi

import (
	"github.com/danielgtaylor/huma/v2"
)

// OpenAPISpec builds the huma API with no runtime dependencies and
// returns the generated OpenAPI document for dumping. It reuses the
// same JSON operation definitions as New. Registration does not open a
// database, vault, or provider. Raw byte and event routes are not included.
func OpenAPISpec() *huma.OpenAPI {
	_, api := buildAPI(Deps{})
	return api.OpenAPI()
}

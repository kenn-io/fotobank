package httpapi

import (
	"github.com/danielgtaylor/huma/v2"
)

// OpenAPISpec builds the huma API with no runtime dependencies and
// returns the generated OpenAPI document for dumping. It reuses the
// same buildAPI helper as New so the emitted spec always matches the
// routes served at runtime. Deps{} is passed because the spec dumper
// has no runtime collaborators; routes that require a collaborator
// will be absent from the emitted spec.
func OpenAPISpec() *huma.OpenAPI {
	_, api := buildAPI(Deps{})
	return api.OpenAPI()
}

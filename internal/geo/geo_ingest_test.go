package geo_test

import (
	"go.kenn.io/fotobank/internal/geo"
	"go.kenn.io/fotobank/internal/ingest"
)

// Compile-time guard: *geo.NaturalEarth MUST satisfy
// ingest.PlaceResolver. Importing ingest from geo's production package
// would create an import cycle because PlaceResolver belongs to its consumer
// in ingest. A test-package
// file can import ingest freely — `go test ./internal/geo/...` will
// fail to build if ingest renames the method or alters the signature,
// catching drift at the same point a production wiring assignment
// would.
var _ ingest.PlaceResolver = (*geo.NaturalEarth)(nil)

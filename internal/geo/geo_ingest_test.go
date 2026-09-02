package geo_test

import (
	"go.kenn.io/fotobank/internal/geo"
	"go.kenn.io/fotobank/internal/media"
)

// Compile-time guard: *geo.NaturalEarth MUST satisfy
// media.PlaceResolver. Importing media from geo's production package
// would create an import cycle because PlaceResolver belongs to its consumer
// in media. A test-package file can import media freely. The geo package test
// fails to build if media renames the method or alters the signature, catching
// drift at the same point a production wiring assignment would.
var _ media.PlaceResolver = (*geo.NaturalEarth)(nil)

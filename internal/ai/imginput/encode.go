// Package imginput exposes the canonical AI-input profile constants.
// The actual re-encoding pipeline lives in
// internal/ai/imginput/encode; chat workers call encode.EncodeChat
// and embed workers call encode.EncodeEmbed. ProfileV1 stays here
// because importers and gateway-fingerprint plumbing reference it
// without needing the encoder code.
package imginput

// ProfileV1 is the canonical name persisted on every ai_results row.
// Bumping requires a new constant and re-running the gap scanner.
const ProfileV1 = "jpeg-1024-q85-metadata-stripped-v1"

// Package ai owns the shared types and configuration for fotobank's
// VLM-driven tagging and captioning. Subpackages implement the gateway,
// queue, results store, workers, and gap scanner.
package ai

import "fmt"

// Task discriminates between the two AI tasks fotobank runs in v1.
type Task string

const (
	TaskTag     Task = "tag"
	TaskCaption Task = "caption"
)

// Valid reports whether t is a known task value.
func (t Task) Valid() bool { return t == TaskTag || t == TaskCaption }

// Fingerprint is the (model, prompt, input) triple that identifies a
// specific way of producing AI output. Stored verbatim on every result
// row; gap scanner and panel queries filter by the currently-configured
// triple.
type Fingerprint struct {
	ModelID       string
	PromptVersion string
	InputProfile  string
}

// String returns the canonical "model|prompt|profile" form persisted in
// ai_jobs.fingerprint and surfaced in SSE events.
func (f Fingerprint) String() string {
	return fmt.Sprintf("%s|%s|%s", f.ModelID, f.PromptVersion, f.InputProfile)
}

// JobStatus enumerates the states an ai_jobs row may occupy.
type JobStatus string

const (
	JobPending    JobStatus = "pending"
	JobWorking    JobStatus = "working"
	JobBlocked    JobStatus = "blocked"
	JobDone       JobStatus = "done"
	JobFailed     JobStatus = "failed"
	JobSuperseded JobStatus = "superseded"
)

// Terminal reports whether s is a no-further-work state.
func (s JobStatus) Terminal() bool {
	return s == JobDone || s == JobFailed || s == JobSuperseded
}

// LastErrorKind classifies why a job execution failed. Used both to
// drive in-call vs job-level retry decisions and to populate
// ai_failures.last_error_kind for the panel.
type LastErrorKind string

const (
	ErrKindTransient      LastErrorKind = "transient"
	ErrKindProvider4xx    LastErrorKind = "provider_4xx"
	ErrKindMalformed      LastErrorKind = "malformed"
	ErrKindThumbBlocked   LastErrorKind = "thumb_blocked"
	ErrKindMissingAIInput LastErrorKind = "missing_ai_input"
	ErrKindSuperseded     LastErrorKind = "superseded"
)

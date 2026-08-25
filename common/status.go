package common

// DeployStatus is the payload of a STATUS reply frame. It must stay compact
// enough to fit in a message parameter (MaxParameterLength bytes) once
// JSON-encoded.
type DeployStatus struct {
	// State is "idle" or "running".
	State string `json:"state"`
	// Run is the ID of the currently running run, if any.
	Run uint64 `json:"run,omitempty"`
	// Last is the outcome of the most recent finished run: "success",
	// "failed" or "none" if the deployment never ran.
	Last string `json:"last"`
	// LastRun is the ID of the most recent finished run.
	LastRun uint64 `json:"last_run,omitempty"`
	// FinishedAt is when the most recent run finished, in unix seconds.
	FinishedAt int64 `json:"finished,omitempty"`
	// DurationMS is how long the most recent run took, in milliseconds.
	DurationMS int64 `json:"dur_ms,omitempty"`
}

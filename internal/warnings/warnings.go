// Package warnings defines the non-fatal warning codes returned by the agent
// API and surfaced as Terraform diagnostics.
package warnings

const (
	CodeDurabilityUncertain = "durability_uncertain"
	CodeAuditWriteFailed    = "audit_write_failed"
	CodeFileDeleteFailed    = "file_delete_failed"
)

// Warning is a non-fatal result attached to a successful or partially
// successful operation.
type Warning struct {
	Code    string
	Message string
}

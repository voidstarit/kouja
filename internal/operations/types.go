package operations

import (
	"encoding/json"
	"time"

	"github.com/voidstarit/kouja/internal/observability"
	"github.com/voidstarit/kouja/internal/state"
)

type Action string

const (
	ActionCreate   Action = "create"
	ActionExport   Action = "export"
	ActionUnexport Action = "unexport"
	ActionResize   Action = "resize"
	ActionDelete   Action = "delete"
	ActionWipe     Action = "wipe"
	ActionImport   Action = "import"
)

type Options struct {
	MaxAttempts  int
	TotalTimeout time.Duration
	StepTimeouts map[Action]time.Duration
	Failpoints   Failpoints
	Metrics      *observability.Metrics
	Audit        AuditWriter
	Clock        Clock
}

type Request struct {
	Owner     string
	VolumeID  string
	Action    Action
	Intent    json.RawMessage
	RequestID string
}

type Result struct {
	Volume      state.Volume
	Export      state.Export
	OperationID string
}

type Failpoints interface {
	Hit(name string) error
}

type AuditWriter interface {
	Write(state.Audit) error
}

type Clock interface {
	Now() time.Time
}

package workflow

import (
	"context"
	"encoding/json"
	"time"
)

// Phase is the stage of a primitive call an event records.
type Phase string

const (
	PhaseStarted   Phase = "started"
	PhaseCompleted Phase = "completed"
	PhaseFailed    Phase = "failed"
)

// Event is one entry in a run's log. A primitive call writes a started
// event before its side effect and a completed or failed event after it.
// Children of parallel carry their parent's sequence number and their index.
type Event struct {
	Seq       int
	Parent    int
	Index     int
	Primitive string
	Phase     Phase
	Args      json.RawMessage
	ArgsHash  string
	Result    json.RawMessage
	Error     json.RawMessage
	At        time.Time
}

// Recorder persists a run's events. It must be safe for concurrent use.
type Recorder interface {
	Record(ctx context.Context, ev Event) error
}

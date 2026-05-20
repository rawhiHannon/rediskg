package pipeline

import (
	"encoding/json"
	"sync"
	"time"
)

// PhaseStatus represents the current state of a pipeline phase.
type PhaseStatus string

const (
	PhasePending    PhaseStatus = "pending"
	PhaseRunning    PhaseStatus = "running"
	PhaseCompleted  PhaseStatus = "completed"
	PhaseSkipped    PhaseStatus = "skipped"
	PhaseFailed     PhaseStatus = "failed"
)

// PhaseInfo holds timing and count data for a single pipeline phase.
type PhaseInfo struct {
	Name      string      `json:"name"`
	Status    PhaseStatus `json:"status"`
	StartedAt *time.Time  `json:"started_at,omitempty"`
	EndedAt   *time.Time  `json:"ended_at,omitempty"`
	Duration  string      `json:"duration,omitempty"`
	Details   string      `json:"details,omitempty"`
}

// PipelineStats holds the full telemetry for a pipeline run.
type PipelineStats struct {
	mu sync.RWMutex

	RunID     string       `json:"run_id"`
	Status    PhaseStatus  `json:"status"` // overall pipeline status
	StartedAt *time.Time   `json:"started_at,omitempty"`
	EndedAt   *time.Time   `json:"ended_at,omitempty"`
	Duration  string       `json:"duration,omitempty"`
	Phases    []*PhaseInfo `json:"phases"`
	Counts    StatCounts   `json:"counts"`
	Error     string       `json:"error,omitempty"`

	subscribers []chan []byte
}

// StatCounts holds aggregate counts collected during the pipeline run.
type StatCounts struct {
	Documents         int `json:"documents"`
	Chunks            int `json:"chunks"`
	EntitiesExtracted int `json:"entities_extracted"`
	EdgesExtracted    int `json:"edges_extracted"`
	EntitiesCanonical int `json:"entities_canonical"`
	AliasMappings     int `json:"alias_mappings"`
	EdgesAfterSolver  int `json:"edges_after_solver"`
	FinalEntities     int `json:"final_entities"`
	FinalEdges        int `json:"final_edges"`
	GraphNodes        int `json:"graph_nodes"`
	GraphEdges        int `json:"graph_edges"`
}

// NewPipelineStats creates a new telemetry tracker for a pipeline run.
func NewPipelineStats(runID string) *PipelineStats {
	now := time.Now()
	return &PipelineStats{
		RunID:     runID,
		Status:    PhaseRunning,
		StartedAt: &now,
		Phases:    make([]*PhaseInfo, 0),
	}
}

// Subscribe returns a channel that receives JSON-encoded SSE events
// for every phase transition. The channel is closed when the pipeline
// finishes (Complete or Fail).
func (ps *PipelineStats) Subscribe() chan []byte {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ch := make(chan []byte, 64)
	ps.subscribers = append(ps.subscribers, ch)
	return ch
}

// Unsubscribe removes and closes a subscriber channel.
func (ps *PipelineStats) Unsubscribe(ch chan []byte) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for i, sub := range ps.subscribers {
		if sub == ch {
			ps.subscribers = append(ps.subscribers[:i], ps.subscribers[i+1:]...)
			break
		}
	}
}

// broadcast sends a snapshot event to all subscribers.
func (ps *PipelineStats) broadcast(event string) {
	snap := ps.snapshot()
	data, _ := json.Marshal(map[string]interface{}{
		"event": event,
		"data":  snap,
	})
	for _, ch := range ps.subscribers {
		select {
		case ch <- data:
		default:
			// subscriber too slow, skip
		}
	}
}

// snapshot returns a copy of current stats (caller must hold at least RLock,
// but broadcast calls this under write lock already).
func (ps *PipelineStats) snapshot() map[string]interface{} {
	return map[string]interface{}{
		"run_id":     ps.RunID,
		"status":     ps.Status,
		"started_at": ps.StartedAt,
		"ended_at":   ps.EndedAt,
		"duration":   ps.Duration,
		"phases":     ps.Phases,
		"counts":     ps.Counts,
		"error":      ps.Error,
	}
}

// StartPhase begins tracking a new phase.
func (ps *PipelineStats) StartPhase(name string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	ps.Phases = append(ps.Phases, &PhaseInfo{
		Name:      name,
		Status:    PhaseRunning,
		StartedAt: &now,
	})
	ps.broadcast("phase_start")
}

// EndPhase marks the current (last) phase as completed with optional details.
func (ps *PipelineStats) EndPhase(details string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if len(ps.Phases) == 0 {
		return
	}
	phase := ps.Phases[len(ps.Phases)-1]
	now := time.Now()
	phase.EndedAt = &now
	phase.Status = PhaseCompleted
	if phase.StartedAt != nil {
		phase.Duration = now.Sub(*phase.StartedAt).Round(time.Millisecond).String()
	}
	phase.Details = details
	ps.broadcast("phase_end")
}

// SkipPhase records a phase that was skipped.
func (ps *PipelineStats) SkipPhase(name, reason string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.Phases = append(ps.Phases, &PhaseInfo{
		Name:    name,
		Status:  PhaseSkipped,
		Details: reason,
	})
	ps.broadcast("phase_skip")
}

// SetCounts updates aggregate counts and broadcasts.
func (ps *PipelineStats) SetCounts(fn func(c *StatCounts)) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	fn(&ps.Counts)
	ps.broadcast("counts_update")
}

// Complete marks the pipeline run as finished successfully.
func (ps *PipelineStats) Complete() {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	ps.EndedAt = &now
	ps.Status = PhaseCompleted
	if ps.StartedAt != nil {
		ps.Duration = now.Sub(*ps.StartedAt).Round(time.Millisecond).String()
	}
	ps.broadcast("pipeline_complete")
	ps.closeSubscribers()
}

// Fail marks the pipeline run as failed.
func (ps *PipelineStats) Fail(err string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	now := time.Now()
	ps.EndedAt = &now
	ps.Status = PhaseFailed
	ps.Error = err
	if ps.StartedAt != nil {
		ps.Duration = now.Sub(*ps.StartedAt).Round(time.Millisecond).String()
	}
	// Mark current running phase as failed
	for _, phase := range ps.Phases {
		if phase.Status == PhaseRunning {
			phase.Status = PhaseFailed
			phase.EndedAt = &now
			if phase.StartedAt != nil {
				phase.Duration = now.Sub(*phase.StartedAt).Round(time.Millisecond).String()
			}
		}
	}
	ps.broadcast("pipeline_failed")
	ps.closeSubscribers()
}

// Snapshot returns a thread-safe copy of the current stats as JSON.
func (ps *PipelineStats) Snapshot() ([]byte, error) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	return json.Marshal(ps.snapshot())
}

func (ps *PipelineStats) closeSubscribers() {
	for _, ch := range ps.subscribers {
		close(ch)
	}
	ps.subscribers = nil
}

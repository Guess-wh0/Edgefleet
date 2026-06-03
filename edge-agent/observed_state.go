package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"
)

type ObservedState struct {
	Workloads map[string]ObservedWorkload `json:"workloads"`
}

// Observed state is agent-owned runtime truth; handlers return facts but never mutate it directly.
type ObservedWorkload struct {
	Version    int             `json:"version"`
	Type       string          `json:"type"`
	Status     string          `json:"status"`
	Spec       json.RawMessage `json:"spec,omitempty"`
	LastOutput string          `json:"last_output,omitempty"`
	LastError  string          `json:"last_error,omitempty"`
	UpdatedAt  string          `json:"updated_at"`
}

func loadObservedState() (ObservedState, error) {
	data, err := os.ReadFile(workloadStatePath())
	if err == nil {
		var state ObservedState
		if err := json.Unmarshal(data, &state); err != nil {
			return ObservedState{}, err
		}
		if state.Workloads == nil {
			state.Workloads = map[string]ObservedWorkload{}
		}
		return state, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return ObservedState{Workloads: map[string]ObservedWorkload{}}, nil
	}
	return ObservedState{}, err
}

func saveObservedState(state ObservedState) error {
	if state.Workloads == nil {
		state.Workloads = map[string]ObservedWorkload{}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(workloadStatePath(), data, 0644)
}

// Reconciliation owns comparison, handler selection, execution, and observed-state updates.
func reconcileTypedDesiredState(state *PersistentState, desired DesiredState, handlers map[string]Handler) error {
	if err := validateTypedDesiredState(desired); err != nil {
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "invalid-desired", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": desired.WorkloadID,
			"type":        desired.Type,
			"version":     desired.Version,
			"reason":      err.Error(),
			"retryable":   false,
		}, fmt.Sprintf("[RECONCILE] invalid desired workload=%s reason=%s", desired.WorkloadID, err.Error()))
		return err
	}

	observed, err := loadObservedState()
	if err != nil {
		return err
	}

	if err := stopWorkloadsMissingFromDesired(*state, observed, desired.WorkloadID, handlers); err != nil {
		return err
	}
	observed, err = loadObservedState()
	if err != nil {
		return err
	}

	current, exists := observed.Workloads[desired.WorkloadID]
	if exists && current.Version > desired.Version {
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "stale", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": desired.WorkloadID,
			"remote":      desired.Version,
			"local":       current.Version,
		}, fmt.Sprintf("[RECONCILE] compare workload=%s remote=%d local=%d result=stale", desired.WorkloadID, desired.Version, current.Version))
		return nil
	}
	if exists && current.Version == desired.Version && workloadIsApplied(current.Status) {
		logReconciliation("edge-agent.reconcile", "reconciliation_decision", "in-sync", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": desired.WorkloadID,
			"remote":      desired.Version,
			"local":       current.Version,
			"status":      current.Status,
		}, fmt.Sprintf("[RECONCILE] compare workload=%s remote=%d local=%d result=in-sync", desired.WorkloadID, desired.Version, current.Version))
		return nil
	}

	handler, ok := handlers[desired.Type]
	if !ok {
		logExecution("edge-agent.execution", "handler_selection_failed", "error", map[string]any{
			"node_id":      state.NodeID,
			"workload_id":  desired.WorkloadID,
			"type":         desired.Type,
			"version":      desired.Version,
			"reason":       "unknown workload type",
			"retryable":    false,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[HANDLER] unknown type=%s workload=%s", desired.Type, desired.WorkloadID))
		return fmt.Errorf("unknown workload type %q", desired.Type)
	}
	handlerStatus := handler.Status()
	logExecution("edge-agent.execution", "handler_selected", "success", map[string]any{
		"node_id":        state.NodeID,
		"workload_id":    desired.WorkloadID,
		"type":           desired.Type,
		"version":        desired.Version,
		"handler_status": handlerStatus.Status,
	}, fmt.Sprintf("[HANDLER] selected type=%s workload=%s status=%s", desired.Type, desired.WorkloadID, handlerStatus.Status))

	logExecution("edge-agent.execution", "workload_execute_started", "started", map[string]any{
		"node_id":     state.NodeID,
		"workload_id": desired.WorkloadID,
		"type":        desired.Type,
		"version":     desired.Version,
	}, fmt.Sprintf("[EXECUTE] workload=%s type=%s version=%d", desired.WorkloadID, desired.Type, desired.Version))

	result := handler.Execute(desired.Spec)
	observed.Workloads[desired.WorkloadID] = ObservedWorkload{
		Version:    desired.Version,
		Type:       desired.Type,
		Status:     result.Status,
		Spec:       desired.Spec,
		LastOutput: result.Output,
		LastError:  result.Error,
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
	}
	if err := saveObservedState(observed); err != nil {
		return err
	}
	logReconciliation("edge-agent.reconcile", "observed_state_updated", "success", map[string]any{
		"node_id":     state.NodeID,
		"workload_id": desired.WorkloadID,
		"type":        desired.Type,
		"version":     desired.Version,
		"status":      result.Status,
	}, fmt.Sprintf("[RECONCILE] observed workload=%s version=%d status=%s", desired.WorkloadID, desired.Version, result.Status))

	status := "success"
	event := "workload_execute_succeeded"
	if !workloadIsApplied(result.Status) {
		status = "error"
		event = "workload_execute_failed"
	}
	logExecution("edge-agent.execution", event, status, map[string]any{
		"node_id":       state.NodeID,
		"workload_id":   desired.WorkloadID,
		"type":          desired.Type,
		"version":       desired.Version,
		"handler_state": result.Status,
		"output":        result.Output,
		"reason":        result.Error,
		"exit_code":     result.ExitCode,
		"retryable":     !workloadIsApplied(result.Status),
		"retry_in_sec":  currentLoopRetrySec(),
	}, fmt.Sprintf("[EXECUTE] workload=%s status=%s", desired.WorkloadID, result.Status))

	if !workloadIsApplied(result.Status) {
		if result.Error != "" {
			return errors.New(result.Error)
		}
		return fmt.Errorf("handler returned status %q", result.Status)
	}

	state.LastAppliedVersion = desired.Version
	savePersistentState(*state)
	return nil
}

func stopObservedWorkloads(state PersistentState, handlers map[string]Handler) {
	observed, err := loadObservedState()
	if err != nil {
		logExecution("edge-agent.execution", "workload_stop_failed", "error", map[string]any{
			"node_id":      state.NodeID,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[STOP] load observed state failed reason=%s", err.Error()))
		return
	}
	if len(observed.Workloads) == 0 {
		return
	}
	if err := stopWorkloadsMissingFromDesired(state, observed, "", handlers); err != nil {
		logExecution("edge-agent.execution", "workload_stop_failed", "error", map[string]any{
			"node_id":      state.NodeID,
			"reason":       err.Error(),
			"retryable":    true,
			"retry_in_sec": currentLoopRetrySec(),
		}, fmt.Sprintf("[STOP] observed cleanup failed reason=%s", err.Error()))
	}
}

func stopWorkloadsMissingFromDesired(state PersistentState, observed ObservedState, desiredWorkloadID string, handlers map[string]Handler) error {
	workloadIDs := make([]string, 0, len(observed.Workloads))
	for workloadID := range observed.Workloads {
		if workloadID != desiredWorkloadID {
			workloadIDs = append(workloadIDs, workloadID)
		}
	}
	sort.Strings(workloadIDs)

	for _, workloadID := range workloadIDs {
		workload := observed.Workloads[workloadID]
		handler, ok := handlers[workload.Type]
		if !ok {
			return fmt.Errorf("unknown workload type %q for observed workload %s", workload.Type, workloadID)
		}

		logExecution("edge-agent.execution", "workload_stop_started", "started", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": workloadID,
			"type":        workload.Type,
			"version":     workload.Version,
		}, fmt.Sprintf("[STOP] workload=%s type=%s", workloadID, workload.Type))

		result := handler.Stop(workload.Spec)
		if workloadIsStopped(result.Status) {
			delete(observed.Workloads, workloadID)
		} else {
			workload.Status = "stop_failed"
			workload.LastError = result.Error
			workload.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			observed.Workloads[workloadID] = workload
		}

		if err := saveObservedState(observed); err != nil {
			return err
		}
		observedStatus := "removed"
		if !workloadIsStopped(result.Status) {
			observedStatus = "stop_failed"
		}
		logReconciliation("edge-agent.reconcile", "observed_state_updated", "success", map[string]any{
			"node_id":     state.NodeID,
			"workload_id": workloadID,
			"type":        workload.Type,
			"version":     workload.Version,
			"status":      observedStatus,
		}, fmt.Sprintf("[RECONCILE] observed workload=%s status=%s", workloadID, observedStatus))

		event := "workload_stop_succeeded"
		status := "success"
		if !workloadIsStopped(result.Status) {
			event = "workload_stop_failed"
			status = "error"
		}
		logExecution("edge-agent.execution", event, status, map[string]any{
			"node_id":       state.NodeID,
			"workload_id":   workloadID,
			"type":          workload.Type,
			"version":       workload.Version,
			"handler_state": result.Status,
			"reason":        result.Error,
			"retryable":     !workloadIsStopped(result.Status),
			"retry_in_sec":  currentLoopRetrySec(),
		}, fmt.Sprintf("[STOP] workload=%s status=%s", workloadID, result.Status))

		if !workloadIsStopped(result.Status) {
			if result.Error != "" {
				return errors.New(result.Error)
			}
			return fmt.Errorf("handler returned stop status %q", result.Status)
		}
	}
	return nil
}

func validateTypedDesiredState(desired DesiredState) error {
	if desired.WorkloadID == "" {
		return errors.New("missing workload_id")
	}
	if desired.Type == "" {
		return errors.New("missing workload type")
	}
	if len(desired.Spec) == 0 {
		return errors.New("missing workload spec")
	}
	return nil
}

func workloadIsApplied(status string) bool {
	return status == "succeeded" || status == "running"
}

func workloadIsStopped(status string) bool {
	return status == "stopped" || status == "succeeded"
}

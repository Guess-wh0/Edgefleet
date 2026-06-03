package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type Handler interface {
	Execute(spec json.RawMessage) HandlerResult
	Stop(spec json.RawMessage) HandlerResult
	Status() HandlerStatus
}

type HandlerResult struct {
	Status   string `json:"status"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exit_code,omitempty"`
}

type HandlerStatus struct {
	Status string `json:"status"`
}

type ScriptHandler struct{}

type ScriptSpec struct {
	Command    string `json:"command"`
	TimeoutSec int    `json:"timeout_sec,omitempty"`
}

// Handlers are intentionally registered by type only; reconciliation decides when to call them.
func defaultHandlers() map[string]Handler {
	return map[string]Handler{
		"script": ScriptHandler{},
	}
}

func (h ScriptHandler) Execute(spec json.RawMessage) HandlerResult {
	var script ScriptSpec
	if err := json.Unmarshal(spec, &script); err != nil {
		return HandlerResult{Status: "failed", Error: fmt.Sprintf("invalid script spec: %v", err)}
	}

	command := strings.TrimSpace(script.Command)
	if command == "" {
		return HandlerResult{Status: "failed", Error: "missing script command"}
	}

	timeout := time.Duration(script.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	name, args := shellCommand(command)
	cmd := exec.CommandContext(ctx, name, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	output := stdout.String()
	errOutput := stderr.String()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return HandlerResult{
			Status: "failed",
			Output: output,
			Error:  fmt.Sprintf("script timed out after %s", timeout),
		}
	}
	if err != nil {
		result := HandlerResult{
			Status: "failed",
			Output: output,
			Error:  strings.TrimSpace(errOutput),
		}
		if result.Error == "" {
			result.Error = err.Error()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exitErr.ExitCode()
		}
		return result
	}

	return HandlerResult{
		Status: "succeeded",
		Output: output,
	}
}

func (h ScriptHandler) Stop(spec json.RawMessage) HandlerResult {
	// Scripts are short-lived commands in Phase 5, so there is no managed process to terminate yet.
	return HandlerResult{Status: "stopped"}
}

func (h ScriptHandler) Status() HandlerStatus {
	return HandlerStatus{Status: "ready"}
}

func shellCommand(command string) (string, []string) {
	// Keep script execution portable while preserving the user's command string.
	if runtime.GOOS == "windows" {
		return "powershell", []string{"-NoProfile", "-Command", command}
	}
	return "sh", []string{"-c", command}
}

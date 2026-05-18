package main

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	controlLogPath = defaultServiceLogPath("control-plane", "control.log")
	controlLogMu   sync.Mutex
)

type observabilityLogEntry struct {
	Category  string         `json:"category"`
	Event     string         `json:"event"`
	Component string         `json:"component"`
	Process   string         `json:"process"`
	Timestamp string         `json:"timestamp"`
	Status    string         `json:"status"`
	Context   map[string]any `json:"context,omitempty"`
}

func logSystem(component, event, status string, context map[string]any, console string) {
	logObservability("system", component, event, status, context, console)
}

func logReconciliation(component, event, status string, context map[string]any, console string) {
	logObservability("reconciliation", component, event, status, context, console)
}

func logExecution(component, event, status string, context map[string]any, console string) {
	logObservability("execution", component, event, status, context, console)
}

func logObservability(category, component, event, status string, context map[string]any, console string) {
	entry := observabilityLogEntry{
		Category:  category,
		Event:     event,
		Component: component,
		Process:   processFromComponent(component),
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Status:    status,
		Context:   context,
	}

	if err := appendJSONLog(controlLogPath, entry, &controlLogMu); err != nil {
		log.Printf("[LOGGER][ERROR] path=%s err=%v", controlLogPath, err)
	}
	if console != "" {
		log.Print(console)
	}
}

func defaultServiceLogPath(serviceDir, fileName string) string {
	wd, err := os.Getwd()
	if err == nil && filepath.Base(wd) == serviceDir {
		return filepath.Join("logs", fileName)
	}
	if info, err := os.Stat(serviceDir); err == nil && info.IsDir() {
		return filepath.Join(serviceDir, "logs", fileName)
	}
	return filepath.Join("logs", fileName)
}

func processFromComponent(component string) string {
	process, _, found := strings.Cut(component, ".")
	if found {
		return process
	}
	return component
}

func appendJSONLog(path string, entry observabilityLogEntry, mu *sync.Mutex) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}

	return nil
}

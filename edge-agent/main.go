package main

import (
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const controlPlaneBase = "http://localhost:8080"

var nodeIDFile = ""

func loadNodeID() string {
	data, err := os.ReadFile(nodeIDFile)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveNodeID(id string) {
	_ = os.WriteFile(nodeIDFile, []byte(id), 0644)
}

func registerNode() string {
	req, _ := http.NewRequest(
		"POST",
		controlPlaneBase+"/register",
		nil,
	)

	req.Header.Set("X-Node-Hostname", getenv("EDGE_HOSTNAME", "edge-sim"))
	req.Header.Set("X-Node-Arch", getenv("EDGE_ARCH", "amd64"))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatal("registration failed:", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	nodeID := strings.TrimSpace(string(body))

	saveNodeID(nodeID)
	log.Println("Registered node:", nodeID)

	return nodeID
}

func sendHeartbeat(nodeID string) {
	req, _ := http.NewRequest(
		"POST",
		controlPlaneBase+"/heartbeat",
		nil,
	)
	req.Header.Set("X-Node-ID", nodeID)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Println("heartbeat error:", err)
		return
	}
	resp.Body.Close()

	log.Println("heartbeat sent")
}

func getenv(key, def string) string {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func main() {
	// connectWiFi() // platform-specific: implemented on Pico (TinyGo)
	nodeDir := getenv("EDGE_NODE_DIR", ".")
	_ = os.MkdirAll(nodeDir, 0755)
	nodeIDFile = filepath.Join(nodeDir, "node_id.txt")
	nodeID := loadNodeID()
	if nodeID == "" {
		nodeID = registerNode()
	}

	for {
		heartbeatEvery := time.Duration(getenvInt("EDGE_HEARTBEAT_SEC", 10)) * time.Second
		sendHeartbeat(nodeID)
		time.Sleep(heartbeatEvery)
	}
}

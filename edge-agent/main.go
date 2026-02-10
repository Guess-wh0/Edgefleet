const (
	controlPlaneBase = "http://localhost:8080"
	nodeIDFile       = "node_id.txt"
	heartbeatEvery   = 10 * time.Second
)

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

	req.Header.Set("X-Node-Hostname", "pico-2w")
	req.Header.Set("X-Node-Arch", "arm")

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

func main() {
	// connectWiFi() // platform-specific: implemented on Pico (TinyGo)

	nodeID := loadNodeID()
	if nodeID == "" {
		nodeID = registerNode()
	}

	for {
		sendHeartbeat(nodeID)
		time.Sleep(heartbeatEvery)
	}
}

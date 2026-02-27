package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/detector"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/publisher"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/registry"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/relay"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/video"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

func main() {
	logger.Info("[pulsar] constellation overwatch sync service")

	// Load .env (optional, don't fail if missing)
	_ = godotenv.Load()
	_ = godotenv.Load("../.env")

	apiKey := os.Getenv("C4_API_KEY")
	baseURL := os.Getenv("C4_BASE_URL")
	natsKey := os.Getenv("C4_NATS_KEY")
	natsURL := envOr("C4_NATS_URL", "nats://localhost:4222")
	fleetPath := envOr("FLEET_CONFIG", "config/fleet.yaml")
	statePath := envOr("C4_STATE_FILE", "config/c4.json")
	rtspHost := envOr("RTSP_HOST", "localhost")
	advertiseHost := shared.PickAdvertiseHost(os.Getenv("ADVERTISE_HOST"))

	if apiKey == "" || baseURL == "" {
		logger.Fatal("[pulsar] C4_API_KEY and C4_BASE_URL are required. Check your .env file.")
	}

	// Load or create fleet config
	fleet, err := loadOrInitFleet(fleetPath)
	if err != nil {
		logger.Fatalf("[pulsar] fleet config error: %v", err)
	}

	// Create Overwatch client and wait for it to be available
	client := shared.NewOverwatchClient(baseURL, apiKey)
	syncInterval := 30 * time.Second

	logger.Info("[pulsar] waiting for overwatch API...")
	if err := registry.WaitForOverwatch(client, 5*time.Minute); err != nil {
		logger.Fatalf("[pulsar] %v", err)
	}

	// Load previous state for entity_id tracking
	previousState := registry.LoadC4State(statePath)
	if previousState != nil {
		logger.Infof("[pulsar] loaded previous state (%d entities)", len(previousState.Entities))
	}

	// Initial registration (entity_id-based reconciliation)
	state, err := registry.Register(client, fleet, natsKey, natsURL, rtspHost, advertiseHost, previousState)
	if err != nil {
		logger.Fatalf("[pulsar] registration failed: %v", err)
	}

	if err := registry.WriteC4State(statePath, state); err != nil {
		logger.Fatalf("[pulsar] failed to write state: %v", err)
	}

	printSummary(state, statePath)

	// Connect to NATS for streaming
	pub, err := publisher.NewOverwatchPublisher(natsURL, natsKey, state.PulsarID)
	if err != nil {
		logger.Warnf("[pulsar] NATS connection failed: %v (running without streaming)", err)
	}
	if pub != nil {
		defer pub.Close()
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start background services if we have entities
	var serviceCancel context.CancelFunc
	if len(state.Entities) > 0 && pub != nil {
		serviceCancel = startServices(ctx, state, pub)
	}

	// Sync loop: watch fleet.yaml for changes and re-register
	lastHash, _ := registry.FleetConfigHash(fleetPath)
	ticker := time.NewTicker(syncInterval)
	defer ticker.Stop()

	logger.Infof("[pulsar] sync loop active (every %s), press Ctrl+C to stop", syncInterval)

	for {
		select {
		case <-ctx.Done():
			logger.Info("[pulsar] shutting down...")
			if serviceCancel != nil {
				serviceCancel()
			}
			return
		case <-ticker.C:
			currentHash, err := registry.FleetConfigHash(fleetPath)
			if err != nil {
				logger.Warnf("[pulsar] could not hash fleet config: %v", err)
				continue
			}

			if currentHash == lastHash {
				continue
			}

			logger.Info("[pulsar] fleet.yaml changed, re-syncing...")
			lastHash = currentHash

			newFleet, err := loadOrInitFleet(fleetPath)
			if err != nil {
				logger.Warnf("[pulsar] failed to reload fleet config: %v", err)
				continue
			}

			newState, err := registry.Register(client, newFleet, natsKey, natsURL, rtspHost, advertiseHost, state)
			if err != nil {
				logger.Warnf("[pulsar] re-registration failed: %v", err)
				continue
			}

			if err := registry.WriteC4State(statePath, newState); err != nil {
				logger.Warnf("[pulsar] failed to write state: %v", err)
			}

			// Cancel old services and restart with new state
			if serviceCancel != nil {
				serviceCancel()
			}
			state = newState
			printSummary(state, statePath)

			if len(state.Entities) > 0 && pub != nil {
				serviceCancel = startServices(ctx, state, pub)
			}
		}
	}
}

func printSummary(state *shared.C4State, statePath string) {
	fmt.Println()
	fmt.Println("=== Pulsar Registration Complete ===")
	fmt.Printf("  Pulsar ID:    %s\n", state.PulsarID)
	fmt.Printf("  Organization: %s (%s)\n", state.OrgName, state.OrgID)
	fmt.Printf("  Entities:     %d registered\n", len(state.Entities))
	for _, e := range state.Entities {
		suffix := ""
		if e.MavlinkPort > 0 {
			suffix = fmt.Sprintf(" | mavlink:%d", e.MavlinkPort)
		}
		fmt.Printf("    - %s [%s] -> %s%s\n", e.Name, e.Type, e.RTSPURL, suffix)
	}
	fmt.Printf("  State file:   %s\n", statePath)
	fmt.Println()
}

// startServices launches MAVLink relay, video bridge, and detector in a child context.
// Returns the cancel function to stop them for restart.
func startServices(parent context.Context, state *shared.C4State, pub *publisher.OverwatchPublisher) context.CancelFunc {
	svcCtx, svcCancel := context.WithCancel(parent)
	relay.StartRelay(svcCtx, state, pub)

	// Video: smart-detect MediaMTX or start embedded RTSP server
	rtspHost := envOr("RTSP_HOST", "localhost")
	srv := video.NewRTSPServer(rtspHost, 8554)

	// Bridge video sources to normalized entity_id paths
	cleanupBridge := video.StartBridge(svcCtx, state, srv)

	// Create overlay writers per entity (for post-inference annotated streams)
	overlayWriters := make(map[string]*video.OverlayWriter)
	for _, entity := range state.Entities {
		if entity.RTSPURL == "" {
			continue
		}
		ow, err := video.NewOverlayWriter(entity.EntityID, rtspHost, 8554)
		if err != nil {
			logger.Warnf("[pulsar] overlay writer for %s: %v (skipping overlay)", entity.Name, err)
			continue
		}
		if ow != nil {
			overlayWriters[entity.EntityID] = ow
		}
	}

	modelPath := envOr("MODEL_PATH", "data/yolo26s.onnx")
	onnxLibPath := envOr("ONNX_LIB_PATH", "")
	detector.StartDetector(svcCtx, state.Entities, state.OrgID, pub, modelPath, onnxLibPath, overlayWriters)

	logger.Infof("[pulsar] services started for %d entities", len(state.Entities))
	return func() {
		svcCancel()
		cleanupBridge()
		for _, ow := range overlayWriters {
			ow.Close()
		}
		srv.Close()
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// loadOrInitFleet loads fleet.yaml or runs guided setup if it doesn't exist.
func loadOrInitFleet(path string) (*shared.FleetConfig, error) {
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		var fleet shared.FleetConfig
		if err := yaml.Unmarshal(data, &fleet); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if fleet.Organization.Name != "" && len(fleet.Entities) > 0 {
			logger.Infof("[pulsar] loaded fleet config from %s (%d entities)", path, len(fleet.Entities))
			return &fleet, nil
		}
	}

	logger.Info("[pulsar] no fleet config found, starting guided setup...")
	fleet, err := guidedInit()
	if err != nil {
		return nil, fmt.Errorf("guided init: %w", err)
	}

	out, err := yaml.Marshal(fleet)
	if err != nil {
		return nil, fmt.Errorf("marshal fleet: %w", err)
	}
	if err := os.WriteFile(path, out, 0644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}
	logger.Infof("[pulsar] saved fleet config to %s", path)

	return fleet, nil
}

// guidedInit walks the user through first-time setup.
func guidedInit() (*shared.FleetConfig, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println()
	fmt.Println("=== Pulsar First-Time Setup ===")
	fmt.Println()

	orgName := prompt(reader, "Organization name", "GCS Alpha Station")
	fmt.Println("  Organization types: military, civilian, commercial, ngo")
	orgType := prompt(reader, "Organization type", "civilian")
	orgDesc := prompt(reader, "Description (optional)", "Rapid response ground control station")

	countStr := prompt(reader, "How many entities to register?", "1")
	count, err := strconv.Atoi(countStr)
	if err != nil || count < 1 {
		count = 1
	}

	fmt.Println()
	fmt.Println("  Entity types: uav, isr_sensor, camera, ground_vehicle, boat, fixed_wing")
	fmt.Println("  Priorities: low, normal, high, critical")
	fmt.Println()

	var entities []shared.EntityConfig
	for i := 0; i < count; i++ {
		fmt.Printf("--- Entity %d of %d ---\n", i+1, count)

		name := prompt(reader, "  Entity name", fmt.Sprintf("Entity %d", i+1))
		eType := prompt(reader, "  Entity type", "uav")
		priority := prompt(reader, "  Priority", "normal")

		var mavlink *shared.MavlinkConfig
		mavEnable := prompt(reader, fmt.Sprintf("  Enable MAVLink telemetry? (y/n, ports auto-assigned from %d)", shared.MavlinkBasePort()), "y")
		if strings.EqualFold(mavEnable, "y") || strings.EqualFold(mavEnable, "yes") {
			mavlink = &shared.MavlinkConfig{Protocol: "udp"}
		}

		var vc map[string]interface{}
		videoEnable := prompt(reader, "  Enable video stream? (y/n)", "n")
		if strings.EqualFold(videoEnable, "y") || strings.EqualFold(videoEnable, "yes") {
			vc = map[string]interface{}{"protocol": "rtsp", "port": 8554}
			fmt.Println("    Video source types:")
			fmt.Println("      rtsp   - Network RTSP source (camera, MediaMTX, etc.)")
			fmt.Println("      device - Local capture device (/dev/video0)")
			srcType := prompt(reader, "  Video source type", "rtsp")
			if strings.EqualFold(srcType, "device") {
				device := prompt(reader, "  Device path", "/dev/video0")
				vc["device"] = device
			} else {
				source := prompt(reader, "  RTSP source URL (e.g., rtsp://user:pass@192.168.1.50:554/stream)", "")
				if source != "" {
					vc["source"] = source
				}
			}
		}

		entities = append(entities, shared.EntityConfig{
			Name:        name,
			Type:        eType,
			Priority:    priority,
			Status:      "active",
			VideoConfig: vc,
			Mavlink:     mavlink,
		})
		fmt.Println()
	}

	// Summary before saving
	mavCount := 0
	vidCount := 0
	for _, e := range entities {
		if e.Mavlink != nil {
			mavCount++
		}
		if e.VideoConfig != nil {
			vidCount++
		}
	}
	fmt.Println("=== Fleet Summary ===")
	fmt.Printf("  Organization: %s (%s)\n", orgName, orgType)
	fmt.Printf("  Entities:     %d total, %d with MAVLink, %d with video\n", len(entities), mavCount, vidCount)
	for _, e := range entities {
		streams := ""
		if e.Mavlink != nil {
			streams += " mavlink"
		}
		if e.VideoConfig != nil {
			if src, ok := e.VideoConfig["source"].(string); ok && src != "" {
				streams += fmt.Sprintf(" video(rtsp:%s)", src)
			} else if dev, ok := e.VideoConfig["device"].(string); ok && dev != "" {
				streams += fmt.Sprintf(" video(device:%s)", dev)
			} else {
				streams += " video"
			}
		}
		if streams == "" {
			streams = " (no streams)"
		}
		fmt.Printf("    - %s [%s] ->%s\n", e.Name, e.Type, streams)
	}
	fmt.Println()

	return &shared.FleetConfig{
		Organization: shared.OrgConfig{
			Name:        orgName,
			Type:        orgType,
			Description: orgDesc,
		},
		Entities: entities,
	}, nil
}

func prompt(reader *bufio.Reader, label, defaultVal string) string {
	if defaultVal != "" {
		fmt.Printf("  %s [%s]: ", label, defaultVal)
	} else {
		fmt.Printf("  %s: ", label)
	}
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return defaultVal
	}
	return line
}

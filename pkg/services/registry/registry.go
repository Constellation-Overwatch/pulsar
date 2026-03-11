package registry

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/google/uuid"
)

// Register performs idempotent org + entity registration against Overwatch.
// Uses previousState (from c4.json) to track entities by entity_id across restarts.
// RegisterOptions holds optional flags that modify registration behavior.
type RegisterOptions struct {
	// ForceVideoSync forces a video endpoint update on all entities,
	// even if fleet config hasn't drifted. Use when ADVERTISE_HOST or
	// ADVERTISE_HTTPS changes in .env.
	ForceVideoSync bool
}

func Register(client *shared.OverwatchClient, fleet *shared.FleetConfig, natsKey, natsURL, rtspHost, advertiseHost string, advertiseHTTPS bool, previousState *shared.C4State, opts ...RegisterOptions) (*shared.C4State, error) {
	var opt RegisterOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	// 1. Health check
	logger.Info("[pulsar] checking overwatch health...")
	health, err := client.HealthCheck()
	if err != nil {
		return nil, fmt.Errorf("overwatch health check failed: %w", err)
	}
	logger.Infof("[pulsar] overwatch is %s (%s v%s)", health.Status, health.Service, health.Version)

	// 2. Find or create organization
	logger.Infof("[pulsar] looking for organization %q...", fleet.Organization.Name)
	org, err := findOrCreateOrg(client, fleet.Organization)
	if err != nil {
		return nil, fmt.Errorf("organization registration failed: %w", err)
	}
	logger.Infof("[pulsar] organization: %s (id: %s)", org.Name, org.OrgID)

	// 3. Fetch current remote entities
	remoteEntities, err := client.ListEntities(org.OrgID)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}

	// Build indexes: entity_id -> remote entity, name -> remote entity
	remoteByID := make(map[string]*shared.Entity)
	remoteByName := make(map[string]*shared.Entity)
	for i := range remoteEntities {
		remoteByID[remoteEntities[i].EntityID] = &remoteEntities[i]
		remoteByName[strings.ToLower(remoteEntities[i].Name)] = &remoteEntities[i]
	}

	// Build previous state index: fleet name -> entity_id (from c4.json)
	prevEntityID := make(map[string]string) // name(lower) -> entity_id
	if previousState != nil {
		for _, e := range previousState.Entities {
			prevEntityID[strings.ToLower(e.Name)] = e.EntityID
		}
	}

	// 4. Reconcile each fleet entity by entity_id first, then name
	desiredEntityIDs := make(map[string]bool) // track which entity_ids we want to keep
	var registeredEntities []shared.EntityState

	for _, ec := range fleet.Entities {
		apiType := shared.ResolveEntityType(ec.Type)
		var entity *shared.Entity

		// Try to find by entity_id from previous state
		if knownID, ok := prevEntityID[strings.ToLower(ec.Name)]; ok {
			if remote, exists := remoteByID[knownID]; exists {
				entity = remote
				if !needsDriftUpdate(remote, ec, apiType) {
					logger.Infof("[pulsar] entity %q (id: %s) in sync", ec.Name, knownID)
				}
			} else {
				logger.Infof("[pulsar] entity %q (id: %s) missing from server, re-provisioning...", ec.Name, knownID)
			}
		}

		// Fallback: try to find by name if not resolved by entity_id
		if entity == nil {
			if remote, exists := remoteByName[strings.ToLower(ec.Name)]; exists {
				entity = remote
				logger.Infof("[pulsar] found existing entity %q by name (id: %s)", ec.Name, remote.EntityID)
			}
		}

		// Build RTSP port from video_config
		rtspPort := 8554
		if ec.VideoConfig != nil {
			if p, ok := ec.VideoConfig["port"].(int); ok {
				rtspPort = p
			}
			if p, ok := ec.VideoConfig["port"].(float64); ok {
				rtspPort = int(p)
			}
		}

		// Build advertised video_config for Overwatch (externally consumable URLs).
		// This replaces the local device/source config with reachable endpoints.
		var advertisedVC map[string]interface{}
		buildAdvertisedVC := func(entityID string) map[string]interface{} {
			if ec.VideoConfig == nil {
				return nil
			}
			return shared.FormatVideoEndpoints(advertiseHost, rtspPort, entityID, advertiseHTTPS)
		}

		// Create or update entity on Overwatch
		drifted := entity != nil && needsDriftUpdate(entity, ec, apiType)
		if drifted {
			advertisedVC = buildAdvertisedVC(entity.EntityID)
			logger.Infof("[pulsar] drift detected for %q (id: %s), updating...", ec.Name, entity.EntityID)
			updated, err := client.UpdateEntity(org.OrgID, entity.EntityID, shared.UpdateEntityRequest{
				Name:        ec.Name,
				Status:      ec.Status,
				Priority:    ec.Priority,
				VideoConfig: advertisedVC,
			})
			if err != nil {
				logger.Warnf("[pulsar] failed to update entity %q: %v", ec.Name, err)
			} else {
				entity = updated
			}
		}

		if entity == nil {
			// Create with placeholder video_config (entity_id not yet known)
			logger.Infof("[pulsar] creating entity %q [%s -> %s]...", ec.Name, ec.Type, apiType)
			created, err := client.CreateEntity(org.OrgID, shared.CreateEntityRequest{
				Name:       ec.Name,
				EntityType: apiType,
				Status:     ec.Status,
				Priority:   ec.Priority,
			})
			if err != nil {
				logger.Warnf("[pulsar] failed to create entity %q: %v", ec.Name, err)
				continue
			}
			entity = created

			// Now that we have entity_id, push the advertised video_config
			advertisedVC = buildAdvertisedVC(entity.EntityID)
			if advertisedVC != nil {
				if _, err := client.UpdateEntity(org.OrgID, entity.EntityID, shared.UpdateEntityRequest{
					VideoConfig: advertisedVC,
				}); err != nil {
					logger.Warnf("[pulsar] failed to set video endpoints for %q: %v", ec.Name, err)
				}
			}
		} else if !drifted && ec.VideoConfig != nil && (advertisedVC == nil || opt.ForceVideoSync) {
			// Entity exists, no fleet drift, but video endpoints need syncing:
			// either first time (advertisedVC == nil) or env changed (ForceVideoSync)
			advertisedVC = buildAdvertisedVC(entity.EntityID)
			if advertisedVC != nil {
				if opt.ForceVideoSync {
					logger.Infof("[pulsar] force-syncing video endpoints for %q", ec.Name)
				}
				if _, err := client.UpdateEntity(org.OrgID, entity.EntityID, shared.UpdateEntityRequest{
					VideoConfig: advertisedVC,
				}); err != nil {
					logger.Warnf("[pulsar] failed to sync video endpoints for %q: %v", ec.Name, err)
				}
			}
		}

		desiredEntityIDs[entity.EntityID] = true

		// Local RTSP URL (for Pulsar's own detector/bridge)
		rtspURL := fmt.Sprintf("rtsp://%s:%d/%s", rtspHost, rtspPort, entity.EntityID)

		// Resolve video source from video_config (local ingest)
		var videoSource string
		if ec.VideoConfig != nil {
			if src, ok := ec.VideoConfig["source"].(string); ok && src != "" {
				videoSource = src
			}
		}

		// mavlink port: -1 = auto-assign, >0 = explicit, 0 = disabled
		var mavlinkPort int
		if ec.Mavlink != nil {
			if ec.Mavlink.Port > 0 {
				mavlinkPort = ec.Mavlink.Port
			} else {
				mavlinkPort = -1 // auto-assign
			}
		}

		// commands: only enable if both commands flag and mavlink are set
		commandsEnabled := ec.Commands != nil && *ec.Commands && ec.Mavlink != nil
		if ec.Commands != nil && *ec.Commands && ec.Mavlink == nil {
			logger.Warnf("[pulsar] entity %q has commands: true but no mavlink config — commands disabled", ec.Name)
		}

		registeredEntities = append(registeredEntities, shared.EntityState{
			EntityID:        entity.EntityID,
			Name:            entity.Name,
			Type:            entity.EntityType,
			StreamPath:      entity.EntityID,
			RTSPURL:         rtspURL,
			MavlinkPort:     mavlinkPort,
			CommandsEnabled: commandsEnabled,
			VideoConfig:     ec.VideoConfig, // local ingest config (device/source)
			VideoSource:     videoSource,
			AdvertisedVideo: advertisedVC,   // what was pushed to Overwatch
		})

		logger.Infof("[pulsar] entity: %s [%s] (id: %s) -> %s", entity.Name, entity.EntityType, entity.EntityID, rtspURL)
		if advertisedVC != nil {
			logger.Infof("[pulsar]   advertised: %s", advertisedVC["stream_url"])
		}
	}

	// 4b. Resolve auto-assigned MAVLink ports
	shared.ResolveMavlinkPorts(registeredEntities, shared.MavlinkBasePort())
	for _, e := range registeredEntities {
		if e.MavlinkPort > 0 {
			logger.Infof("[pulsar] mavlink: %s -> UDP :%d", e.Name, e.MavlinkPort)
		}
	}

	// 5. Cleanup: remove remote entities not in desired set
	for _, re := range remoteEntities {
		if !desiredEntityIDs[re.EntityID] {
			logger.Infof("[pulsar] removing stale entity %q (id: %s)", re.Name, re.EntityID)
			if err := client.DeleteEntity(org.OrgID, re.EntityID); err != nil {
				logger.Warnf("[pulsar] failed to delete stale entity %q: %v", re.Name, err)
			}
		}
	}

	// Carry forward pulsar_id from previous state, or generate a new one
	pulsarID := ""
	if previousState != nil && previousState.PulsarID != "" {
		pulsarID = previousState.PulsarID
	} else {
		pulsarID = "pulsar-" + uuid.NewString()[:8]
		logger.Infof("[pulsar] generated new pulsar_id: %s", pulsarID)
	}

	state := &shared.C4State{
		PulsarID:       pulsarID,
		OrgID:          org.OrgID,
		OrgName:        org.Name,
		APIURL:         client.BaseURL,
		NATSKey:        natsKey,
		NATSURL:        natsURL,
		AdvertiseHost:  advertiseHost,
		AdvertiseHTTPS: advertiseHTTPS,
		RTSPHost:       rtspHost,
		Entities:       registeredEntities,
		RegisteredAt:   time.Now().UTC(),
	}

	return state, nil
}

// needsDriftUpdate checks if a remote entity has drifted from desired config.
func needsDriftUpdate(remote *shared.Entity, desired shared.EntityConfig, apiType string) bool {
	if remote.Status != desired.Status {
		return true
	}
	if remote.Priority != desired.Priority {
		return true
	}
	if remote.EntityType != apiType {
		return true
	}
	if remote.Name != desired.Name {
		return true
	}
	return false
}

func findOrCreateOrg(client *shared.OverwatchClient, cfg shared.OrgConfig) (*shared.Organization, error) {
	orgs, err := client.ListOrganizations()
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}

	for i := range orgs {
		if strings.EqualFold(orgs[i].Name, cfg.Name) {
			logger.Infof("[pulsar] found existing organization %q", cfg.Name)
			return &orgs[i], nil
		}
	}

	logger.Infof("[pulsar] creating organization %q...", cfg.Name)
	org, err := client.CreateOrganization(shared.CreateOrganizationRequest{
		Name:        cfg.Name,
		OrgType:     shared.ResolveOrgType(cfg.Type),
		Description: cfg.Description,
	})
	if err != nil {
		return nil, fmt.Errorf("create organization: %w", err)
	}

	return org, nil
}

// WaitForOverwatch retries the health check with exponential backoff until
// Overwatch is available or maxWait is exceeded.
func WaitForOverwatch(client *shared.OverwatchClient, maxWait time.Duration) error {
	deadline := time.Now().Add(maxWait)
	wait := 2 * time.Second

	for {
		health, err := client.HealthCheck()
		if err == nil {
			logger.Infof("[pulsar] overwatch is %s (%s v%s)", health.Status, health.Service, health.Version)
			return nil
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("overwatch not reachable after %s: %w", maxWait, err)
		}

		logger.Warnf("[pulsar] waiting for overwatch... (%v)", err)
		time.Sleep(wait)
		if wait < 30*time.Second {
			wait = wait * 2
		}
	}
}

// CheckEntityHealth verifies all registered entities still exist on Overwatch.
// Returns the list of missing entities (empty if all healthy).
func CheckEntityHealth(client *shared.OverwatchClient, state *shared.C4State) []shared.EntityState {
	entities, err := client.ListEntities(state.OrgID)
	if err != nil {
		return nil // can't reach API, assume OK
	}

	remoteIDs := make(map[string]bool, len(entities))
	for _, e := range entities {
		remoteIDs[e.EntityID] = true
	}

	var missing []shared.EntityState
	for _, e := range state.Entities {
		if !remoteIDs[e.EntityID] {
			missing = append(missing, e)
		}
	}
	return missing
}

// FleetConfigHash returns a sha256 hash of the fleet config file for change detection.
func FleetConfigHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h), nil
}

// EnvFileHash returns a sha256 hash of the .env file for change detection.
// Returns empty string if the file doesn't exist (not an error).
func EnvFileHash(paths ...string) string {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		h := sha256.Sum256(data)
		return fmt.Sprintf("%x", h)
	}
	return ""
}

// LoadC4State loads previous c4.json state for entity_id tracking.
// Returns nil if file doesn't exist or is unreadable.
func LoadC4State(path string) *shared.C4State {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var state shared.C4State
	if err := json.Unmarshal(data, &state); err != nil {
		return nil
	}
	return &state
}

// WriteC4State writes the c4.json state file.
func WriteC4State(path string, state *shared.C4State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	logger.Infof("[pulsar] wrote %s", path)
	return nil
}

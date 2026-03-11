package shared

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// FleetConfig is the fleet.yaml structure.
type FleetConfig struct {
	Organization OrgConfig      `yaml:"organization" json:"organization"`
	Entities     []EntityConfig `yaml:"entities" json:"entities"`
}

type OrgConfig struct {
	Name        string `yaml:"name" json:"name"`
	Type        string `yaml:"type" json:"type"`
	Description string `yaml:"description" json:"description,omitempty"`
}

type EntityConfig struct {
	Name        string                 `yaml:"name" json:"name"`
	Type        string                 `yaml:"type" json:"type"`
	Priority    string                 `yaml:"priority" json:"priority"`
	Status      string                 `yaml:"status" json:"status"`
	VideoConfig map[string]interface{} `yaml:"video_config" json:"video_config,omitempty"`
	Mavlink     *MavlinkConfig         `yaml:"mavlink" json:"mavlink,omitempty"`
	Commands    *bool                  `yaml:"commands" json:"commands,omitempty"`
}

type MavlinkConfig struct {
	Port     int    `yaml:"port" json:"port"`
	Protocol string `yaml:"protocol" json:"protocol,omitempty"`
}

// MarshalYAML emits "true" when port is auto-assigned (Port==0),
// otherwise emits the full struct for explicit port overrides.
func (m MavlinkConfig) MarshalYAML() (interface{}, error) {
	if m.Port == 0 {
		return true, nil
	}
	type plain MavlinkConfig
	return plain(m), nil
}

// UnmarshalYAML supports both "mavlink: true" (auto-assign port) and
// "mavlink: {port: N}" (explicit port) in fleet.yaml.
func (m *MavlinkConfig) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var b bool
		if err := value.Decode(&b); err == nil && b {
			m.Protocol = "udp"
			return nil
		}
		return fmt.Errorf("mavlink: expected true or mapping, got %q", value.Value)
	}
	type plain MavlinkConfig
	if err := value.Decode((*plain)(m)); err != nil {
		return err
	}
	if m.Protocol == "" {
		m.Protocol = "udp"
	}
	return nil
}

// C4State is the c4.json output file structure.
type C4State struct {
	PulsarID       string        `json:"pulsar_id"`
	OrgID          string        `json:"org_id"`
	OrgName        string        `json:"org_name"`
	APIURL         string        `json:"api_url"`
	NATSKey        string        `json:"nats_key"`
	NATSURL        string        `json:"nats_url"`
	AdvertiseHost  string        `json:"advertise_host"`
	AdvertiseHTTPS bool          `json:"advertise_https"`
	RTSPHost       string        `json:"rtsp_host"`
	Entities       []EntityState `json:"entities"`
	RegisteredAt   time.Time     `json:"registered_at"`
}

type EntityState struct {
	EntityID        string                 `json:"entity_id"`
	Name            string                 `json:"name"`
	Type            string                 `json:"type"`
	StreamPath      string                 `json:"stream_path"`
	RTSPURL         string                 `json:"rtsp_url"`
	MavlinkPort     int                    `json:"mavlink_port,omitempty"`
	CommandsEnabled bool                   `json:"commands_enabled,omitempty"`
	VideoConfig     map[string]interface{} `json:"video_config,omitempty"`
	VideoSource     string                 `json:"video_source,omitempty"`
	AdvertisedVideo map[string]interface{} `json:"advertised_video,omitempty"`
}

// orgTypeMap maps friendly fleet.yaml names to API org_type enum values.
var orgTypeMap = map[string]string{
	"military":   "military",
	"civilian":   "civilian",
	"commercial": "commercial",
	"ngo":        "ngo",
	// friendly aliases
	"mil":        "military",
	"civ":        "civilian",
	"company":    "commercial",
	"enterprise": "commercial",
	"nonprofit":  "ngo",
	"agency":     "civilian",
}

// ResolveOrgType maps a friendly org type name to the API enum value.
func ResolveOrgType(t string) string {
	lower := strings.ToLower(strings.TrimSpace(t))
	if mapped, ok := orgTypeMap[lower]; ok {
		return mapped
	}
	return lower
}

// entityTypeMap maps friendly fleet.yaml names to API enum values.
var entityTypeMap = map[string]string{
	"uav":            "aircraft_multirotor",
	"multirotor":     "aircraft_multirotor",
	"fixed_wing":     "aircraft_fixed_wing",
	"vtol":           "aircraft_vtol",
	"helicopter":     "aircraft_helicopter",
	"airship":        "aircraft_airship",
	"ground_vehicle": "ground_vehicle_wheeled",
	"wheeled":        "ground_vehicle_wheeled",
	"tracked":        "ground_vehicle_tracked",
	"boat":           "surface_vessel_usv",
	"usv":            "surface_vessel_usv",
	"submarine":      "underwater_vehicle",
	"auv":            "underwater_vehicle",
	"isr_sensor":     "sensor_platform",
	"sensor":         "sensor_platform",
	"camera":         "sensor_platform",
	"payload":        "payload_system",
	"gcs":            "operator_station",
	"operator":       "operator_station",
	"waypoint":       "waypoint",
	"no_fly_zone":    "no_fly_zone",
	"geofence":       "geofence",
}

// ResolveEntityType maps a friendly type name to the API enum value.
func ResolveEntityType(t string) string {
	lower := strings.ToLower(strings.TrimSpace(t))
	if mapped, ok := entityTypeMap[lower]; ok {
		return mapped
	}
	return lower
}

// validPriorities defines the accepted priority values.
var validPriorities = map[string]bool{
	"low":      true,
	"normal":   true,
	"high":     true,
	"critical": true,
}

// IsValidEntityType returns true if t is a recognized entity type alias.
func IsValidEntityType(t string) bool {
	_, ok := entityTypeMap[strings.ToLower(strings.TrimSpace(t))]
	return ok
}

// IsValidOrgType returns true if t is a recognized organization type alias.
func IsValidOrgType(t string) bool {
	_, ok := orgTypeMap[strings.ToLower(strings.TrimSpace(t))]
	return ok
}

// IsValidPriority returns true if t is a recognized priority value.
func IsValidPriority(t string) bool {
	return validPriorities[strings.ToLower(strings.TrimSpace(t))]
}

// ValidEntityTypes returns all recognized entity type aliases, sorted.
func ValidEntityTypes() []string {
	keys := make([]string, 0, len(entityTypeMap))
	for k := range entityTypeMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidOrgTypes returns all recognized org type aliases, sorted.
func ValidOrgTypes() []string {
	keys := make([]string, 0, len(orgTypeMap))
	for k := range orgTypeMap {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidPriorities returns all recognized priority values, sorted.
func ValidPriorities() []string {
	keys := make([]string, 0, len(validPriorities))
	for k := range validPriorities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// MavlinkBasePort returns the base port from MAVLINK_BASE_PORT env (default 14550).
func MavlinkBasePort() int {
	if v := os.Getenv("MAVLINK_BASE_PORT"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return 14550
}

// ResolveMavlinkPorts assigns sequential ports to entities with MavlinkPort == -1
// (auto-assign marker), starting from basePort and skipping any explicitly set ports.
func ResolveMavlinkPorts(entities []EntityState, basePort int) {
	used := map[int]bool{}
	for _, e := range entities {
		if e.MavlinkPort > 0 {
			used[e.MavlinkPort] = true
		}
	}
	next := basePort
	for i := range entities {
		if entities[i].MavlinkPort == -1 {
			for used[next] {
				next++
			}
			entities[i].MavlinkPort = next
			used[next] = true
			next++
		}
	}
}

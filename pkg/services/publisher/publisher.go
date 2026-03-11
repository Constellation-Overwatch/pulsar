package publisher

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/nats-io/nkeys"
)

// OverwatchPublisher handles all NATS JetStream + KV publishing to Constellation Overwatch.
type OverwatchPublisher struct {
	nc *nats.Conn
	js jetstream.JetStream
	kv jetstream.KeyValue
}

// ── Constellation Ontology: KV State Payload ────────────────────────────────
// Matches https://constellation-overwatch.dev/docs/concepts/telemetry

// EntityKVState is the top-level payload stored per entity in CONSTELLATION_GLOBAL_STATE.
type EntityKVState struct {
	EntityID   string `json:"entity_id"`
	OrgID      string `json:"org_id"`
	OrgName    string `json:"org_name"`
	Name       string `json:"name"`
	EntityType string `json:"entity_type"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	IsLive     bool   `json:"is_live"`
	FirstSeen  string `json:"first_seen,omitempty"`
	LastSeen   string `json:"last_seen"`

	Position      *PositionState      `json:"position,omitempty"`
	Attitude      *AttitudeState      `json:"attitude,omitempty"`
	VehicleStatus *VehicleStatusState `json:"vehicle_status,omitempty"`
	Power         *PowerState         `json:"power,omitempty"`
	VFR           *VFRState           `json:"vfr,omitempty"`
	Detections    *DetectionsState    `json:"detections,omitempty"`
}

type PositionState struct {
	Global *PositionGlobal `json:"global,omitempty"`
	Local  *PositionLocal  `json:"local,omitempty"`
}

type PositionGlobal struct {
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	AltitudeMSL      float64 `json:"altitude_msl"`
	AltitudeRelative float64 `json:"altitude_relative"`
	Heading          float64 `json:"heading"`
	Timestamp        string  `json:"timestamp"`
}

type PositionLocal struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Z         float64 `json:"z"`
	Vx        float64 `json:"vx"`
	Vy        float64 `json:"vy"`
	Vz        float64 `json:"vz"`
	Timestamp string  `json:"timestamp"`
}

type AttitudeState struct {
	Euler *AttitudeEuler `json:"euler,omitempty"`
}

type AttitudeEuler struct {
	Roll       float64 `json:"roll"`
	Pitch      float64 `json:"pitch"`
	Yaw        float64 `json:"yaw"`
	RollSpeed  float64 `json:"rollspeed"`
	PitchSpeed float64 `json:"pitchspeed"`
	YawSpeed   float64 `json:"yawspeed"`
	Timestamp  string  `json:"timestamp"`
}

type VehicleStatusState struct {
	Armed          bool   `json:"armed"`
	Mode           string `json:"mode"`
	CustomMode     int    `json:"custom_mode"`
	Autopilot      int    `json:"autopilot"`
	SystemStatus   int    `json:"system_status"`
	VehicleType    int    `json:"vehicle_type"`
	Load           int    `json:"load"`
	SensorsEnabled int    `json:"sensors_enabled"`
	SensorsHealth  int    `json:"sensors_health"`
	Timestamp      string `json:"timestamp"`
}

type PowerState struct {
	Voltage          float64 `json:"voltage"`
	Current          float64 `json:"current"`
	BatteryRemaining int     `json:"battery_remaining"`
	Timestamp        string  `json:"timestamp"`
}

type VFRState struct {
	Airspeed    float64 `json:"airspeed"`
	Groundspeed float64 `json:"groundspeed"`
	Heading     int     `json:"heading"`
	ClimbRate   float64 `json:"climb_rate"`
	Throttle    int     `json:"throttle"`
	Altitude    float64 `json:"altitude"`
	Timestamp   string  `json:"timestamp"`
}

type DetectionsState struct {
	Status     string                     `json:"status"`
	IsLive     bool                       `json:"is_live"`
	FrameCount int64                      `json:"frame_count"`
	Timestamp  string                     `json:"timestamp"`
	Objects    map[string]*DetectedObject `json:"objects,omitempty"`
}

type DetectedObject struct {
	Label      string  `json:"label"`
	Confidence float64 `json:"confidence"`
	BBox       BBox    `json:"bbox"`
	CX         float64 `json:"cx"`
	CY         float64 `json:"cy"`
	DX         float64 `json:"dx"`
	DY         float64 `json:"dy"`
	FrameCount int     `json:"frame_count"`
	FirstSeen  string  `json:"first_seen"`
	LastSeen   string  `json:"last_seen"`
}

type BBox struct {
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
	X2 float64 `json:"x2"`
	Y2 float64 `json:"y2"`
}

// EntityMeta carries org/entity metadata needed for KV state publishing.
type EntityMeta struct {
	EntityID   string
	OrgID      string
	OrgName    string
	Name       string
	EntityType string
	Status     string
	Priority   string
}

// NewOverwatchPublisher connects to NATS and ensures streams + KV bucket exist.
func NewOverwatchPublisher(natsURL, natsKey, pulsarID string) (*OverwatchPublisher, error) {
	opts := []nats.Option{
		nats.Name(pulsarID),
		nats.ReconnectWait(2 * time.Second),
		nats.MaxReconnects(-1),
		nats.Timeout(10 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				logger.Warnf("[nats] disconnected: %v", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			logger.Info("[nats] reconnected")
		}),
	}

	// Use nkey auth if seed provided
	if natsKey != "" {
		kp, err := nkeys.FromSeed([]byte(natsKey))
		if err != nil {
			return nil, fmt.Errorf("parse nats nkey seed: %w", err)
		}
		pub, err := kp.PublicKey()
		if err != nil {
			return nil, fmt.Errorf("derive nkey public key: %w", err)
		}
		opts = append(opts, nats.Nkey(pub, func(nonce []byte) ([]byte, error) {
			return kp.Sign(nonce)
		}))
	}

	nc, err := nats.Connect(natsURL, opts...)
	if err != nil {
		return nil, fmt.Errorf("connect to nats at %s: %w", natsURL, err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("create jetstream context: %w", err)
	}

	ctx := context.Background()

	// Ensure telemetry stream
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "CONSTELLATION_TELEMETRY",
		Subjects: []string{"constellation.telemetry.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   24 * time.Hour,
	})
	if err != nil {
		logger.Warnf("[nats] telemetry stream: %v", err)
	}

	// Ensure events stream (with 2s dedup window for idempotent detection publishing)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:       "CONSTELLATION_EVENTS",
		Subjects:   []string{"constellation.events.>"},
		Storage:    jetstream.FileStorage,
		MaxAge:     24 * time.Hour,
		Duplicates: 2 * time.Second,
	})
	if err != nil {
		logger.Warnf("[nats] events stream: %v", err)
	}

	// Ensure commands stream (C2 forwarding)
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:     "CONSTELLATION_COMMANDS",
		Subjects: []string{"constellation.commands.>"},
		Storage:  jetstream.FileStorage,
		MaxAge:   1 * time.Hour,
	})
	if err != nil {
		logger.Warnf("[nats] commands stream: %v", err)
	}

	// Ensure KV bucket for entity state
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:  "CONSTELLATION_GLOBAL_STATE",
		Storage: jetstream.FileStorage,
		TTL:     1 * time.Hour,
	})
	if err != nil {
		logger.Warnf("[nats] KV bucket: %v", err)
	}

	logger.Infof("[nats] connected to %s (jetstream ready)", natsURL)
	return &OverwatchPublisher{nc: nc, js: js, kv: kv}, nil
}

// PublishTelemetry publishes a MAVLink telemetry envelope to JetStream with headers.
// Subject: constellation.telemetry.{entityID}.{msgType}
func (p *OverwatchPublisher) PublishTelemetry(entityID, source, msgType string, envelope interface{}) error {
	data, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal telemetry: %w", err)
	}

	subject := fmt.Sprintf("constellation.telemetry.%s.%s", entityID, msgType)

	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header: nats.Header{
			"entity_id":    {entityID},
			"source":       {source},
			"message_type": {msgType},
			"timestamp":    {time.Now().UTC().Format(time.RFC3339Nano)},
		},
	}

	_, err = p.js.PublishMsg(context.Background(), msg)
	return err
}

// PublishDetection publishes a detection event to JetStream with per-track subject and dedup header.
// Subject: constellation.events.isr.{orgID}.{entityID}.detection.{trackID}
func (p *OverwatchPublisher) PublishDetection(orgID, entityID, trackID string, event interface{}) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal detection: %w", err)
	}
	subject := fmt.Sprintf("constellation.events.isr.%s.%s.detection.%s", orgID, entityID, trackID)
	msg := &nats.Msg{
		Subject: subject,
		Data:    data,
		Header: nats.Header{
			"Nats-Msg-Id": {fmt.Sprintf("%s-%s-%d", entityID, trackID, time.Now().UnixNano())},
			"entity_id":   {entityID},
			"track_id":    {trackID},
		},
	}
	_, err = p.js.PublishMsg(context.Background(), msg)
	return err
}

// PublishEvent publishes a generic event (bootsequence, shutdown, etc).
func (p *OverwatchPublisher) PublishEvent(orgID, entityID, eventType string, event interface{}) error {
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	subject := fmt.Sprintf("constellation.events.isr.%s.%s.%s", orgID, entityID, eventType)
	_, err = p.js.Publish(context.Background(), subject, data)
	return err
}

// UpdateDeviceState reads the existing entity state from KV, merges the
// incoming MAVLink fields into the correct ontology signal tree, and writes back.
// KV key: {entityID} in bucket CONSTELLATION_GLOBAL_STATE
func (p *OverwatchPublisher) UpdateDeviceState(meta EntityMeta, envelope interface{}) error {
	if p.kv == nil {
		return nil
	}

	// Extract msg_name + fields from the envelope
	raw, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	var env struct {
		MsgName string                 `json:"msg_name"`
		Fields  map[string]interface{} `json:"fields"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}

	ctx := context.Background()
	key := meta.EntityID

	// Read existing state to merge into
	var state EntityKVState
	if entry, err := p.kv.Get(ctx, key); err == nil {
		_ = json.Unmarshal(entry.Value(), &state)
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)

	// Set top-level metadata
	state.EntityID = meta.EntityID
	state.OrgID = meta.OrgID
	state.OrgName = meta.OrgName
	state.Name = meta.Name
	state.EntityType = meta.EntityType
	state.Status = meta.Status
	state.Priority = meta.Priority
	state.IsLive = true
	state.LastSeen = now
	if state.FirstSeen == "" {
		state.FirstSeen = now
	}

	// Merge signal tree branch based on message type
	mergeIntoOntology(&state, env.MsgName, env.Fields, now)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal entity kv state: %w", err)
	}
	_, err = p.kv.Put(ctx, key, data)
	return err
}

// UpdateKV updates an entity's state in the CONSTELLATION_GLOBAL_STATE KV bucket.
// Used by the detector and other non-telemetry publishers.
func (p *OverwatchPublisher) UpdateKV(key string, value interface{}) error {
	if p.kv == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal kv state: %w", err)
	}
	_, err = p.kv.Put(context.Background(), key, data)
	return err
}

// UpdateDetections merges detection state into the entity's KV entry,
// preserving all telemetry signal trees.
func (p *OverwatchPublisher) UpdateDetections(entityID string, detections *DetectionsState) error {
	if p.kv == nil {
		return nil
	}

	ctx := context.Background()

	var state EntityKVState
	if entry, err := p.kv.Get(ctx, entityID); err == nil {
		_ = json.Unmarshal(entry.Value(), &state)
	}

	state.Detections = detections
	state.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)

	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal entity kv state: %w", err)
	}
	_, err = p.kv.Put(ctx, entityID, data)
	return err
}

// GetDetections reads the current detection objects from KV for track reclaim on restart.
func (p *OverwatchPublisher) GetDetections(entityID string) (map[string]*DetectedObject, error) {
	if p.kv == nil {
		return nil, nil
	}
	entry, err := p.kv.Get(context.Background(), entityID)
	if err != nil {
		return nil, err
	}
	var state EntityKVState
	if err := json.Unmarshal(entry.Value(), &state); err != nil {
		return nil, err
	}
	if state.Detections == nil {
		return nil, nil
	}
	return state.Detections.Objects, nil
}

// DeleteKV removes an entity's state from the CONSTELLATION_GLOBAL_STATE KV bucket.
// Used to clean up orphaned entries when entity IDs change.
func (p *OverwatchPublisher) DeleteKV(key string) error {
	if p.kv == nil {
		return nil
	}
	return p.kv.Delete(context.Background(), key)
}

// JetStream returns the underlying JetStream context for direct consumer creation.
func (p *OverwatchPublisher) JetStream() jetstream.JetStream { return p.js }

// Close gracefully drains and closes the NATS connection.
func (p *OverwatchPublisher) Close() {
	if p.nc != nil {
		p.nc.Drain()
		logger.Info("[nats] connection closed")
	}
}

// ── Ontology merge logic ────────────────────────────────────────────────────

func mergeIntoOntology(s *EntityKVState, msgName string, fields map[string]interface{}, ts string) {
	switch msgName {
	case "GlobalPositionInt":
		if s.Position == nil {
			s.Position = &PositionState{}
		}

		// MAVLink: lat/lon in degE7 -> decimal degrees
		// MAVLink: alt in mm -> meters
		// MAVLink: vx/vy/vz in cm/s -> m/s
		// MAVLink: heading (Hdg) in cdeg (0-36000) -> degrees
		s.Position.Global = &PositionGlobal{
			Latitude:         toF64(fields["latitude"]) / 1e7,
			Longitude:        toF64(fields["longitude"]) / 1e7,
			AltitudeMSL:      toF64(fields["altitude"]) / 1000.0,
			AltitudeRelative: toF64(fields["relative_alt"]) / 1000.0,
			Heading:          toF64(fields["heading"]) / 100.0,
			Timestamp:        ts,
		}
		s.Position.Local = &PositionLocal{
			Vx:        toF64(fields["vx"]) / 100.0,
			Vy:        toF64(fields["vy"]) / 100.0,
			Vz:        toF64(fields["vz"]) / 100.0,
			Timestamp: ts,
		}
		// Preserve existing local x/y/z if set
		if existing := s.Position.Local; existing != nil {
			s.Position.Local.X = existing.X
			s.Position.Local.Y = existing.Y
			s.Position.Local.Z = existing.Z
		}

	case "Attitude":
		if s.Attitude == nil {
			s.Attitude = &AttitudeState{}
		}
		// MAVLink attitude fields are already in radians and rad/s
		s.Attitude.Euler = &AttitudeEuler{
			Roll:       toF64(fields["roll"]),
			Pitch:      toF64(fields["pitch"]),
			Yaw:        toF64(fields["yaw"]),
			RollSpeed:  toF64(fields["roll_speed"]),
			PitchSpeed: toF64(fields["pitch_speed"]),
			YawSpeed:   toF64(fields["yaw_speed"]),
			Timestamp:  ts,
		}

	case "Heartbeat":
		if s.VehicleStatus == nil {
			s.VehicleStatus = &VehicleStatusState{}
		}
		s.VehicleStatus.VehicleType = toInt(fields["type"])
		s.VehicleStatus.SystemStatus = toInt(fields["system_status"])
		s.VehicleStatus.Mode = mavSystemStatus(toInt(fields["system_status"]))
		s.VehicleStatus.CustomMode = toInt(fields["custom_mode"])
		s.VehicleStatus.Autopilot = toInt(fields["autopilot"])
		s.VehicleStatus.Armed = toInt(fields["base_mode"])&128 != 0 // MAV_MODE_FLAG_SAFETY_ARMED
		s.VehicleStatus.Timestamp = ts

	case "SystemStatus":
		// Power signal tree
		if s.Power == nil {
			s.Power = &PowerState{}
		}
		// MAVLink: voltage_battery in mV -> V
		// MAVLink: current_battery in cA -> A (10 mA units)
		s.Power.Voltage = toF64(fields["voltage_battery"]) / 1000.0
		s.Power.Current = toF64(fields["current_battery"]) / 100.0
		s.Power.BatteryRemaining = toInt(fields["battery_remaining"])
		s.Power.Timestamp = ts

		// Vehicle status signal tree (load + sensors)
		if s.VehicleStatus == nil {
			s.VehicleStatus = &VehicleStatusState{}
		}
		s.VehicleStatus.Load = toInt(fields["load"])
		s.VehicleStatus.SensorsEnabled = toInt(fields["onboard_control_sensors_enabled"])
		s.VehicleStatus.SensorsHealth = toInt(fields["onboard_control_sensors_health"])
		if s.VehicleStatus.Timestamp == "" {
			s.VehicleStatus.Timestamp = ts
		}

	case "VFR_HUD":
		if s.VFR == nil {
			s.VFR = &VFRState{}
		}
		// MAVLink VFR_HUD fields are already in correct SI units
		// heading is uint16 centi-degrees (0-36000) in raw MAVLink but
		// gomavlib's VfrHud.Heading is already int16 in degrees (0-360)
		s.VFR.Airspeed = toF64(fields["airspeed"])
		s.VFR.Groundspeed = toF64(fields["ground_speed"])
		s.VFR.Heading = toInt(fields["heading"])
		s.VFR.ClimbRate = toF64(fields["climb_rate"])
		s.VFR.Throttle = toInt(fields["throttle"])
		s.VFR.Altitude = toF64(fields["altitude"])
		s.VFR.Timestamp = ts
	}
}

// ── MAVLink enum helpers ─────────────────────────────────────────────────────

func mavSystemStatus(v int) string {
	switch v {
	case 0:
		return "UNINIT"
	case 1:
		return "BOOT"
	case 2:
		return "CALIBRATING"
	case 3:
		return "STANDBY"
	case 4:
		return "ACTIVE"
	case 5:
		return "CRITICAL"
	case 6:
		return "EMERGENCY"
	case 7:
		return "POWEROFF"
	case 8:
		return "FLIGHT_TERMINATION"
	default:
		return "UNKNOWN"
	}
}

// ── Type conversion helpers ─────────────────────────────────────────────────
// Fields come as native Go types from the MAVLink parser but may be float64
// after JSON round-trip.

func toF64(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int32:
		return float64(n)
	case int16:
		return float64(n)
	case int8:
		return float64(n)
	case uint16:
		return float64(n)
	case int:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}

func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int32:
		return int(n)
	case int16:
		return int(n)
	case int8:
		return int(n)
	case uint16:
		return int(n)
	case uint32:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

func toStr(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

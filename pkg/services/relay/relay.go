package relay

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/services/publisher"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/bluenviron/gomavlib/v3"
	"github.com/bluenviron/gomavlib/v3/pkg/dialects/common"
)

// TelemetryEnvelope is the message format published to NATS JetStream.
// Field names match the aero-arc-relay2constellation reference.
type TelemetryEnvelope struct {
	DroneID        string                 `json:"drone_id"`
	Source         string                 `json:"source"`
	TimestampRelay time.Time              `json:"timestamp_relay"`
	MsgID          uint32                 `json:"msg_id"`
	MsgName        string                 `json:"msg_name"`
	SystemID       uint8                  `json:"system_id"`
	ComponentID    uint8                  `json:"component_id"`
	Fields         map[string]interface{} `json:"fields"`
}

// StartRelay starts MAVLink listeners for all entities that have mavlink config.
// Accepts the full C4State so entity metadata (org_id, etc.) is available for KV publishing.
func StartRelay(ctx context.Context, state *shared.C4State, pub *publisher.OverwatchPublisher) {
	started := 0
	for _, entity := range state.Entities {
		if entity.MavlinkPort == 0 {
			continue
		}
		meta := publisher.EntityMeta{
			EntityID:   entity.EntityID,
			OrgID:      state.OrgID,
			OrgName:    state.OrgName,
			Name:       entity.Name,
			EntityType: entity.Type,
			Status:     "active",
			Priority:   "normal",
		}
		go runRelayForEntity(ctx, entity, meta, pub)
		started++
	}
	if started > 0 {
		logger.Infof("[relay] started %d MAVLink listener(s)", started)
	}
}

func runRelayForEntity(ctx context.Context, entity shared.EntityState, meta publisher.EntityMeta, pub *publisher.OverwatchPublisher) {
	logger.Infof("[relay] listening for %s on UDP :%d", entity.Name, entity.MavlinkPort)

	node, err := gomavlib.NewNode(gomavlib.NodeConf{
		Endpoints: []gomavlib.EndpointConf{
			gomavlib.EndpointUDPServer{Address: fmt.Sprintf(":%d", entity.MavlinkPort)},
		},
		Dialect:     common.Dialect,
		OutVersion:  gomavlib.V2,
		OutSystemID: 254,
	})
	if err != nil {
		logger.Errorf("[relay] failed to start node for %s: %v", entity.Name, err)
		return
	}

	go func() {
		<-ctx.Done()
		node.Close()
	}()

	for evt := range node.Events() {
		if frm, ok := evt.(*gomavlib.EventFrame); ok {
			handleMAVLinkFrame(frm, entity, meta, pub)
		}
	}
}

func handleMAVLinkFrame(frm *gomavlib.EventFrame, entity shared.EntityState, meta publisher.EntityMeta, pub *publisher.OverwatchPublisher) {
	var msgName string
	var msgID uint32
	fields := make(map[string]interface{})

	switch msg := frm.Message().(type) {
	case *common.MessageHeartbeat:
		msgName = "Heartbeat"
		msgID = msg.GetID()
		fields["type"] = int(msg.Type)
		fields["autopilot"] = int(msg.Autopilot)
		fields["base_mode"] = int(msg.BaseMode)
		fields["custom_mode"] = int(msg.CustomMode)
		fields["system_status"] = int(msg.SystemStatus)

	case *common.MessageGlobalPositionInt:
		msgName = "GlobalPositionInt"
		msgID = msg.GetID()
		fields["latitude"] = msg.Lat
		fields["longitude"] = msg.Lon
		fields["altitude"] = msg.Alt
		fields["relative_alt"] = msg.RelativeAlt
		fields["heading"] = msg.Hdg
		fields["vx"] = msg.Vx
		fields["vy"] = msg.Vy
		fields["vz"] = msg.Vz

	case *common.MessageAttitude:
		msgName = "Attitude"
		msgID = msg.GetID()
		fields["pitch"] = msg.Pitch
		fields["roll"] = msg.Roll
		fields["yaw"] = msg.Yaw
		fields["pitch_speed"] = msg.Pitchspeed
		fields["roll_speed"] = msg.Rollspeed
		fields["yaw_speed"] = msg.Yawspeed

	case *common.MessageVfrHud:
		msgName = "VFR_HUD"
		msgID = msg.GetID()
		fields["airspeed"] = msg.Airspeed
		fields["ground_speed"] = msg.Groundspeed
		fields["altitude"] = msg.Alt
		fields["heading"] = int(msg.Heading)
		fields["throttle"] = int(msg.Throttle)
		fields["climb_rate"] = msg.Climb

	case *common.MessageSysStatus:
		msgName = "SystemStatus"
		msgID = msg.GetID()
		fields["battery_remaining"] = int(msg.BatteryRemaining)
		fields["voltage_battery"] = int(msg.VoltageBattery)
		fields["current_battery"] = int(msg.CurrentBattery)
		fields["load"] = int(msg.Load)
		fields["onboard_control_sensors_present"] = int(msg.OnboardControlSensorsPresent)
		fields["onboard_control_sensors_enabled"] = int(msg.OnboardControlSensorsEnabled)
		fields["onboard_control_sensors_health"] = int(msg.OnboardControlSensorsHealth)
		fields["drop_rate_comm"] = int(msg.DropRateComm)
		fields["errors_comm"] = int(msg.ErrorsComm)

	default:
		return
	}

	envelope := TelemetryEnvelope{
		DroneID:        entity.EntityID,
		Source:         entity.Name,
		TimestampRelay: time.Now().UTC(),
		MsgID:          msgID,
		MsgName:        msgName,
		SystemID:       frm.Frame.GetSystemID(),
		ComponentID:    frm.Frame.GetComponentID(),
		Fields:         fields,
	}

	if err := pub.PublishTelemetry(entity.EntityID, entity.Name, strings.ToLower(msgName), envelope); err != nil {
		logger.Errorf("[relay] publish error for %s/%s: %v", entity.Name, msgName, err)
	}

	// Update ontology-compliant entity state in KV
	if err := pub.UpdateDeviceState(meta, envelope); err != nil {
		logger.Errorf("[relay] kv update error for %s: %v", entity.Name, err)
	}
}

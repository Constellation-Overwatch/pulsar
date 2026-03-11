package relay

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Constellation-Overwatch/pulsar/pkg/services/logger"
	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
	"github.com/bluenviron/gomavlib/v3"
	"github.com/bluenviron/gomavlib/v3/pkg/dialects/common"
	"github.com/bluenviron/gomavlib/v3/pkg/message"
	"github.com/nats-io/nats.go/jetstream"
)

// CommandEnvelope is the JSON payload consumed from the CONSTELLATION_COMMANDS stream.
// It uses a flat union of all fields across 16 supported MAVLink message types.
// Each command_type uses a subset of fields; unused fields are zero-valued and ignored.
type CommandEnvelope struct {
	CommandType     string  `json:"command_type"`
	TargetSystem    uint8   `json:"target_system"`
	TargetComponent uint8   `json:"target_component"`

	// CommandLong / CommandInt / CommandCancel
	Command      uint16  `json:"command,omitempty"`
	Confirmation uint8   `json:"confirmation,omitempty"`
	Param1       float32 `json:"param1,omitempty"`
	Param2       float32 `json:"param2,omitempty"`
	Param3       float32 `json:"param3,omitempty"`
	Param4       float32 `json:"param4,omitempty"`
	Param5       float32 `json:"param5,omitempty"`
	Param6       float32 `json:"param6,omitempty"`
	Param7       float32 `json:"param7,omitempty"`

	// CommandInt position / SetHomePosition
	Frame uint8   `json:"frame,omitempty"`
	X     int32   `json:"x,omitempty"`
	Y     int32   `json:"y,omitempty"`
	Z     float32 `json:"z,omitempty"`

	// SetMode
	BaseMode   uint8  `json:"base_mode,omitempty"`
	CustomMode uint32 `json:"custom_mode,omitempty"`

	// SetPositionTargetGlobalInt / LocalNed
	TimeBootMs      uint32  `json:"time_boot_ms,omitempty"`
	CoordinateFrame uint8   `json:"coordinate_frame,omitempty"`
	TypeMask        uint16  `json:"type_mask,omitempty"`
	LatInt          int32   `json:"lat_int,omitempty"`
	LonInt          int32   `json:"lon_int,omitempty"`
	Alt             float32 `json:"alt,omitempty"`
	Vx              float32 `json:"vx,omitempty"`
	Vy              float32 `json:"vy,omitempty"`
	Vz              float32 `json:"vz,omitempty"`
	Afx             float32 `json:"afx,omitempty"`
	Afy             float32 `json:"afy,omitempty"`
	Afz             float32 `json:"afz,omitempty"`
	Yaw             float32 `json:"yaw,omitempty"`
	YawRate         float32 `json:"yaw_rate,omitempty"`
	// LocalNed position floats
	PosX float32 `json:"pos_x,omitempty"`
	PosY float32 `json:"pos_y,omitempty"`
	PosZ float32 `json:"pos_z,omitempty"`

	// SetAttitudeTarget
	Q             [4]float32 `json:"q,omitempty"`
	TypeMaskAtt   uint8      `json:"type_mask_att,omitempty"`
	BodyRollRate  float32    `json:"body_roll_rate,omitempty"`
	BodyPitchRate float32    `json:"body_pitch_rate,omitempty"`
	BodyYawRate   float32    `json:"body_yaw_rate,omitempty"`
	Thrust        float32    `json:"thrust,omitempty"`

	// SetHomePosition
	Latitude  int32 `json:"latitude,omitempty"`
	Longitude int32 `json:"longitude,omitempty"`
	Altitude  int32 `json:"altitude,omitempty"`

	// MissionItemInt
	Seq          uint16 `json:"seq,omitempty"`
	Current      uint8  `json:"current,omitempty"`
	Autocontinue uint8  `json:"autocontinue,omitempty"`
	MissionType  uint8  `json:"mission_type,omitempty"`

	// MissionCount
	Count uint16 `json:"count,omitempty"`

	// ManualControl
	ManualX int16  `json:"manual_x,omitempty"`
	ManualY int16  `json:"manual_y,omitempty"`
	ManualZ int16  `json:"manual_z,omitempty"`
	ManualR int16  `json:"manual_r,omitempty"`
	Buttons uint16 `json:"buttons,omitempty"`

	// RcChannelsOverride
	Chan1 uint16 `json:"chan1,omitempty"`
	Chan2 uint16 `json:"chan2,omitempty"`
	Chan3 uint16 `json:"chan3,omitempty"`
	Chan4 uint16 `json:"chan4,omitempty"`
	Chan5 uint16 `json:"chan5,omitempty"`
	Chan6 uint16 `json:"chan6,omitempty"`
	Chan7 uint16 `json:"chan7,omitempty"`
	Chan8 uint16 `json:"chan8,omitempty"`

	// ParamSet
	ParamID    string  `json:"param_id,omitempty"`
	ParamValue float32 `json:"param_value,omitempty"`
	ParamType  uint8   `json:"param_type,omitempty"`

	// Metadata (not forwarded to MAVLink)
	Source    string `json:"source,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
}

// buildMAVLinkMessage converts a CommandEnvelope into the corresponding gomavlib message.
func buildMAVLinkMessage(env CommandEnvelope) (message.Message, error) {
	switch env.CommandType {
	case "command_long":
		return &common.MessageCommandLong{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Command:         common.MAV_CMD(env.Command),
			Confirmation:    env.Confirmation,
			Param1:          env.Param1,
			Param2:          env.Param2,
			Param3:          env.Param3,
			Param4:          env.Param4,
			Param5:          env.Param5,
			Param6:          env.Param6,
			Param7:          env.Param7,
		}, nil

	case "command_int":
		return &common.MessageCommandInt{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Frame:           common.MAV_FRAME(env.Frame),
			Command:         common.MAV_CMD(env.Command),
			Current:         env.Current,
			Autocontinue:    env.Autocontinue,
			Param1:          env.Param1,
			Param2:          env.Param2,
			Param3:          env.Param3,
			Param4:          env.Param4,
			X:               env.X,
			Y:               env.Y,
			Z:               env.Z,
		}, nil

	case "command_cancel":
		return &common.MessageCommandCancel{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Command:         common.MAV_CMD(env.Command),
		}, nil

	case "set_position_target_global_int":
		return &common.MessageSetPositionTargetGlobalInt{
			TimeBootMs:      env.TimeBootMs,
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			CoordinateFrame: common.MAV_FRAME(env.CoordinateFrame),
			TypeMask:        common.POSITION_TARGET_TYPEMASK(env.TypeMask),
			LatInt:          env.LatInt,
			LonInt:          env.LonInt,
			Alt:             env.Alt,
			Vx:              env.Vx,
			Vy:              env.Vy,
			Vz:              env.Vz,
			Afx:             env.Afx,
			Afy:             env.Afy,
			Afz:             env.Afz,
			Yaw:             env.Yaw,
			YawRate:         env.YawRate,
		}, nil

	case "set_position_target_local_ned":
		return &common.MessageSetPositionTargetLocalNed{
			TimeBootMs:      env.TimeBootMs,
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			CoordinateFrame: common.MAV_FRAME(env.CoordinateFrame),
			TypeMask:        common.POSITION_TARGET_TYPEMASK(env.TypeMask),
			X:               env.PosX,
			Y:               env.PosY,
			Z:               env.PosZ,
			Vx:              env.Vx,
			Vy:              env.Vy,
			Vz:              env.Vz,
			Afx:             env.Afx,
			Afy:             env.Afy,
			Afz:             env.Afz,
			Yaw:             env.Yaw,
			YawRate:         env.YawRate,
		}, nil

	case "set_attitude_target":
		return &common.MessageSetAttitudeTarget{
			TimeBootMs:      env.TimeBootMs,
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			TypeMask:        common.ATTITUDE_TARGET_TYPEMASK(env.TypeMaskAtt),
			Q:               env.Q,
			BodyRollRate:    env.BodyRollRate,
			BodyPitchRate:   env.BodyPitchRate,
			BodyYawRate:     env.BodyYawRate,
			Thrust:          env.Thrust,
		}, nil

	case "set_mode":
		return &common.MessageSetMode{
			TargetSystem: env.TargetSystem,
			BaseMode:     common.MAV_MODE(env.BaseMode),
			CustomMode:   env.CustomMode,
		}, nil

	case "set_home_position":
		return &common.MessageSetHomePosition{
			TargetSystem: env.TargetSystem,
			Latitude:     env.Latitude,
			Longitude:    env.Longitude,
			Altitude:     env.Altitude,
			X:            env.PosX,
			Y:            env.PosY,
			Z:            env.PosZ,
			Q:            env.Q,
		}, nil

	case "mission_item_int":
		return &common.MessageMissionItemInt{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Seq:             env.Seq,
			Frame:           common.MAV_FRAME(env.Frame),
			Command:         common.MAV_CMD(env.Command),
			Current:         env.Current,
			Autocontinue:    env.Autocontinue,
			Param1:          env.Param1,
			Param2:          env.Param2,
			Param3:          env.Param3,
			Param4:          env.Param4,
			X:               env.X,
			Y:               env.Y,
			Z:               env.Z,
			MissionType:     common.MAV_MISSION_TYPE(env.MissionType),
		}, nil

	case "mission_count":
		return &common.MessageMissionCount{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Count:           env.Count,
			MissionType:     common.MAV_MISSION_TYPE(env.MissionType),
		}, nil

	case "mission_clear_all":
		return &common.MessageMissionClearAll{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			MissionType:     common.MAV_MISSION_TYPE(env.MissionType),
		}, nil

	case "mission_set_current":
		return &common.MessageMissionSetCurrent{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Seq:             env.Seq,
		}, nil

	case "manual_control":
		return &common.MessageManualControl{
			Target:  env.TargetSystem,
			X:       env.ManualX,
			Y:       env.ManualY,
			Z:       env.ManualZ,
			R:       env.ManualR,
			Buttons: env.Buttons,
		}, nil

	case "rc_channels_override":
		return &common.MessageRcChannelsOverride{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			Chan1Raw:        env.Chan1,
			Chan2Raw:        env.Chan2,
			Chan3Raw:        env.Chan3,
			Chan4Raw:        env.Chan4,
			Chan5Raw:        env.Chan5,
			Chan6Raw:        env.Chan6,
			Chan7Raw:        env.Chan7,
			Chan8Raw:        env.Chan8,
		}, nil

	case "param_set":
		return &common.MessageParamSet{
			TargetSystem:    env.TargetSystem,
			TargetComponent: env.TargetComponent,
			ParamId:         env.ParamID,
			ParamValue:      env.ParamValue,
			ParamType:       common.MAV_PARAM_TYPE(env.ParamType),
		}, nil

	default:
		return nil, fmt.Errorf("unsupported command type: %s", env.CommandType)
	}
}

// runCommandSubscriber consumes commands from JetStream and forwards them as MAVLink messages.
func runCommandSubscriber(ctx context.Context, node *gomavlib.Node, entity shared.EntityState, js jetstream.JetStream) {
	subject := fmt.Sprintf("constellation.commands.%s.>", entity.EntityID)
	consumerName := fmt.Sprintf("pulsar-cmd-%s", entity.EntityID)

	consumer, err := js.CreateOrUpdateConsumer(ctx, "CONSTELLATION_COMMANDS", jetstream.ConsumerConfig{
		Name:          consumerName,
		Durable:       consumerName,
		FilterSubject: subject,
		DeliverPolicy: jetstream.DeliverNewPolicy,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		logger.Errorf("[relay] command consumer for %s: %v", entity.Name, err)
		return
	}

	iter, err := consumer.Messages()
	if err != nil {
		logger.Errorf("[relay] command messages iterator for %s: %v", entity.Name, err)
		return
	}

	logger.Infof("[relay] command subscriber started for %s on %s", entity.Name, subject)

	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			return
		}

		var env CommandEnvelope
		if err := json.Unmarshal(msg.Data(), &env); err != nil {
			logger.Warnf("[relay] malformed command for %s: %v", entity.Name, err)
			_ = msg.Ack()
			continue
		}

		mavMsg, err := buildMAVLinkMessage(env)
		if err != nil {
			logger.Warnf("[relay] unsupported command for %s: %v", entity.Name, err)
			_ = msg.Ack()
			continue
		}

		if err := node.WriteMessageAll(mavMsg); err != nil {
			logger.Errorf("[relay] failed to forward %s to %s: %v", env.CommandType, entity.Name, err)
			_ = msg.Nak()
			continue
		}

		source := env.Source
		if source == "" {
			source = "unknown"
		}
		logger.Infof("[relay] forwarded %s to %s (from %s)", env.CommandType, entity.Name, source)
		_ = msg.Ack()
	}
}

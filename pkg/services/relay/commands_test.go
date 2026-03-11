package relay

import (
	"testing"

	"github.com/bluenviron/gomavlib/v3/pkg/dialects/common"
)

func TestBuildMAVLinkMessage_CommandLong(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "command_long",
		TargetSystem:    1,
		TargetComponent: 1,
		Command:         400, // MAV_CMD_COMPONENT_ARM_DISARM
		Param1:          1,   // arm
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cl, ok := msg.(*common.MessageCommandLong)
	if !ok {
		t.Fatalf("expected *MessageCommandLong, got %T", msg)
	}
	if cl.TargetSystem != 1 {
		t.Errorf("TargetSystem = %d, want 1", cl.TargetSystem)
	}
	if cl.Command != 400 {
		t.Errorf("Command = %d, want 400", cl.Command)
	}
	if cl.Param1 != 1 {
		t.Errorf("Param1 = %f, want 1", cl.Param1)
	}
}

func TestBuildMAVLinkMessage_CommandInt(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "command_int",
		TargetSystem:    1,
		TargetComponent: 1,
		Frame:           6, // MAV_FRAME_GLOBAL_RELATIVE_ALT_INT
		Command:         192, // MAV_CMD_DO_REPOSITION
		Param1:          5,
		X:               473977500,
		Y:               85455620,
		Z:               50.0,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ci, ok := msg.(*common.MessageCommandInt)
	if !ok {
		t.Fatalf("expected *MessageCommandInt, got %T", msg)
	}
	if ci.Frame != 6 {
		t.Errorf("Frame = %d, want 6", ci.Frame)
	}
	if ci.X != 473977500 {
		t.Errorf("X = %d, want 473977500", ci.X)
	}
	if ci.Z != 50.0 {
		t.Errorf("Z = %f, want 50.0", ci.Z)
	}
}

func TestBuildMAVLinkMessage_CommandCancel(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "command_cancel",
		TargetSystem:    1,
		TargetComponent: 1,
		Command:         22, // MAV_CMD_NAV_TAKEOFF
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	cc, ok := msg.(*common.MessageCommandCancel)
	if !ok {
		t.Fatalf("expected *MessageCommandCancel, got %T", msg)
	}
	if cc.Command != 22 {
		t.Errorf("Command = %d, want 22", cc.Command)
	}
}

func TestBuildMAVLinkMessage_SetPositionTargetGlobalInt(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "set_position_target_global_int",
		TargetSystem:    1,
		CoordinateFrame: 6,
		TypeMask:        3576,
		LatInt:          473977500,
		LonInt:          85455620,
		Alt:             50.0,
		Yaw:             1.57,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp, ok := msg.(*common.MessageSetPositionTargetGlobalInt)
	if !ok {
		t.Fatalf("expected *MessageSetPositionTargetGlobalInt, got %T", msg)
	}
	if sp.LatInt != 473977500 {
		t.Errorf("LatInt = %d, want 473977500", sp.LatInt)
	}
	if sp.Alt != 50.0 {
		t.Errorf("Alt = %f, want 50.0", sp.Alt)
	}
	if sp.Yaw != 1.57 {
		t.Errorf("Yaw = %f, want 1.57", sp.Yaw)
	}
	if sp.TypeMask != 3576 {
		t.Errorf("TypeMask = %d, want 3576", sp.TypeMask)
	}
}

func TestBuildMAVLinkMessage_SetPositionTargetLocalNed(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "set_position_target_local_ned",
		TargetSystem:    1,
		CoordinateFrame: 1, // MAV_FRAME_LOCAL_NED
		TypeMask:        3576,
		PosX:            10.0,
		PosY:            5.0,
		PosZ:            -20.0,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sp, ok := msg.(*common.MessageSetPositionTargetLocalNed)
	if !ok {
		t.Fatalf("expected *MessageSetPositionTargetLocalNed, got %T", msg)
	}
	if sp.X != 10.0 {
		t.Errorf("X = %f, want 10.0", sp.X)
	}
	if sp.Y != 5.0 {
		t.Errorf("Y = %f, want 5.0", sp.Y)
	}
	if sp.Z != -20.0 {
		t.Errorf("Z = %f, want -20.0", sp.Z)
	}
}

func TestBuildMAVLinkMessage_SetAttitudeTarget(t *testing.T) {
	env := CommandEnvelope{
		CommandType:  "set_attitude_target",
		TargetSystem: 1,
		Q:            [4]float32{1, 0, 0, 0},
		Thrust:       0.5,
		TypeMaskAtt:  7, // ignore rates
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sa, ok := msg.(*common.MessageSetAttitudeTarget)
	if !ok {
		t.Fatalf("expected *MessageSetAttitudeTarget, got %T", msg)
	}
	if sa.Q[0] != 1.0 {
		t.Errorf("Q[0] = %f, want 1.0", sa.Q[0])
	}
	if sa.Thrust != 0.5 {
		t.Errorf("Thrust = %f, want 0.5", sa.Thrust)
	}
	if sa.TypeMask != 7 {
		t.Errorf("TypeMask = %d, want 7", sa.TypeMask)
	}
}

func TestBuildMAVLinkMessage_SetMode(t *testing.T) {
	env := CommandEnvelope{
		CommandType:  "set_mode",
		TargetSystem: 1,
		BaseMode:     1,
		CustomMode:   4, // GUIDED
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sm, ok := msg.(*common.MessageSetMode)
	if !ok {
		t.Fatalf("expected *MessageSetMode, got %T", msg)
	}
	if sm.CustomMode != 4 {
		t.Errorf("CustomMode = %d, want 4", sm.CustomMode)
	}
	if sm.BaseMode != 1 {
		t.Errorf("BaseMode = %d, want 1", sm.BaseMode)
	}
}

func TestBuildMAVLinkMessage_SetHomePosition(t *testing.T) {
	env := CommandEnvelope{
		CommandType:  "set_home_position",
		TargetSystem: 1,
		Latitude:     473977500,
		Longitude:    85455620,
		Altitude:     500000, // mm
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	sh, ok := msg.(*common.MessageSetHomePosition)
	if !ok {
		t.Fatalf("expected *MessageSetHomePosition, got %T", msg)
	}
	if sh.Latitude != 473977500 {
		t.Errorf("Latitude = %d, want 473977500", sh.Latitude)
	}
	if sh.Longitude != 85455620 {
		t.Errorf("Longitude = %d, want 85455620", sh.Longitude)
	}
}

func TestBuildMAVLinkMessage_MissionItemInt(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "mission_item_int",
		TargetSystem:    1,
		TargetComponent: 1,
		Seq:             0,
		Frame:           3, // MAV_FRAME_GLOBAL_RELATIVE_ALT
		Command:         16, // MAV_CMD_NAV_WAYPOINT
		Autocontinue:    1,
		X:               473977500,
		Y:               85455620,
		Z:               50.0,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mi, ok := msg.(*common.MessageMissionItemInt)
	if !ok {
		t.Fatalf("expected *MessageMissionItemInt, got %T", msg)
	}
	if mi.Seq != 0 {
		t.Errorf("Seq = %d, want 0", mi.Seq)
	}
	if mi.Command != 16 {
		t.Errorf("Command = %d, want 16", mi.Command)
	}
	if mi.X != 473977500 {
		t.Errorf("X = %d, want 473977500", mi.X)
	}
}

func TestBuildMAVLinkMessage_MissionCount(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "mission_count",
		TargetSystem:    1,
		TargetComponent: 1,
		Count:           5,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mc, ok := msg.(*common.MessageMissionCount)
	if !ok {
		t.Fatalf("expected *MessageMissionCount, got %T", msg)
	}
	if mc.Count != 5 {
		t.Errorf("Count = %d, want 5", mc.Count)
	}
}

func TestBuildMAVLinkMessage_MissionClearAll(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "mission_clear_all",
		TargetSystem:    1,
		TargetComponent: 1,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, ok := msg.(*common.MessageMissionClearAll)
	if !ok {
		t.Fatalf("expected *MessageMissionClearAll, got %T", msg)
	}
}

func TestBuildMAVLinkMessage_MissionSetCurrent(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "mission_set_current",
		TargetSystem:    1,
		TargetComponent: 1,
		Seq:             3,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ms, ok := msg.(*common.MessageMissionSetCurrent)
	if !ok {
		t.Fatalf("expected *MessageMissionSetCurrent, got %T", msg)
	}
	if ms.Seq != 3 {
		t.Errorf("Seq = %d, want 3", ms.Seq)
	}
}

func TestBuildMAVLinkMessage_ManualControl(t *testing.T) {
	env := CommandEnvelope{
		CommandType:  "manual_control",
		TargetSystem: 1,
		ManualX:      500,
		ManualY:      -300,
		ManualZ:      700,
		ManualR:      0,
		Buttons:      1,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mc, ok := msg.(*common.MessageManualControl)
	if !ok {
		t.Fatalf("expected *MessageManualControl, got %T", msg)
	}
	if mc.X != 500 {
		t.Errorf("X = %d, want 500", mc.X)
	}
	if mc.Y != -300 {
		t.Errorf("Y = %d, want -300", mc.Y)
	}
	if mc.Target != 1 {
		t.Errorf("Target = %d, want 1", mc.Target)
	}
}

func TestBuildMAVLinkMessage_RcChannelsOverride(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "rc_channels_override",
		TargetSystem:    1,
		TargetComponent: 1,
		Chan1:           1500,
		Chan2:           1500,
		Chan3:           1200,
		Chan4:           1500,
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rc, ok := msg.(*common.MessageRcChannelsOverride)
	if !ok {
		t.Fatalf("expected *MessageRcChannelsOverride, got %T", msg)
	}
	if rc.Chan1Raw != 1500 {
		t.Errorf("Chan1Raw = %d, want 1500", rc.Chan1Raw)
	}
	if rc.Chan3Raw != 1200 {
		t.Errorf("Chan3Raw = %d, want 1200", rc.Chan3Raw)
	}
}

func TestBuildMAVLinkMessage_ParamSet(t *testing.T) {
	env := CommandEnvelope{
		CommandType:     "param_set",
		TargetSystem:    1,
		TargetComponent: 1,
		ParamID:         "SYSID_THISMAV",
		ParamValue:      2,
		ParamType:       6, // MAV_PARAM_TYPE_REAL32
	}
	msg, err := buildMAVLinkMessage(env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	ps, ok := msg.(*common.MessageParamSet)
	if !ok {
		t.Fatalf("expected *MessageParamSet, got %T", msg)
	}
	if ps.ParamId != "SYSID_THISMAV" {
		t.Errorf("ParamId = %q, want SYSID_THISMAV", ps.ParamId)
	}
	if ps.ParamValue != 2 {
		t.Errorf("ParamValue = %f, want 2", ps.ParamValue)
	}
}

func TestBuildMAVLinkMessage_Unsupported(t *testing.T) {
	env := CommandEnvelope{
		CommandType: "nonexistent_type",
	}
	_, err := buildMAVLinkMessage(env)
	if err == nil {
		t.Fatal("expected error for unsupported command type")
	}
}

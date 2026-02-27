#!/usr/bin/env bash
# mavlink-relay.sh — Synthetic MAVLink relay test
#
# Auto-reads entity_id + mavlink_port from c4.json, sends synthetic MAVLink
# packets via pymavlink, then verifies CONSTELLATION_GLOBAL_STATE KV entries.
#
# Prerequisites:
#   - Pulsar relay running (task dev or docker-compose up)
#   - NATS running with JetStream enabled
#   - uv installed (for Python env management)
#
# Usage:
#   ./scripts/mavlink-relay.sh              # default: 3 rounds, auto-detect from c4.json
#   ./scripts/mavlink-relay.sh --rounds 5   # 5 rounds
#   ./scripts/mavlink-relay.sh --port 14550 # manual port override
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PULSAR_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
MAVLINK_TEST_DIR="$PULSAR_DIR/tmp/mavlink-test"
C4_JSON="$PULSAR_DIR/config/c4.json"
ENV_FILE="$PULSAR_DIR/.env"
NATS_URL="${C4_NATS_URL:-nats://localhost:4222}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

info()  { echo -e "${CYAN}[info]${NC}  $*"; }
ok()    { echo -e "${GREEN}[ok]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[warn]${NC}  $*"; }
fail()  { echo -e "${RED}[fail]${NC}  $*"; }

# ── Preflight ────────────────────────────────────────────────────────────────

info "Pulsar MAVLink Relay Test"
echo ""

# Check c4.json
if [[ ! -f "$C4_JSON" ]]; then
    fail "c4.json not found at $C4_JSON"
    echo "  Run pulsar first to register entities."
    exit 1
fi
ok "c4.json found"

# Load .env for NATS key
if [[ -f "$ENV_FILE" ]]; then
    set -a
    source "$ENV_FILE"
    set +a
    ok ".env loaded"
fi

# Show entities from c4.json
ENTITY_COUNT=$(jq '[.entities[] | select(.mavlink_port > 0)] | length' "$C4_JSON")
if [[ "$ENTITY_COUNT" -eq 0 ]]; then
    fail "no entities with mavlink_port in c4.json"
    exit 1
fi
ok "found $ENTITY_COUNT entity/entities with MAVLink config:"
jq -r '.entities[] | select(.mavlink_port > 0) | "     \(.name) (\(.type)) -> UDP :\(.mavlink_port)  entity_id=\(.entity_id)"' "$C4_JSON"
echo ""

# Check uv + pymavlink project
if ! command -v uv &>/dev/null; then
    fail "uv not found — install with: curl -LsSf https://astral.sh/uv/install.sh | sh"
    exit 1
fi

if [[ ! -f "$MAVLINK_TEST_DIR/pyproject.toml" ]]; then
    fail "mavlink-test project not found at $MAVLINK_TEST_DIR"
    echo "  Run: cd $PULSAR_DIR/tmp && uv init mavlink-test && cd mavlink-test && uv add pymavlink"
    exit 1
fi
ok "uv + pymavlink project ready"
echo ""

# ── Phase 1: Send Synthetic MAVLink ──────────────────────────────────────────

info "Phase 1: Sending synthetic MAVLink messages..."
echo ""

cd "$MAVLINK_TEST_DIR"
uv run python synthetic_mavlink.py --c4 "$C4_JSON" "$@"

echo ""

# ── Phase 2: Verify KV State ────────────────────────────────────────────────

info "Phase 2: Verifying CONSTELLATION_GLOBAL_STATE KV entries..."
echo ""

# Build nats auth flags
NATS_AUTH=""
if [[ -n "${C4_NATS_KEY:-}" ]]; then
    # Write seed to temp file for nats CLI --nkey flag
    NKEY_TMP=$(mktemp)
    echo "$C4_NATS_KEY" > "$NKEY_TMP"
    NATS_AUTH="--nkey=$NKEY_TMP"
    trap "rm -f $NKEY_TMP" EXIT
fi

PASS=0
FAIL=0

ENTITY_IDS=$(jq -r '.entities[] | select(.mavlink_port > 0) | .entity_id' "$C4_JSON")
for ENTITY_ID in $ENTITY_IDS; do
    ENTITY_NAME=$(jq -r --arg eid "$ENTITY_ID" '.entities[] | select(.entity_id == $eid) | .name' "$C4_JSON")
    KV_KEY="${ENTITY_ID}"

    echo -n "  checking $ENTITY_NAME ($KV_KEY)... "

    KV_OUT=$(nats kv get CONSTELLATION_GLOBAL_STATE "$KV_KEY" \
        --server="$NATS_URL" $NATS_AUTH --raw 2>&1) || true

    if echo "$KV_OUT" | jq -e '.entity_id' &>/dev/null; then
        ok "found"

        # Validate ontology signal tree branches
        HAS_METADATA=$(echo "$KV_OUT" | jq -e '.last_seen and .org_id and .entity_type' &>/dev/null && echo "yes" || echo "no")
        HAS_POSITION=$(echo "$KV_OUT" | jq -e '.position.global.latitude' &>/dev/null && echo "yes" || echo "no")
        HAS_ATTITUDE=$(echo "$KV_OUT" | jq -e '.attitude.euler.roll' &>/dev/null && echo "yes" || echo "no")
        HAS_POWER=$(echo "$KV_OUT" | jq -e '.power.battery_remaining' &>/dev/null && echo "yes" || echo "no")
        HAS_VFR=$(echo "$KV_OUT" | jq -e '.vfr.groundspeed' &>/dev/null && echo "yes" || echo "no")
        HAS_VEHICLE=$(echo "$KV_OUT" | jq -e '.vehicle_status.armed != null' &>/dev/null && echo "yes" || echo "no")

        echo "     Ontology signal trees:"
        echo "       metadata (last_seen, org_id):  $HAS_METADATA"
        echo "       position.global.latitude:       $HAS_POSITION  (GlobalPositionInt)"
        echo "       attitude.euler.roll:             $HAS_ATTITUDE  (Attitude)"
        echo "       power.battery_remaining:         $HAS_POWER  (SystemStatus)"
        echo "       vfr.groundspeed:                 $HAS_VFR  (VFR_HUD)"
        echo "       vehicle_status.armed:            $HAS_VEHICLE  (Heartbeat)"

        if [[ "$HAS_POSITION" == "yes" && "$HAS_ATTITUDE" == "yes" && "$HAS_POWER" == "yes" && "$HAS_VFR" == "yes" && "$HAS_VEHICLE" == "yes" ]]; then
            ok "all 5 message types merged into ontology signal trees"
            ((PASS++))

            # Unit conversion spot checks
            echo ""
            echo "     unit conversion checks:"
            LAT=$(echo "$KV_OUT" | jq '.position.global.latitude')
            VOLT=$(echo "$KV_OUT" | jq '.power.voltage')
            echo "       latitude: $LAT (should be ~37.7xxx decimal degrees, not degE7)"
            echo "       voltage:  $VOLT V (should be ~12.x, not millivolts)"
        else
            warn "some signal trees missing — not all message types reached KV"
            ((FAIL++))
        fi

        echo ""
        echo "     raw JSON:"
        echo "$KV_OUT" | jq --indent 2 '.'
        echo ""
    else
        fail "not found or empty"
        echo "     output: $KV_OUT"
        ((FAIL++))
    fi
done

# ── Summary ──────────────────────────────────────────────────────────────────

echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
if [[ "$FAIL" -eq 0 ]]; then
    ok "ALL PASSED — $PASS/$ENTITY_COUNT entities verified (ontology-compliant KV)"
else
    fail "$FAIL/$ENTITY_COUNT entities failed, $PASS passed"
    exit 1
fi

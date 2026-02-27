package registry

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Constellation-Overwatch/pulsar/pkg/shared"
)

// mockOverwatch simulates the Constellation Overwatch API for testing.
type mockOverwatch struct {
	mu       sync.Mutex
	orgs     map[string]*shared.Organization
	entities map[string]map[string]*shared.Entity // orgID -> entityID -> Entity
	nextID   int
}

func newMockOverwatch() *mockOverwatch {
	return &mockOverwatch{
		orgs:     make(map[string]*shared.Organization),
		entities: make(map[string]map[string]*shared.Entity),
	}
}

func (m *mockOverwatch) genID() string {
	m.nextID++
	return fmt.Sprintf("test-%d", m.nextID)
}

func (m *mockOverwatch) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == "GET" && r.URL.Path == "/api/v1/health":
		json.NewEncoder(w).Encode(shared.HealthStatus{
			Status: "healthy", Service: "overwatch", Version: "test",
			Timestamp: time.Now(),
		})

	case r.Method == "GET" && r.URL.Path == "/api/v1/organizations":
		var orgs []shared.Organization
		for _, o := range m.orgs {
			orgs = append(orgs, *o)
		}
		json.NewEncoder(w).Encode(orgs)

	case r.Method == "POST" && r.URL.Path == "/api/v1/organizations":
		var req shared.CreateOrganizationRequest
		json.NewDecoder(r.Body).Decode(&req)
		id := m.genID()
		org := &shared.Organization{
			OrgID: id, Name: req.Name, OrgType: req.OrgType,
			Description: req.Description,
			CreatedAt:   time.Now(), UpdatedAt: time.Now(),
		}
		m.orgs[id] = org
		m.entities[id] = make(map[string]*shared.Entity)
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(org)

	default:
		// Entity routes: /api/v1/organizations/{orgID}/entities[/{entityID}]
		var orgID, entityID string
		n, _ := fmt.Sscanf(r.URL.Path, "/api/v1/organizations/%s", &orgID)
		if n == 0 {
			http.Error(w, "not found", 404)
			return
		}
		// Clean trailing /entities or /entities/xxx
		if len(orgID) > 0 {
			// Parse manually
			parts := splitPath(r.URL.Path)
			if len(parts) >= 4 {
				orgID = parts[3]
			}
			if len(parts) >= 6 {
				entityID = parts[5]
			}
		}

		orgEntities, ok := m.entities[orgID]
		if !ok {
			http.Error(w, "org not found", 404)
			return
		}

		switch {
		case r.Method == "GET" && entityID == "":
			var entities []shared.Entity
			for _, e := range orgEntities {
				entities = append(entities, *e)
			}
			json.NewEncoder(w).Encode(entities)

		case r.Method == "POST" && entityID == "":
			var req shared.CreateEntityRequest
			json.NewDecoder(r.Body).Decode(&req)
			id := m.genID()
			entity := &shared.Entity{
				EntityID: id, OrgID: orgID, Name: req.Name,
				EntityType: req.EntityType, Status: req.Status,
				Priority: req.Priority, CreatedAt: time.Now(), UpdatedAt: time.Now(),
			}
			orgEntities[id] = entity
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(entity)

		case r.Method == "PUT" && entityID != "":
			entity, ok := orgEntities[entityID]
			if !ok {
				http.Error(w, "entity not found", 404)
				return
			}
			var req shared.UpdateEntityRequest
			json.NewDecoder(r.Body).Decode(&req)
			if req.Name != "" {
				entity.Name = req.Name
			}
			if req.Status != "" {
				entity.Status = req.Status
			}
			if req.Priority != "" {
				entity.Priority = req.Priority
			}
			entity.UpdatedAt = time.Now()
			json.NewEncoder(w).Encode(entity)

		case r.Method == "GET" && entityID != "":
			entity, ok := orgEntities[entityID]
			if !ok {
				http.Error(w, "entity not found", 404)
				return
			}
			json.NewEncoder(w).Encode(entity)

		case r.Method == "DELETE" && entityID != "":
			delete(orgEntities, entityID)
			json.NewEncoder(w).Encode(map[string]string{"message": "deleted"})

		default:
			http.Error(w, "not found", 404)
		}
	}
}

func splitPath(p string) []string {
	var parts []string
	current := ""
	for _, c := range p {
		if c == '/' {
			if current != "" {
				parts = append(parts, current)
			}
			current = ""
		} else {
			current += string(c)
		}
	}
	if current != "" {
		parts = append(parts, current)
	}
	return parts
}

func TestResolveEntityType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"uav", "aircraft_multirotor"},
		{"UAV", "aircraft_multirotor"},
		{"fixed_wing", "aircraft_fixed_wing"},
		{"isr_sensor", "sensor_platform"},
		{"camera", "sensor_platform"},
		{"boat", "surface_vessel_usv"},
		{"ground_vehicle", "ground_vehicle_wheeled"},
		{"aircraft_multirotor", "aircraft_multirotor"}, // already valid API type
		{"unknown_type", "unknown_type"},               // passthrough
	}

	for _, tt := range tests {
		got := shared.ResolveEntityType(tt.input)
		if got != tt.expected {
			t.Errorf("ResolveEntityType(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestRegister_FirstBoot_CreatesEntities(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "high", Status: "active"},
			{Name: "Ground Cam", Type: "isr_sensor", Priority: "normal", Status: "active"},
		},
	}

	state, err := Register(client, fleet, "nkey", "nats://test", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}

	if state.OrgName != "Test Org" {
		t.Errorf("OrgName = %q, want %q", state.OrgName, "Test Org")
	}
	if len(state.Entities) != 2 {
		t.Fatalf("got %d entities, want 2", len(state.Entities))
	}

	// Verify entity_ids are assigned
	for _, e := range state.Entities {
		if e.EntityID == "" {
			t.Error("entity_id should not be empty")
		}
	}

	// Verify entity types were mapped
	if state.Entities[0].Type != "aircraft_multirotor" {
		t.Errorf("entity type = %q, want aircraft_multirotor", state.Entities[0].Type)
	}
	if state.Entities[1].Type != "sensor_platform" {
		t.Errorf("entity type = %q, want sensor_platform", state.Entities[1].Type)
	}
}

func TestRegister_Idempotent_DoesNotDuplicate(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "high", Status: "active"},
		},
	}

	// First registration
	state1, err := Register(client, fleet, "", "", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Second registration with first state as previousState
	state2, err := Register(client, fleet, "", "", "localhost", "localhost", state1)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	// Should have same entity_id (not a new one)
	if state1.Entities[0].EntityID != state2.Entities[0].EntityID {
		t.Errorf("entity_id changed: %s -> %s", state1.Entities[0].EntityID, state2.Entities[0].EntityID)
	}

	// Should still be 1 entity total on server
	if len(state2.Entities) != 1 {
		t.Errorf("got %d entities, want 1", len(state2.Entities))
	}
}

func TestRegister_DriftDetection_UpdatesChanged(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "normal", Status: "active"},
		},
	}

	// First registration
	state1, err := Register(client, fleet, "", "", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	entityID := state1.Entities[0].EntityID

	// Change priority in fleet.yaml (drift)
	fleet.Entities[0].Priority = "critical"

	// Re-register with state tracking
	state2, err := Register(client, fleet, "", "", "localhost", "localhost", state1)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	// Same entity_id should be kept
	if state2.Entities[0].EntityID != entityID {
		t.Errorf("entity_id changed during drift update: %s -> %s", entityID, state2.Entities[0].EntityID)
	}

	// Verify server has updated priority
	mock.mu.Lock()
	for _, orgEntities := range mock.entities {
		for _, e := range orgEntities {
			if e.EntityID == entityID && e.Priority != "critical" {
				t.Errorf("server entity priority = %q, want critical", e.Priority)
			}
		}
	}
	mock.mu.Unlock()
}

func TestRegister_StaleEntityRemoval(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "high", Status: "active"},
			{Name: "Drone Beta", Type: "uav", Priority: "normal", Status: "active"},
		},
	}

	// Register both
	state1, err := Register(client, fleet, "", "", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if len(state1.Entities) != 2 {
		t.Fatalf("got %d entities, want 2", len(state1.Entities))
	}

	// Remove Drone Beta from fleet
	fleet.Entities = fleet.Entities[:1]

	// Re-register - should remove Drone Beta
	state2, err := Register(client, fleet, "", "", "localhost", "localhost", state1)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if len(state2.Entities) != 1 {
		t.Errorf("got %d entities after removal, want 1", len(state2.Entities))
	}
	if state2.Entities[0].Name != "Drone Alpha" {
		t.Errorf("remaining entity = %q, want Drone Alpha", state2.Entities[0].Name)
	}

	// Verify server only has 1 entity
	mock.mu.Lock()
	totalEntities := 0
	for _, orgEntities := range mock.entities {
		totalEntities += len(orgEntities)
	}
	mock.mu.Unlock()
	if totalEntities != 1 {
		t.Errorf("server has %d entities, want 1", totalEntities)
	}
}

func TestRegister_ReProvisions_MissingEntity(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "high", Status: "active"},
		},
	}

	// First registration
	state1, err := Register(client, fleet, "", "", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}
	oldEntityID := state1.Entities[0].EntityID

	// Simulate entity being deleted from server (external deletion)
	mock.mu.Lock()
	for _, orgEntities := range mock.entities {
		delete(orgEntities, oldEntityID)
	}
	mock.mu.Unlock()

	// Re-register with previous state - should detect missing and re-create
	state2, err := Register(client, fleet, "", "", "localhost", "localhost", state1)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if len(state2.Entities) != 1 {
		t.Fatalf("got %d entities, want 1", len(state2.Entities))
	}

	// New entity_id should be different (re-provisioned)
	if state2.Entities[0].EntityID == oldEntityID {
		t.Error("entity_id should have changed after re-provisioning")
	}
	if state2.Entities[0].Name != "Drone Alpha" {
		t.Errorf("re-provisioned entity name = %q, want Drone Alpha", state2.Entities[0].Name)
	}
}

func TestRegister_AddNewEntity(t *testing.T) {
	mock := newMockOverwatch()
	server := httptest.NewServer(mock)
	defer server.Close()

	client := shared.NewOverwatchClient(server.URL, "test-key")
	fleet := &shared.FleetConfig{
		Organization: shared.OrgConfig{Name: "Test Org", Type: "agency"},
		Entities: []shared.EntityConfig{
			{Name: "Drone Alpha", Type: "uav", Priority: "high", Status: "active"},
		},
	}

	// First registration
	state1, err := Register(client, fleet, "", "", "localhost", "localhost", nil)
	if err != nil {
		t.Fatalf("first Register: %v", err)
	}

	// Add a new entity to fleet
	fleet.Entities = append(fleet.Entities, shared.EntityConfig{
		Name: "Drone Beta", Type: "fixed_wing", Priority: "normal", Status: "active",
	})

	// Re-register
	state2, err := Register(client, fleet, "", "", "localhost", "localhost", state1)
	if err != nil {
		t.Fatalf("second Register: %v", err)
	}

	if len(state2.Entities) != 2 {
		t.Fatalf("got %d entities, want 2", len(state2.Entities))
	}

	// Original entity_id preserved
	if state2.Entities[0].EntityID != state1.Entities[0].EntityID {
		t.Errorf("original entity_id changed: %s -> %s", state1.Entities[0].EntityID, state2.Entities[0].EntityID)
	}

	// New entity has correct type mapping
	if state2.Entities[1].Type != "aircraft_fixed_wing" {
		t.Errorf("new entity type = %q, want aircraft_fixed_wing", state2.Entities[1].Type)
	}
}

func TestFleetConfigHash(t *testing.T) {
	tmp, _ := os.CreateTemp("", "fleet-*.yaml")
	defer os.Remove(tmp.Name())

	tmp.WriteString("organization:\n  name: test\n")
	tmp.Close()

	hash1, err := FleetConfigHash(tmp.Name())
	if err != nil {
		t.Fatalf("hash error: %v", err)
	}
	if hash1 == "" {
		t.Error("hash should not be empty")
	}

	// Same content = same hash
	hash2, _ := FleetConfigHash(tmp.Name())
	if hash1 != hash2 {
		t.Error("same content should produce same hash")
	}

	// Modified content = different hash
	os.WriteFile(tmp.Name(), []byte("organization:\n  name: changed\n"), 0644)
	hash3, _ := FleetConfigHash(tmp.Name())
	if hash1 == hash3 {
		t.Error("different content should produce different hash")
	}
}

func TestLoadC4State(t *testing.T) {
	// Non-existent file returns nil
	state := LoadC4State("/nonexistent/path")
	if state != nil {
		t.Error("expected nil for non-existent file")
	}

	// Valid state file
	tmp, _ := os.CreateTemp("", "c4-*.json")
	defer os.Remove(tmp.Name())

	testState := &shared.C4State{
		PulsarID: "test-pulsar",
		OrgID:    "org-1",
		OrgName:  "Test",
		Entities: []shared.EntityState{
			{EntityID: "ent-1", Name: "Drone"},
		},
	}
	data, _ := json.Marshal(testState)
	os.WriteFile(tmp.Name(), data, 0644)

	loaded := LoadC4State(tmp.Name())
	if loaded == nil {
		t.Fatal("expected non-nil state")
	}
	if loaded.PulsarID != "test-pulsar" {
		t.Errorf("PulsarID = %q, want test-pulsar", loaded.PulsarID)
	}
	if len(loaded.Entities) != 1 {
		t.Errorf("got %d entities, want 1", len(loaded.Entities))
	}
}

func TestNeedsDriftUpdate(t *testing.T) {
	remote := &shared.Entity{
		Name: "Drone", EntityType: "aircraft_multirotor",
		Status: "active", Priority: "high",
	}
	desired := shared.EntityConfig{
		Name: "Drone", Type: "uav", Priority: "high", Status: "active",
	}

	// No drift
	if needsDriftUpdate(remote, desired, "aircraft_multirotor") {
		t.Error("should not detect drift for matching config")
	}

	// Status drift
	desired.Status = "inactive"
	if !needsDriftUpdate(remote, desired, "aircraft_multirotor") {
		t.Error("should detect status drift")
	}
	desired.Status = "active"

	// Priority drift
	desired.Priority = "critical"
	if !needsDriftUpdate(remote, desired, "aircraft_multirotor") {
		t.Error("should detect priority drift")
	}
	desired.Priority = "high"

	// Name drift
	desired.Name = "Drone Renamed"
	if !needsDriftUpdate(remote, desired, "aircraft_multirotor") {
		t.Error("should detect name drift")
	}
}

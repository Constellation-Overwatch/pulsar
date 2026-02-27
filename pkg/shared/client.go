package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OverwatchClient wraps the Constellation Overwatch REST API.
type OverwatchClient struct {
	BaseURL    string
	apiKey     string
	httpClient *http.Client
}

// NewOverwatchClient creates a client for the Overwatch API.
func NewOverwatchClient(baseURL, apiKey string) *OverwatchClient {
	return &OverwatchClient{
		BaseURL: baseURL,
		apiKey:  apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// --- API types matching the OpenAPI spec ---

type HealthStatus struct {
	Status    string            `json:"status"`
	Service   string            `json:"service"`
	Version   string            `json:"version,omitempty"`
	Uptime    int64             `json:"uptime,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
	Details   map[string]string `json:"details,omitempty"`
}

type Organization struct {
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	OrgType     string    `json:"org_type"`
	Description string    `json:"description,omitempty"`
	Metadata    string    `json:"metadata,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateOrganizationRequest struct {
	Name        string                 `json:"name"`
	OrgType     string                 `json:"org_type"`
	Description string                 `json:"description,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

type Entity struct {
	EntityID    string    `json:"entity_id"`
	OrgID       string    `json:"org_id"`
	Name        string    `json:"name"`
	EntityType  string    `json:"entity_type"`
	Status      string    `json:"status"`
	Priority    string    `json:"priority"`
	IsLive      bool      `json:"is_live"`
	Latitude    float64   `json:"latitude,omitempty"`
	Longitude   float64   `json:"longitude,omitempty"`
	Altitude    float64   `json:"altitude,omitempty"`
	Heading     float64   `json:"heading,omitempty"`
	Velocity    float64   `json:"velocity,omitempty"`
	Metadata    string    `json:"metadata,omitempty"`
	VideoConfig string    `json:"video_config,omitempty"`
	Tags        string    `json:"tags,omitempty"`
	Components  string    `json:"components,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type CreateEntityRequest struct {
	Name        string                 `json:"name,omitempty"`
	EntityType  string                 `json:"entity_type"`
	Status      string                 `json:"status,omitempty"`
	Priority    string                 `json:"priority,omitempty"`
	Position    *Position              `json:"position,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	VideoConfig map[string]interface{} `json:"video_config,omitempty"`
}

type UpdateEntityRequest struct {
	Name        string                 `json:"name,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Priority    string                 `json:"priority,omitempty"`
	IsLive      *bool                  `json:"is_live,omitempty"`
	Position    *Position              `json:"position,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
	VideoConfig map[string]interface{} `json:"video_config,omitempty"`
}

type Position struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude,omitempty"`
}

type APIError struct {
	Status int    `json:"status"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("overwatch api error %d: %s - %s", e.Status, e.Title, e.Detail)
}

// --- HTTP helpers ---

func (c *OverwatchClient) doRequest(method, path string, body interface{}) ([]byte, error) {
	url := c.BaseURL + path

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 2 * time.Second)
		}

		var reqBody io.Reader
		if bodyBytes != nil {
			reqBody = bytes.NewReader(bodyBytes)
		}

		req, err := http.NewRequest(method, url, reqBody)
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("X-API-Key", c.apiKey)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("http request: %w", err)
			continue
		}

		respBody, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("read response: %w", err)
			continue
		}

		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error %d: %s", resp.StatusCode, string(respBody))
			continue
		}

		if resp.StatusCode >= 400 {
			var apiErr APIError
			if json.Unmarshal(respBody, &apiErr) == nil && apiErr.Detail != "" {
				return nil, &apiErr
			}
			return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
		}

		return respBody, nil
	}

	return nil, fmt.Errorf("request failed after 3 attempts: %w", lastErr)
}

// --- API methods ---

func (c *OverwatchClient) HealthCheck() (*HealthStatus, error) {
	data, err := c.doRequest("GET", "/api/v1/health", nil)
	if err != nil {
		return nil, err
	}
	var status HealthStatus
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("decode health: %w", err)
	}
	return &status, nil
}

func (c *OverwatchClient) ListOrganizations() ([]Organization, error) {
	data, err := c.doRequest("GET", "/api/v1/organizations", nil)
	if err != nil {
		return nil, err
	}
	var orgs []Organization
	if err := json.Unmarshal(data, &orgs); err != nil {
		return nil, fmt.Errorf("decode organizations: %w", err)
	}
	return orgs, nil
}

func (c *OverwatchClient) CreateOrganization(req CreateOrganizationRequest) (*Organization, error) {
	data, err := c.doRequest("POST", "/api/v1/organizations", req)
	if err != nil {
		return nil, err
	}
	var org Organization
	if err := json.Unmarshal(data, &org); err != nil {
		return nil, fmt.Errorf("decode organization: %w", err)
	}
	return &org, nil
}

func (c *OverwatchClient) ListEntities(orgID string) ([]Entity, error) {
	data, err := c.doRequest("GET", fmt.Sprintf("/api/v1/organizations/%s/entities", orgID), nil)
	if err != nil {
		return nil, err
	}
	var entities []Entity
	if err := json.Unmarshal(data, &entities); err != nil {
		return nil, fmt.Errorf("decode entities: %w", err)
	}
	return entities, nil
}

func (c *OverwatchClient) CreateEntity(orgID string, req CreateEntityRequest) (*Entity, error) {
	data, err := c.doRequest("POST", fmt.Sprintf("/api/v1/organizations/%s/entities", orgID), req)
	if err != nil {
		return nil, err
	}
	var entity Entity
	if err := json.Unmarshal(data, &entity); err != nil {
		return nil, fmt.Errorf("decode entity: %w", err)
	}
	return &entity, nil
}

func (c *OverwatchClient) UpdateEntity(orgID, entityID string, req UpdateEntityRequest) (*Entity, error) {
	data, err := c.doRequest("PUT", fmt.Sprintf("/api/v1/organizations/%s/entities/%s", orgID, entityID), req)
	if err != nil {
		return nil, err
	}
	var entity Entity
	if err := json.Unmarshal(data, &entity); err != nil {
		return nil, fmt.Errorf("decode entity: %w", err)
	}
	return &entity, nil
}

func (c *OverwatchClient) DeleteEntity(orgID, entityID string) error {
	_, err := c.doRequest("DELETE", fmt.Sprintf("/api/v1/organizations/%s/entities/%s", orgID, entityID), nil)
	return err
}

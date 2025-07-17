package kandji

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"kandji-cloudflare-device-sync/config"
	"kandji-cloudflare-device-sync/internal/ratelimit"
)

// User represents the user information from the Kandji API.
type User struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	ID         string `json:"id"`
	IsArchived bool   `json:"is_archived"`
}

// Device represents a device record from the Kandji API.
type Device struct {
	DeviceName     string   `json:"device_name"`
	SerialNumber   string   `json:"serial_number"`
	Platform       string   `json:"platform"`
	Model          string   `json:"model"`
	OSVersion      string   `json:"os_version"`
	UserObj        *User    `json:"-"` // We'll handle this manually
	UserEmail      string   `json:"-"` // We'll populate this from UserObj.Email if user exists
	AssetTag       string   `json:"asset_tag"`
	LastSeen       string   `json:"last_seen"`
	EnrollmentDate string   `json:"enrollment_date"`
	DeviceID       string   `json:"device_id"`
	MacAddress     string   `json:"mac_address"`
	Tags           []string `json:"tags"`
	BlueprintID    string   `json:"blueprint_id"`
	BlueprintName  string   `json:"blueprint_name"`
}

// UnmarshalJSON implements custom JSON unmarshaling for Device to handle the user field properly
func (d *Device) UnmarshalJSON(data []byte) error {
	// Create a temporary struct with all fields except user
	type TempDevice struct {
		DeviceName     string      `json:"device_name"`
		SerialNumber   string      `json:"serial_number"`
		Platform       string      `json:"platform"`
		Model          string      `json:"model"`
		OSVersion      string      `json:"os_version"`
		User           interface{} `json:"user"` // Accept any type for user field
		AssetTag       string      `json:"asset_tag"`
		LastSeen       string      `json:"last_seen"`
		EnrollmentDate string      `json:"enrollment_date"`
		DeviceID       string      `json:"device_id"`
		MacAddress     string      `json:"mac_address"`
		Tags           []string    `json:"tags"`
		BlueprintID    string      `json:"blueprint_id"`
		BlueprintName  string      `json:"blueprint_name"`
	}

	var temp TempDevice
	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	// Copy all non-user fields
	d.DeviceName = temp.DeviceName
	d.SerialNumber = temp.SerialNumber
	d.Platform = temp.Platform
	d.Model = temp.Model
	d.OSVersion = temp.OSVersion
	d.AssetTag = temp.AssetTag
	d.LastSeen = temp.LastSeen
	d.EnrollmentDate = temp.EnrollmentDate
	d.DeviceID = temp.DeviceID
	d.MacAddress = temp.MacAddress
	d.Tags = temp.Tags
	d.BlueprintID = temp.BlueprintID
	d.BlueprintName = temp.BlueprintName

	// Handle the user field based on its type
	if temp.User != nil {
		switch userValue := temp.User.(type) {
		case map[string]interface{}:
			// User is an object, convert it to User struct
			userBytes, err := json.Marshal(userValue)
			if err != nil {
				return fmt.Errorf("failed to marshal user object: %w", err)
			}
			var user User
			if err := json.Unmarshal(userBytes, &user); err != nil {
				return fmt.Errorf("failed to unmarshal user object: %w", err)
			}
			d.UserObj = &user
			d.UserEmail = user.Email
		case string:
			// User is a string, leave UserObj as nil but could log this if needed
			d.UserObj = nil
			d.UserEmail = ""
		default:
			// User is some other type, treat as no user
			d.UserObj = nil
			d.UserEmail = ""
		}
	}

	return nil
}

// DevicesResponse represents the paginated response from Kandji API
type DevicesResponse struct {
	Results  []Device `json:"results"`
	Count    int      `json:"count"`
	Next     *string  `json:"next"`
	Previous *string  `json:"previous"`
}

// Client is a client for interacting with the Kandji API.
type Client struct {
	apiURL      string
	apiToken    string
	httpClient  *http.Client
	rateLimiter *ratelimit.Limiter
}

// NewClient creates a new Kandji API client.
func NewClient(cfg config.KandjiConfig, rateLimiter *ratelimit.Limiter) (*Client, error) {
	// Validate the API URL and token
	if cfg.ApiURL == "" {
		return nil, fmt.Errorf("kandji api url is required")
	}
	if cfg.ApiToken == "" {
		return nil, fmt.Errorf("kandji api token is required")
	}
	if !strings.HasPrefix(cfg.ApiURL, "https://") {
		return nil, fmt.Errorf("kandji api url must start with https://")
	}

	return &Client{
		apiURL:      strings.TrimSuffix(cfg.ApiURL, "/"),
		apiToken:    cfg.ApiToken,
		rateLimiter: rateLimiter,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// GetDevices retrieves a list of all devices from Kandji with pagination support.
func (c *Client) GetDevices(ctx context.Context) ([]Device, error) {
	var allDevices []Device
	nextURL := c.apiURL + "/api/v1/devices"
	maxPages := 1000 // Safety limit to prevent infinite loops
	pageCount := 0

	for nextURL != "" && pageCount < maxPages {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		pageCount++

		// Apply rate limiting
		if c.rateLimiter != nil {
			if err := c.rateLimiter.WaitForKandji(ctx); err != nil {
				return nil, fmt.Errorf("rate limiter cancelled: %w", err)
			}
		}

		req, err := http.NewRequest("GET", nextURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Kandji API request: %w", err)
		}

		// Add context to the request
		req = req.WithContext(ctx)

		req.Header.Set("Authorization", "Bearer "+c.apiToken)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "kandji-cloudflare-device-sync/1.0")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute Kandji API request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("received non-200 status from Kandji API: %s, body: %s", resp.Status, string(body))
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read Kandji API response body: %w", err)
		}

		// Try to parse as paginated response first
		var paginatedResp DevicesResponse
		if err := json.Unmarshal(body, &paginatedResp); err == nil {
			allDevices = append(allDevices, paginatedResp.Results...)
			if paginatedResp.Next != nil {
				nextURL = *paginatedResp.Next
			} else {
				nextURL = ""
			}
		} else {
			// Fallback: try to parse as direct array
			var devices []Device
			if err := json.Unmarshal(body, &devices); err != nil {
				fmt.Printf("Error details: %s\n", string(body))
				return nil, fmt.Errorf("failed to unmarshal Kandji devices JSON: %w", err)
			}
			allDevices = append(allDevices, devices...)
			nextURL = ""
		}
	}

	// Check if we hit the page limit
	if pageCount >= maxPages {
		return allDevices, fmt.Errorf("reached maximum page limit (%d pages), there may be more devices", maxPages)
	}

	return allDevices, nil
}

package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"cloudflare-kandji-device-sync/config"
	"cloudflare-kandji-device-sync/internal/ratelimit"
)

const (
	cloudflareAPIBaseV4 = "https://api.cloudflare.com/client/v4"
)

// Client represents a Cloudflare API client for managing Gateway device lists
type Client struct {
	apiToken    string
	accountID   string
	listID      string
	rateLimiter *ratelimit.Limiter
	httpClient  *http.Client
	log         *slog.Logger
}

// DeviceResult represents the result of a device operation
type DeviceResult struct {
	SerialNumber string
	Success      bool
	Error        error
}

// BulkResult represents the result of a bulk operation
type BulkResult struct {
	SuccessCount  int
	FailedDevices []DeviceResult
	Errors        []error
}

// Gateway list API response structures
type GatewayListResponse struct {
	Success bool         `json:"success"`
	Errors  []any        `json:"errors"`
	Result  *GatewayList `json:"result"`
}

type GatewayList struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Type        string    `json:"type"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type GatewayListItemsResponse struct {
	Success bool              `json:"success"`
	Errors  []any             `json:"errors"`
	Result  []GatewayListItem `json:"result"`
}

type GatewayListItem struct {
	ID        string    `json:"id"`
	Value     string    `json:"value"`
	Comment   string    `json:"comment,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type GatewayListItemCreateRequest struct {
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

type GatewayListItemsCreateRequest struct {
	Append      []GatewayListItemCreateRequest `json:"append,omitempty"`
	Replace     []GatewayListItemCreateRequest `json:"replace,omitempty"`
	Remove      []string                       `json:"remove,omitempty"`
	Description string                         `json:"description,omitempty"`
}

type GatewayListItemsDeleteRequest struct {
	Remove []GatewayListItemCreateRequest `json:"remove"`
}

type GatewayListItemDeleteRequest struct {
	ID string `json:"id"`
}

// NewClient creates a new Cloudflare Gateway client
func NewClient(cfg config.CloudflareConfig, rateLimiter *ratelimit.Limiter, log *slog.Logger) (*Client, error) {
	if cfg.ApiToken == "" {
		return nil, fmt.Errorf("Cloudflare API token is required")
	}
	if cfg.AccountID == "" {
		return nil, fmt.Errorf("Cloudflare account ID is required")
	}
	if cfg.ListID == "" {
		return nil, fmt.Errorf("Cloudflare list ID is required")
	}

	return &Client{
		apiToken:    cfg.ApiToken,
		accountID:   cfg.AccountID,
		listID:      cfg.ListID,
		rateLimiter: rateLimiter,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		log: log,
	}, nil
}

// makeRequest makes an HTTP request to the Cloudflare API
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	// Apply rate limiting
	if c.rateLimiter != nil {
		if err := c.rateLimiter.WaitForCloudflare(ctx); err != nil {
			return nil, fmt.Errorf("rate limiter cancelled: %w", err)
		}
	}

	var reqBody io.Reader
	var payloadLog string
	if body != nil {
		jsonBody, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewReader(jsonBody)
		payloadLog = string(jsonBody)
	}

	url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s%s", cloudflareAPIBaseV4, c.accountID, c.listID, endpoint)
	c.log.Debug("Cloudflare API Request", "method", method, "url", url, "payload", payloadLog)
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		c.log.Error("Cloudflare API Error Response", "status", resp.StatusCode, "headers", resp.Header, "body", string(body))
		resp.Body = io.NopCloser(bytes.NewReader(body)) // allow further reading
	}

	return resp, nil
}

/*
ValidateListExists checks if the specified Gateway list exists and is accessible.
*/
func (c *Client) ValidateListExists(ctx context.Context) error {
	return c.ValidateListExistsByID(ctx, c.listID)
}

/*
ValidateListExistsByID checks if the specified Gateway list exists and is accessible by ID.
Returns nil if the list exists and is accessible, or an error otherwise.
*/
func (c *Client) ValidateListExistsByID(ctx context.Context, listID string) error {
	resp, err := c.makeRequest(ctx, "GET", "", nil)
	if err != nil {
		return fmt.Errorf("failed to validate list existence: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to validate list existence: HTTP %d - %s", resp.StatusCode, string(body))
	}

	var response GatewayListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return fmt.Errorf("failed to decode list response: %w", err)
	}

	if !response.Success {
		return fmt.Errorf("failed to validate list existence: %v", response.Errors)
	}

	if response.Result == nil {
		return fmt.Errorf("list with ID %s not found", listID)
	}

	c.log.Info("Successfully validated Cloudflare Gateway list",
		"list_id", listID,
		"list_name", response.Result.Name,
		"list_type", response.Result.Type)

	if response.Result.Type != "SERIAL" {
		c.log.Warn("Cloudflare list is not of type SERIAL. Device serial sync may not work as expected.",
			"list_id", listID, "list_type", response.Result.Type)
	}

	return nil
}

/*
GetListTypeByID fetches the type of a Cloudflare list by its ID.
Returns the type string (e.g., "SERIAL") or an error.
*/
func (c *Client) GetListTypeByID(ctx context.Context, listID string) (string, error) {
	endpoint := ""
	url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s%s", cloudflareAPIBaseV4, c.accountID, listID, endpoint)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch list type: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("failed to fetch list type: HTTP %d - %s", resp.StatusCode, string(body))
	}

	var response GatewayListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return "", fmt.Errorf("failed to decode list response: %w", err)
	}
	if !response.Success || response.Result == nil {
		return "", fmt.Errorf("failed to fetch list type: %v", response.Errors)
	}
	return response.Result.Type, nil
}

/*
GetListItemsByID retrieves all items from the specified Cloudflare Gateway list by ID,
handling pagination to ensure the full list is returned.
Returns a slice of GatewayListItem (with Value and Comment).
*/
func (c *Client) GetListItemsByID(ctx context.Context, listID string) ([]GatewayListItem, error) {
	c.log.Debug("Fetching items from Cloudflare Gateway list", "list_id", listID)

	var allItems []GatewayListItem
	page := 1
	perPage := 1000 // Cloudflare API max is 1000

	for {
		endpoint := fmt.Sprintf("/items?page=%d&per_page=%d", page, perPage)
		url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s%s", cloudflareAPIBaseV4, c.accountID, listID, endpoint)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.apiToken)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("failed to execute request: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("failed to get list items: HTTP %d - %s", resp.StatusCode, string(body))
		}

		var response GatewayListItemsResponse
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return nil, fmt.Errorf("failed to decode list items response: %w", err)
		}

		if !response.Success {
			return nil, fmt.Errorf("failed to get list items: %v", response.Errors)
		}

		allItems = append(allItems, response.Result...)

		// If we got less than perPage, we're done
		if len(response.Result) < perPage {
			break
		}
		page++
	}

	c.log.Debug("Successfully fetched Gateway list items", "count", len(allItems))
	return allItems, nil
}

/*
GetListMetadataByID fetches the metadata (including description) for a Cloudflare list by its ID.
*/
func (c *Client) GetListMetadataByID(ctx context.Context, listID string) (*GatewayList, error) {
	url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s", cloudflareAPIBaseV4, c.accountID, listID)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch list metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to fetch list metadata: HTTP %d - %s", resp.StatusCode, string(body))
	}

	var response GatewayListResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, fmt.Errorf("failed to decode list metadata response: %w", err)
	}
	if !response.Success || response.Result == nil {
		return nil, fmt.Errorf("failed to fetch list metadata: %v", response.Errors)
	}
	return response.Result, nil
}

/*
GetListItems retrieves all items from the specified Cloudflare Gateway list,
handling pagination to ensure the full list is returned.
*/
func (c *Client) GetListItems(ctx context.Context) ([]string, error) {
	c.log.Debug("Fetching items from Cloudflare Gateway list", "list_id", c.listID)

	var allItems []string
	page := 1
	perPage := 1000 // Cloudflare API max is 1000

	for {
		endpoint := fmt.Sprintf("/items?page=%d&per_page=%d", page, perPage)
		resp, err := c.makeRequest(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get list items: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			return nil, fmt.Errorf("failed to get list items: HTTP %d - %s", resp.StatusCode, string(body))
		}

		var response GatewayListItemsResponse
		if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
			return nil, fmt.Errorf("failed to decode list items response: %w", err)
		}

		if !response.Success {
			return nil, fmt.Errorf("failed to get list items: %v", response.Errors)
		}

		for _, item := range response.Result {
			allItems = append(allItems, item.Value)
		}

		// If we got less than perPage, we're done
		if len(response.Result) < perPage {
			break
		}
		page++
	}

	c.log.Debug("Successfully fetched Gateway list items", "count", len(allItems))
	return allItems, nil
}

/*
AppendDevicesWithDescription adds new devices to the Cloudflare Gateway list (does not replace),
and sets the list description.
This uses PATCH /accounts/{account_id}/gateway/lists/{list_id} with "append" and "description".
*/
func (c *Client) AppendDevicesWithDescription(ctx context.Context, items []GatewayListItemCreateRequest, batchSize int, description string) error {
	if len(items) == 0 {
		return nil
	}

	c.log.Info("Appending devices to Cloudflare Gateway list", "count", len(items), "description", description)

	requestBody := GatewayListItemsCreateRequest{
		Append:      items,
		Description: description,
	}

	url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s", cloudflareAPIBaseV4, c.accountID, c.listID)
	c.log.Debug("Cloudflare PATCH Request (append+description)", "url", url, "payload", requestBody)

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal PATCH body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(jsonBody))
	if err != nil {
		return fmt.Errorf("failed to create PATCH request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute PATCH request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.log.Error("Cloudflare PATCH Error", "status", resp.StatusCode, "body", string(body))
		return fmt.Errorf("PATCH failed: HTTP %d - %s", resp.StatusCode, string(body))
	}

	var response GatewayListResponse
	if err := json.Unmarshal(body, &response); err != nil {
		c.log.Error("Failed to decode PATCH response", "error", err)
		return fmt.Errorf("decode failed: %w", err)
	}

	if !response.Success {
		err := fmt.Errorf("PATCH failed: %v", response.Errors)
		c.log.Error("Cloudflare PATCH failed", "error", err)
		return err
	}

	c.log.Info("Successfully appended devices to Cloudflare Gateway list", "count", len(items))
	return nil
}

/*
DeleteDevices removes Kandji devices from the Cloudflare Gateway list by serial number.
This uses PATCH /accounts/{account_id}/gateway/lists/{list_id} with "remove".
*/
func (c *Client) DeleteDevices(ctx context.Context, serialNumbers []string, batchSize int) (*BulkResult, error) {
	result := &BulkResult{
		SuccessCount:  0,
		FailedDevices: []DeviceResult{},
		Errors:        []error{},
	}

	if len(serialNumbers) == 0 {
		return result, nil
	}

	c.log.Info("Removing devices from Cloudflare Gateway list", "count", len(serialNumbers), "batch_size", batchSize)

	// Process serials in batches
	for i := 0; i < len(serialNumbers); i += batchSize {
		end := i + batchSize
		if end > len(serialNumbers) {
			end = len(serialNumbers)
		}
		batch := serialNumbers[i:end]
		batchResult := c.deleteDeviceBatch(ctx, batch)
		result.SuccessCount += batchResult.SuccessCount
		result.FailedDevices = append(result.FailedDevices, batchResult.FailedDevices...)
		result.Errors = append(result.Errors, batchResult.Errors...)
	}

	return result, nil
}

// deleteDeviceBatch removes a batch of serial numbers from the Cloudflare Gateway list
func (c *Client) deleteDeviceBatch(ctx context.Context, serialNumbers []string) *BulkResult {
	result := &BulkResult{
		SuccessCount:  0,
		FailedDevices: []DeviceResult{},
		Errors:        []error{},
	}

	var removeItems []string
	for _, serial := range serialNumbers {
		if serial == "" {
			result.FailedDevices = append(result.FailedDevices, DeviceResult{
				SerialNumber: serial,
				Success:      false,
				Error:        fmt.Errorf("empty serial number"),
			})
			continue
		}
		removeItems = append(removeItems, serial)
	}

	if len(removeItems) == 0 {
		return result
	}

	requestBody := GatewayListItemsCreateRequest{
		Remove: removeItems,
	}

	url := fmt.Sprintf("%s/accounts/%s/gateway/lists/%s", cloudflareAPIBaseV4, c.accountID, c.listID)
	c.log.Debug("Cloudflare PATCH Request (remove)", "url", url, "payload", requestBody)

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("failed to marshal PATCH body: %w", err))
		return result
	}

	req, err := http.NewRequestWithContext(ctx, "PATCH", url, bytes.NewReader(jsonBody))
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("failed to create PATCH request: %w", err))
		return result
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("failed to execute PATCH request: %w", err))
		return result
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		c.log.Error("Cloudflare PATCH Error (remove)", "status", resp.StatusCode, "body", string(body))
		result.Errors = append(result.Errors, fmt.Errorf("PATCH failed: HTTP %d - %s", resp.StatusCode, string(body)))
		return result
	}

	var response GatewayListResponse
	if err := json.Unmarshal(body, &response); err != nil {
		c.log.Error("Failed to decode PATCH response (remove)", "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("decode failed: %w", err))
		return result
	}

	if !response.Success {
		err := fmt.Errorf("PATCH failed: %v", response.Errors)
		c.log.Error("Cloudflare PATCH failed (remove)", "error", err)
		result.Errors = append(result.Errors, err)
		return result
	}

	result.SuccessCount = len(removeItems)
	c.log.Info("Successfully removed devices from Cloudflare Gateway list", "count", result.SuccessCount)
	return result
}

/* Deprecated: addDeviceBatch is no longer used. Use AppendDevices instead. */

/* Deprecated: old DeleteDevices logic replaced by new PATCH/remove logic. */

/* Deprecated: getListItemsWithIDs and deleteItemBatch are no longer used. */

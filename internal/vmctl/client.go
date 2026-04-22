package vmctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Client is an HTTP client for the vmctl service. The proxy uses this
// client to resolve user VM ownership before routing requests.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

// NewClient creates a vmctl client pointing at the given base URL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Resolve resolves or assigns a VM for the given user ID. Returns the
// ownership information including the sandbox URL where the user's VM
// is reachable (VAL-VM-001).
func (c *Client) Resolve(userID string) (*resolveResponse, error) {
	return c.ResolveDesktop(userID, PrimaryDesktopID)
}

// ResolveDesktop resolves or assigns a VM for the given user/desktop pair.
func (c *Client) ResolveDesktop(userID, desktopID string) (*resolveResponse, error) {
	reqBody := resolveRequest{UserID: userID, DesktopID: normalizeDesktopID(desktopID)}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: marshal resolve request: %w", err)
	}

	endpoint := ResolveEndpoint(c.baseURL)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("vmctl client: create resolve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: resolve call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: read resolve response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp vmctlErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("vmctl client: resolve failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("vmctl client: resolve failed with status %s", resp.Status)
	}

	var result resolveResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vmctl client: decode resolve response: %w", err)
	}

	return &result, nil
}

// ForkDesktop creates or resumes a distinct interactive VM for the target
// desktop, derived from the source desktop.
func (c *Client) ForkDesktop(userID, sourceDesktopID, targetDesktopID string) (*resolveResponse, error) {
	reqBody := forkDesktopRequest{
		UserID:          userID,
		SourceDesktopID: normalizeDesktopID(sourceDesktopID),
		TargetDesktopID: normalizeDesktopID(targetDesktopID),
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: marshal fork request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, ForkDesktopEndpoint(c.baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("vmctl client: create fork request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: fork call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: read fork response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp vmctlErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("vmctl client: fork failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("vmctl client: fork failed with status %s", resp.Status)
	}

	var result resolveResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vmctl client: decode fork response: %w", err)
	}
	return &result, nil
}

// PublishDesktop marks a candidate desktop as user-switchable.
func (c *Client) PublishDesktop(userID, desktopID string) (*resolveResponse, error) {
	reqBody := resolveRequest{UserID: userID, DesktopID: normalizeDesktopID(desktopID)}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: marshal publish request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, PublishDesktopEndpoint(c.baseURL), bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("vmctl client: create publish request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: publish call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: read publish response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		var errResp vmctlErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("vmctl client: publish failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("vmctl client: publish failed with status %s", resp.Status)
	}

	var result resolveResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vmctl client: decode publish response: %w", err)
	}
	return &result, nil
}

// Lookup returns the current ownership for a user without creating a VM.
// Returns nil if no ownership exists.
func (c *Client) Lookup(userID string) (*ownershipResponse, error) {
	return c.LookupDesktop(userID, PrimaryDesktopID)
}

// LookupDesktop returns the current ownership for a user/desktop pair without
// creating a VM. Returns nil if no ownership exists.
func (c *Client) LookupDesktop(userID, desktopID string) (*ownershipResponse, error) {
	endpoint := LookupEndpoint(c.baseURL) + "?user_id=" + url.QueryEscape(userID) + "&desktop_id=" + url.QueryEscape(normalizeDesktopID(desktopID))
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: create lookup request: %w", err)
	}
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: lookup call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("vmctl client: read lookup response: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}

	if resp.StatusCode != http.StatusOK {
		var errResp vmctlErrorResponse
		if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
			return nil, fmt.Errorf("vmctl client: lookup failed: %s", errResp.Error)
		}
		return nil, fmt.Errorf("vmctl client: lookup failed with status %s", resp.Status)
	}

	var result ownershipResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("vmctl client: decode lookup response: %w", err)
	}

	return &result, nil
}

// Stop requests vmctl to stop the VM for the given user.
func (c *Client) Stop(userID string) error {
	return c.StopDesktop(userID, PrimaryDesktopID)
}

// Remove requests vmctl to remove the ownership for the given user.
func (c *Client) Remove(userID string) error {
	return c.RemoveDesktop(userID, PrimaryDesktopID)
}

// StopDesktop requests vmctl to stop the VM for the given user/desktop pair.
func (c *Client) StopDesktop(userID, desktopID string) error {
	return c.postAction(StopEndpoint(c.baseURL), userID, desktopID)
}

// RemoveDesktop requests vmctl to remove the ownership for the given
// user/desktop pair.
func (c *Client) RemoveDesktop(userID, desktopID string) error {
	return c.postAction(RemoveEndpoint(c.baseURL), userID, desktopID)
}

// postAction sends a POST request with a user_id/desktop_id body to the given
// endpoint.
func (c *Client) postAction(endpoint, userID, desktopID string) error {
	reqBody := resolveRequest{UserID: userID, DesktopID: normalizeDesktopID(desktopID)}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("vmctl client: marshal request: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("vmctl client: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Caller", "true")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("vmctl client: call failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // drain body for connection reuse

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("vmctl client: action failed with status %s", resp.Status)
	}

	return nil
}

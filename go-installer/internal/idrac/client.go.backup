package idrac

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/logger"
)

// Client represents an iDRAC API client
type Client struct {
	config     *config.IDRACConfig
	httpClient *http.Client
	logger     *logger.Logger
	baseURL    string
}

// SystemInfo represents system information from iDRAC
type SystemInfo struct {
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	SerialNumber string `json:"SerialNumber"`
	BiosVersion  string `json:"BiosVersion"`
	PowerState   string `json:"PowerState"`
	Health       string `json:"Health"`
}

// BootConfig represents boot configuration
type BootConfig struct {
	BootSourceOverrideTarget    string `json:"BootSourceOverrideTarget"`
	BootSourceOverrideEnabled   string `json:"BootSourceOverrideEnabled"`
}

// SystemBoot represents system boot settings
type SystemBoot struct {
	Boot BootConfig `json:"Boot"`
}

// ResetRequest represents a system reset request
type ResetRequest struct {
	ResetType string `json:"ResetType"`
}

// VirtualMediaRequest represents a virtual media request
type VirtualMediaRequest struct {
	Image string `json:"Image,omitempty"`
}

// NewClient creates a new iDRAC client
func NewClient(cfg *config.IDRACConfig, log *logger.Logger) *Client {
	// Create HTTP client with custom transport
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: !cfg.VerifySSL,
		},
	}

	httpClient := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.Timeout) * time.Second,
	}

	baseURL := fmt.Sprintf("https://%s", cfg.IP)

	return &Client{
		config:     cfg,
		httpClient: httpClient,
		logger:     log,
		baseURL:    baseURL,
	}
}

// makeRequest makes an HTTP request to the iDRAC API
func (c *Client) makeRequest(ctx context.Context, method, endpoint string, body interface{}) (*http.Response, error) {
	var reqBody io.Reader
	if body != nil {
		jsonData, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		reqBody = bytes.NewBuffer(jsonData)
	}

	url := c.baseURL + endpoint
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set authentication
	req.SetBasicAuth(c.config.Username, c.config.Password)
	req.Header.Set("Content-Type", "application/json")

	c.logger.LogDebug("Making %s request to %s", method, url)
	if body != nil {
		c.logger.LogDebug("Request body: %+v", body)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to make request: %w", err)
	}

	return resp, nil
}

// CheckConnectivity checks if iDRAC is reachable
func (c *Client) CheckConnectivity(ctx context.Context) error {
	c.logger.LogInfo("Checking iDRAC connectivity to %s...", c.config.IP)

	resp, err := c.makeRequest(ctx, "GET", "/redfish/v1/Systems/System.Embedded.1", nil)
	if err != nil {
		c.logger.LogError("Failed to connect to iDRAC at %s: %v", c.config.IP, err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.logger.LogError("iDRAC returned status code: %d", resp.StatusCode)
		return fmt.Errorf("iDRAC returned status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %w", err)
	}

	if !strings.Contains(string(body), "System.Embedded.1") {
		c.logger.LogError("Invalid response from iDRAC")
		return fmt.Errorf("invalid response from iDRAC")
	}

	c.logger.LogSuccess("iDRAC connectivity verified")
	return nil
}

// GetSystemInfo retrieves system information
func (c *Client) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	c.logger.LogInfo("Getting system information...")

	resp, err := c.makeRequest(ctx, "GET", "/redfish/v1/Systems/System.Embedded.1", nil)
	if err != nil {
		c.logger.LogError("Failed to get system information: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get system info, status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var systemInfo SystemInfo
	if err := json.Unmarshal(body, &systemInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal system info: %w", err)
	}

	c.logger.LogInfo("System Information:")
	c.logger.LogInfo("  Manufacturer: %s", systemInfo.Manufacturer)
	c.logger.LogInfo("  Model: %s", systemInfo.Model)
	c.logger.LogInfo("  Serial Number: %s", systemInfo.SerialNumber)
	c.logger.LogInfo("  BIOS Version: %s", systemInfo.BiosVersion)

	return &systemInfo, nil
}

// GetSystemPowerState retrieves the current power state
func (c *Client) GetSystemPowerState(ctx context.Context) (string, error) {
	c.logger.LogInfo("Getting system power state...")

	systemInfo, err := c.GetSystemInfo(ctx)
	if err != nil {
		return "", err
	}

	c.logger.LogInfo("System power state: %s", systemInfo.PowerState)
	return systemInfo.PowerState, nil
}

// GetSystemHealth retrieves the system health status
func (c *Client) GetSystemHealth(ctx context.Context) (string, error) {
	c.logger.LogInfo("Getting system health status...")

	systemInfo, err := c.GetSystemInfo(ctx)
	if err != nil {
		return "", err
	}

	c.logger.LogInfo("System health: %s", systemInfo.Health)
	return systemInfo.Health, nil
}

// PowerOnSystem powers on the system
func (c *Client) PowerOnSystem(ctx context.Context) error {
	c.logger.LogInfo("Powering on system...")

	// Check current power state
	powerState, err := c.GetSystemPowerState(ctx)
	if err != nil {
		return err
	}

	if powerState == "On" {
		c.logger.LogInfo("System is already powered on")
		return nil
	}

	resetReq := ResetRequest{ResetType: "On"}
	resp, err := c.makeRequest(ctx, "POST", "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset", resetReq)
	if err != nil {
		c.logger.LogError("Failed to power on system: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to power on system, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("System power on command sent successfully")
	return c.WaitForSystemPowerOn(ctx)
}

// PowerOffSystem powers off the system
func (c *Client) PowerOffSystem(ctx context.Context) error {
	c.logger.LogInfo("Powering off system...")

	// Check current power state
	powerState, err := c.GetSystemPowerState(ctx)
	if err != nil {
		return err
	}

	if powerState == "Off" {
		c.logger.LogInfo("System is already powered off")
		return nil
	}

	resetReq := ResetRequest{ResetType: "ForceOff"}
	resp, err := c.makeRequest(ctx, "POST", "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset", resetReq)
	if err != nil {
		c.logger.LogError("Failed to power off system: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to power off system, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("System power off command sent successfully")
	return c.WaitForSystemPowerOff(ctx)
}

// RestartSystem restarts the system
func (c *Client) RestartSystem(ctx context.Context) error {
	c.logger.LogInfo("Restarting system...")

	resetReq := ResetRequest{ResetType: "ForceRestart"}
	resp, err := c.makeRequest(ctx, "POST", "/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset", resetReq)
	if err != nil {
		c.logger.LogError("Failed to restart system: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to restart system, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("System restart command sent successfully")
	return nil
}

// SetVirtualCDBoot sets the boot device to Virtual CD/DVD
func (c *Client) SetVirtualCDBoot(ctx context.Context) error {
	c.logger.LogInfo("Setting boot device to Virtual CD/DVD...")

	bootConfig := SystemBoot{
		Boot: BootConfig{
			BootSourceOverrideTarget:  "Cd",
			BootSourceOverrideEnabled: "Once",
		},
	}

	resp, err := c.makeRequest(ctx, "PATCH", "/redfish/v1/Systems/System.Embedded.1", bootConfig)
	if err != nil {
		c.logger.LogError("Failed to set boot device to Virtual CD/DVD: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to set boot device to Virtual CD/DVD, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("Boot device set to Virtual CD/DVD successfully")
	return nil
}

// SetHDDBoot sets the boot device to HDD
func (c *Client) SetHDDBoot(ctx context.Context) error {
	c.logger.LogInfo("Setting boot device to HDD...")

	bootConfig := SystemBoot{
		Boot: BootConfig{
			BootSourceOverrideTarget:  "Hdd",
			BootSourceOverrideEnabled: "Once",
		},
	}

	resp, err := c.makeRequest(ctx, "PATCH", "/redfish/v1/Systems/System.Embedded.1", bootConfig)
	if err != nil {
		c.logger.LogError("Failed to set boot device to HDD: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to set boot device to HDD, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("Boot device set to HDD successfully")
	return nil
}

// EjectVirtualMedia ejects virtual media
func (c *Client) EjectVirtualMedia(ctx context.Context) error {
	c.logger.LogInfo("Ejecting virtual media...")

	resp, err := c.makeRequest(ctx, "POST", "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia", map[string]interface{}{})
	if err != nil {
		c.logger.LogError("Failed to eject virtual media: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to eject virtual media, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("Virtual media ejected successfully")
	time.Sleep(5 * time.Second)
	return nil
}

// InsertVirtualMedia inserts virtual media
func (c *Client) InsertVirtualMedia(ctx context.Context, isoURL string) error {
	c.logger.LogInfo("Inserting ISO image: %s", isoURL)

	mediaReq := VirtualMediaRequest{Image: isoURL}
	resp, err := c.makeRequest(ctx, "POST", "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia", mediaReq)
	if err != nil {
		c.logger.LogError("Failed to insert virtual media: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("failed to insert virtual media, status code: %d", resp.StatusCode)
	}

	c.logger.LogSuccess("Virtual media inserted successfully")
	time.Sleep(5 * time.Second)
	return nil
}

// WaitForSystemPowerOn waits for the system to power on
func (c *Client) WaitForSystemPowerOn(ctx context.Context) error {
	c.logger.LogInfo("Waiting for system to power on...")
	maxAttempts := 30
	attempt := 0

	for attempt < maxAttempts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		powerState, err := c.GetSystemPowerState(ctx)
		if err != nil {
			c.logger.LogWarn("Failed to get power state: %v", err)
		} else if powerState == "On" {
			c.logger.LogSuccess("System is now powered on")
			return nil
		}

		c.logger.LogInfo("System power state: %s, waiting... (attempt %d/%d)", powerState, attempt+1, maxAttempts)
		time.Sleep(10 * time.Second)
		attempt++
	}

	c.logger.LogError("System failed to power on within expected time")
	return fmt.Errorf("system failed to power on within expected time")
}

// WaitForSystemPowerOff waits for the system to power off
func (c *Client) WaitForSystemPowerOff(ctx context.Context) error {
	c.logger.LogInfo("Waiting for system to power off...")
	maxAttempts := 30
	attempt := 0

	for attempt < maxAttempts {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		powerState, err := c.GetSystemPowerState(ctx)
		if err != nil {
			c.logger.LogWarn("Failed to get power state: %v", err)
		} else if powerState == "Off" {
			c.logger.LogSuccess("System is now powered off")
			return nil
		}

		c.logger.LogInfo("System power state: %s, waiting... (attempt %d/%d)", powerState, attempt+1, maxAttempts)
		time.Sleep(10 * time.Second)
		attempt++
	}

	c.logger.LogError("System failed to power off within expected time")
	return fmt.Errorf("system failed to power off within expected time")
}
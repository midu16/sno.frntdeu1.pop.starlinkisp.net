package idrac

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	
)

// LifecycleControllerInfo represents iDRAC lifecycle controller information
type LifecycleControllerInfo struct {
	FirmwareVersion string `json:"FirmwareVersion"`
	Id              string `json:"Id"`
	Name            string `json:"Name"`
	Status          struct {
		Health string `json:"Health"`
		State  string `json:"State"`
	} `json:"Status"`
}

// GetLifecycleControllerInfo retrieves iDRAC lifecycle controller information
func (c *Client) GetLifecycleControllerInfo(ctx context.Context) (*LifecycleControllerInfo, error) {
	c.logger.LogInfo("Getting iDRAC lifecycle controller information...")

	resp, err := c.makeRequest(ctx, "GET", "/redfish/v1/Managers/iDRAC.Embedded.1", nil)
	if err != nil {
		c.logger.LogError("Failed to get lifecycle controller info: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get lifecycle controller info, status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var lcInfo LifecycleControllerInfo
	if err := json.Unmarshal(body, &lcInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal lifecycle controller info: %w", err)
	}

	c.logger.LogInfo("iDRAC Lifecycle Controller Information:")
	c.logger.LogInfo("  Firmware Version: %s", lcInfo.FirmwareVersion)
	c.logger.LogInfo("  ID: %s", lcInfo.Id)
	c.logger.LogInfo("  Name: %s", lcInfo.Name)
	c.logger.LogInfo("  Health: %s", lcInfo.Status.Health)
	c.logger.LogInfo("  State: %s", lcInfo.Status.State)

	return &lcInfo, nil
}

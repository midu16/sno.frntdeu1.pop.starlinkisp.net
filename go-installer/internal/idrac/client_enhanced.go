package idrac

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/logger"
)

// EnhancedClient extends the base Client with improved virtual media support
type EnhancedClient struct {
	*Client
}

// NewEnhancedClient creates a new enhanced iDRAC client
func NewEnhancedClient(cfg *config.IDRACConfig, log *logger.Logger) *EnhancedClient {
	return &EnhancedClient{
		Client: NewClient(cfg, log),
	}
}

// VirtualMediaInfo represents virtual media information
type VirtualMediaInfo struct {
	ConnectedVia string `json:"ConnectedVia"`
	Image        string `json:"Image"`
	ImageName    string `json:"ImageName"`
	Inserted     bool   `json:"Inserted"`
	MediaTypes   []string `json:"MediaTypes"`
}

// GetVirtualMediaInfo retrieves virtual media information
func (c *EnhancedClient) GetVirtualMediaInfo(ctx context.Context) (*VirtualMediaInfo, error) {
	c.logger.LogInfo("Getting virtual media information...")

	resp, err := c.makeRequest(ctx, "GET", "/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD", nil)
	if err != nil {
		c.logger.LogError("Failed to get virtual media info: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to get virtual media info, status code: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var mediaInfo VirtualMediaInfo
	if err := json.Unmarshal(body, &mediaInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal virtual media info: %w", err)
	}

	c.logger.LogInfo("Virtual Media Information:")
	c.logger.LogInfo("  Connected Via: %s", mediaInfo.ConnectedVia)
	c.logger.LogInfo("  Image: %s", mediaInfo.Image)
	c.logger.LogInfo("  Image Name: %s", mediaInfo.ImageName)
	c.logger.LogInfo("  Inserted: %t", mediaInfo.Inserted)
	c.logger.LogInfo("  Media Types: %v", mediaInfo.MediaTypes)

	return &mediaInfo, nil
}

// SetVirtualCDBootEnhanced sets the boot device to Virtual CD/DVD with enhanced compatibility
func (c *EnhancedClient) SetVirtualCDBootEnhanced(ctx context.Context) error {
	c.logger.LogInfo("Setting boot device to Virtual CD/DVD (Enhanced)...")

	// First, check if virtual media is inserted
	mediaInfo, err := c.GetVirtualMediaInfo(ctx)
	if err != nil {
		c.logger.LogWarn("Could not get virtual media info: %v", err)
	} else if !mediaInfo.Inserted {
		c.logger.LogWarn("No virtual media is currently inserted")
	}

	// Try different virtual CD boot options for iDRAC 8 compatibility
	// Priority order: RemoteCd (most common for iDRAC 8), VirtualCd, Cd
	virtualCDOptions := []string{"RemoteCd", "VirtualCd", "Cd"}
	
	for _, bootTarget := range virtualCDOptions {
		c.logger.LogInfo("Attempting to set boot device to %s...", bootTarget)
		
		bootConfig := SystemBoot{
			Boot: BootConfig{
				BootSourceOverrideTarget:  bootTarget,
				BootSourceOverrideEnabled: "Once",
			},
		}

		resp, err := c.makeRequest(ctx, "PATCH", "/redfish/v1/Systems/System.Embedded.1", bootConfig)
		if err != nil {
			c.logger.LogWarn("Failed to set boot device to %s: %v", bootTarget, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			c.logger.LogSuccess("Boot device set to Virtual CD/DVD (%s) successfully", bootTarget)
			return nil
		} else {
			c.logger.LogWarn("Failed to set boot device to %s, status code: %d", bootTarget, resp.StatusCode)
		}
	}

	return fmt.Errorf("failed to set boot device to Virtual CD/DVD with any supported option")
}

// ManageVirtualMediaBootProcess manages the complete virtual media boot process
func (c *EnhancedClient) ManageVirtualMediaBootProcess(ctx context.Context, isoURL string) error {
	c.logger.LogInfo("Starting enhanced virtual media boot management process...")

	// Step 1: Eject any existing virtual media
	c.logger.LogInfo("Step 1: Ejecting existing virtual media...")
	if err := c.EjectVirtualMedia(ctx); err != nil {
		c.logger.LogWarn("Failed to eject existing virtual media: %v", err)
	}
	time.Sleep(10 * time.Second)

	// Step 2: Insert the new ISO
	c.logger.LogInfo("Step 2: Inserting new virtual media...")
	if err := c.InsertVirtualMedia(ctx, isoURL); err != nil {
		return fmt.Errorf("failed to insert virtual media: %w", err)
	}
	time.Sleep(10 * time.Second)

	// Step 3: Set boot device to virtual CD/DVD
	c.logger.LogInfo("Step 3: Setting boot device to virtual CD/DVD...")
	if err := c.SetVirtualCDBootEnhanced(ctx); err != nil {
		return fmt.Errorf("failed to set boot device to virtual CD/DVD: %w", err)
	}

	// Step 4: Restart the system
	c.logger.LogInfo("Step 4: Restarting system...")
	if err := c.RestartSystem(ctx); err != nil {
		return fmt.Errorf("failed to restart system: %w", err)
	}

	c.logger.LogSuccess("Enhanced virtual media boot management process completed successfully")
	return nil
}

// GetLifecycleControllerInfo retrieves iDRAC lifecycle controller information
func (c *EnhancedClient) GetLifecycleControllerInfo(ctx context.Context) (*LifecycleControllerInfo, error) {
	return c.Client.GetLifecycleControllerInfo(ctx)
}

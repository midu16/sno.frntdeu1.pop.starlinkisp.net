package app

import (
	"context"
	"fmt"
	"os"

	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/idrac"
	"openshift-sno-hub-installer/internal/logger"
	"openshift-sno-hub-installer/internal/openshift"
	"openshift-sno-hub-installer/internal/ssh"
)

// EnhancedApp represents the enhanced application with virtual media support
type EnhancedApp struct {
	config     *config.Config
	logger     *logger.Logger
	idrac      *idrac.EnhancedClient
	installer  *openshift.Installer
	sshManager *ssh.Manager
}

// NewEnhancedApp creates a new enhanced application instance
func NewEnhancedApp(cfg *config.Config, log *logger.Logger) *EnhancedApp {
	return &EnhancedApp{
		config:     cfg,
		logger:     log,
		idrac:      idrac.NewEnhancedClient(&cfg.IDRAC, log),
		installer:  openshift.NewInstaller(cfg, log),
		sshManager: ssh.NewManager(cfg, log),
	}
}

// Run runs the enhanced application with the specified command
func (a *EnhancedApp) Run(ctx context.Context) error {
	// Get command from command line arguments
	if len(os.Args) < 2 {
		return a.runInstall(ctx)
	}

	command := os.Args[1]
	switch command {
	case "config":
		return a.createConfig()
	case "power-on":
		return a.powerOn(ctx)
	case "power-off":
		return a.powerOff(ctx)
	case "status":
		return a.getStatus(ctx)
	case "info":
		return a.getSystemInfo(ctx)
	case "eject-media":
		return a.ejectMedia(ctx)
	case "insert-media":
		if len(os.Args) < 3 {
			return fmt.Errorf("please provide ISO URL as second argument")
		}
		return a.insertMedia(ctx, os.Args[2])
	case "set-boot-cd":
		return a.setBootCD(ctx)
	case "set-boot-cd-enhanced":
		return a.setVirtualCDBootEnhanced(ctx)
	case "virtual-media-info":
		return a.getVirtualMediaInfo(ctx)
	case "lifecycle-controller":
		return a.getLifecycleControllerInfo(ctx)
	case "manage-virtual-boot":
		if len(os.Args) < 3 {
			return fmt.Errorf("please provide ISO URL as second argument")
		}
		return a.manageVirtualMediaBootProcess(ctx, os.Args[2])
	case "set-boot-hdd":
		return a.setBootHDD(ctx)
	case "restart":
		return a.restart(ctx)
	case "cleanup":
		powerOff := len(os.Args) > 2 && os.Args[2] == "poweroff"
		return a.cleanup(ctx, powerOff)
	case "install":
		return a.runInstall(ctx)
	case "help":
		return a.showUsage()
	default:
		return a.showUsage()
	}
}

// createConfig creates a default configuration file
func (a *EnhancedApp) createConfig() error {
	a.logger.LogInfo("Creating configuration file...")
	
	// Create a default config without validation
	defaultConfig := config.DefaultConfig()
	configFile := "idrac_config.yaml"
	if err := defaultConfig.Save(configFile); err != nil {
		return fmt.Errorf("failed to save config file: %w", err)
	}
	
	a.logger.LogSuccess("Configuration file created at %s", configFile)
	a.logger.LogWarn("Please edit the configuration file with your settings before running the script")
	return nil
}

// powerOn powers on the system
func (a *EnhancedApp) powerOn(ctx context.Context) error {
	a.logger.LogInfo("Powering on system...")
	return a.idrac.PowerOnSystem(ctx)
}

// powerOff powers off the system
func (a *EnhancedApp) powerOff(ctx context.Context) error {
	a.logger.LogInfo("Powering off system...")
	return a.idrac.PowerOffSystem(ctx)
}

// getStatus gets system status
func (a *EnhancedApp) getStatus(ctx context.Context) error {
	a.logger.LogInfo("Getting system status...")
	
	powerState, err := a.idrac.GetSystemPowerState(ctx)
	if err != nil {
		return fmt.Errorf("failed to get power state: %w", err)
	}
	
	health, err := a.idrac.GetSystemHealth(ctx)
	if err != nil {
		return fmt.Errorf("failed to get system health: %w", err)
	}
	
	a.logger.LogInfo("System Status:")
	a.logger.LogInfo("  Power State: %s", powerState)
	a.logger.LogInfo("  Health: %s", health)
	
	return nil
}

// getSystemInfo gets system information including lifecycle controller
func (a *EnhancedApp) getSystemInfo(ctx context.Context) error {
	a.logger.LogInfo("Getting system information...")
	
	// Get system information
	_, err := a.idrac.GetSystemInfo(ctx)
	if err != nil {
		a.logger.LogWarn("Failed to get system info: %v", err)
	}
	
	// Get lifecycle controller information
	a.logger.LogInfo("Getting iDRAC lifecycle controller information...")
	_, err = a.idrac.GetLifecycleControllerInfo(ctx)
	if err != nil {
		a.logger.LogWarn("Failed to get lifecycle controller info: %v", err)
	}
	
	return nil
}

// ejectMedia ejects virtual media
func (a *EnhancedApp) ejectMedia(ctx context.Context) error {
	a.logger.LogInfo("Ejecting virtual media...")
	return a.idrac.EjectVirtualMedia(ctx)
}

// insertMedia inserts virtual media
func (a *EnhancedApp) insertMedia(ctx context.Context, isoURL string) error {
	a.logger.LogInfo("Inserting virtual media: %s", isoURL)
	return a.idrac.InsertVirtualMedia(ctx, isoURL)
}

// setBootCD sets boot device to CD
func (a *EnhancedApp) setBootCD(ctx context.Context) error {
	a.logger.LogInfo("Setting boot device to CD...")
	return a.idrac.SetVirtualCDBoot(ctx)
}

// setVirtualCDBootEnhanced sets boot device to virtual CD/DVD with enhanced compatibility
func (a *EnhancedApp) setVirtualCDBootEnhanced(ctx context.Context) error {
	a.logger.LogInfo("Setting boot device to Virtual CD/DVD (Enhanced)...")
	return a.idrac.SetVirtualCDBootEnhanced(ctx)
}

// getVirtualMediaInfo gets virtual media information
func (a *EnhancedApp) getVirtualMediaInfo(ctx context.Context) error {
	a.logger.LogInfo("Getting virtual media information...")
	_, err := a.idrac.GetVirtualMediaInfo(ctx)
	return err
}

// getLifecycleControllerInfo gets iDRAC lifecycle controller information
func (a *EnhancedApp) getLifecycleControllerInfo(ctx context.Context) error {
	a.logger.LogInfo("Getting iDRAC lifecycle controller information...")
	_, err := a.idrac.GetLifecycleControllerInfo(ctx)
	return err
}

// setBootHDD sets boot device to HDD
func (a *EnhancedApp) setBootHDD(ctx context.Context) error {
	a.logger.LogInfo("Setting boot device to HDD...")
	return a.idrac.SetHDDBoot(ctx)
}

// restart restarts the system
func (a *EnhancedApp) restart(ctx context.Context) error {
	a.logger.LogInfo("Restarting system...")
	return a.idrac.RestartSystem(ctx)
}

// cleanup performs cleanup operations
func (a *EnhancedApp) cleanup(ctx context.Context, powerOff bool) error {
	a.logger.LogInfo("Performing cleanup...")
	
	// Eject virtual media
	if err := a.idrac.EjectVirtualMedia(ctx); err != nil {
		a.logger.LogWarn("Failed to eject virtual media: %v", err)
	}
	
	// Set boot back to HDD
	if err := a.idrac.SetHDDBoot(ctx); err != nil {
		a.logger.LogWarn("Failed to set boot to HDD: %v", err)
	}
	
	// Power off if requested
	if powerOff {
		if err := a.idrac.PowerOffSystem(ctx); err != nil {
			a.logger.LogWarn("Failed to power off system: %v", err)
		}
	}
	
	a.logger.LogSuccess("Cleanup completed")
	return nil
}

// manageVirtualMediaBootProcess manages the complete virtual media boot process
func (a *EnhancedApp) manageVirtualMediaBootProcess(ctx context.Context, isoURL string) error {
	a.logger.LogInfo("Managing virtual media boot process...")
	return a.idrac.ManageVirtualMediaBootProcess(ctx, isoURL)
}

// runInstall runs the full installation process
func (a *EnhancedApp) runInstall(ctx context.Context) error {
	a.logger.LogInfo("Starting OpenShift SNO Hub Installation with Enhanced iDRAC8 Management")
	
	// Check iDRAC connectivity
	if err := a.idrac.CheckConnectivity(ctx); err != nil {
		return fmt.Errorf("iDRAC connectivity check failed: %w", err)
	}
	
	// Get system information
	if _, err := a.idrac.GetSystemInfo(ctx); err != nil {
		a.logger.LogWarn("Failed to get system info: %v", err)
	}
	
	// Get system health
	if _, err := a.idrac.GetSystemHealth(ctx); err != nil {
		a.logger.LogWarn("Failed to get system health: %v", err)
	}
	
	// Check and setup SSH key
	if err := a.sshManager.CheckSSHKey(ctx); err != nil {
		return fmt.Errorf("failed to check SSH key: %w", err)
	}
	
	if err := a.sshManager.SetupSSHKey(ctx); err != nil {
		return fmt.Errorf("failed to setup SSH key: %w", err)
	}
	
	// Extract OpenShift installer
	if err := a.installer.ExtractInstaller(ctx); err != nil {
		return fmt.Errorf("failed to extract installer: %w", err)
	}
	
	// Prepare work directory
	if err := a.installer.PrepareWorkDir(ctx); err != nil {
		return fmt.Errorf("failed to prepare work directory: %w", err)
	}
	
	// Create agent image
	if err := a.installer.CreateAgentImage(ctx); err != nil {
		return fmt.Errorf("failed to create agent image: %w", err)
	}
	
	// Copy ISO to remote host
	isoPath := a.installer.GetISOFilePath()
	if err := a.sshManager.CopyISOToRemote(ctx, isoPath); err != nil {
		return fmt.Errorf("failed to copy ISO to remote: %w", err)
	}
	
	// Manage virtual media boot process
	if err := a.manageVirtualMediaBootProcess(ctx, a.config.Remote.ISOURL); err != nil {
		return fmt.Errorf("failed to manage virtual media boot process: %w", err)
	}
	
	// Monitor installation
	if err := a.monitorInstallation(ctx); err != nil {
		return fmt.Errorf("failed to monitor installation: %w", err)
	}
	
	// Cleanup
	if err := a.cleanup(ctx, false); err != nil {
		a.logger.LogWarn("Cleanup failed: %v", err)
	}
	
	a.logger.LogSuccess("OpenShift SNO Hub installation completed successfully!")
	return nil
}

// monitorInstallation monitors the installation progress
func (a *EnhancedApp) monitorInstallation(ctx context.Context) error {
	a.logger.LogInfo("Monitoring installation progress...")
	
	// Check power status
	powerState, err := a.idrac.GetSystemPowerState(ctx)
	if err != nil {
		a.logger.LogWarn("Failed to get power state: %v", err)
		return nil
	}
	
	if powerState == "On" {
		a.logger.LogSuccess("Server is powered ON. Running wait-for install-complete...")
		return a.installer.WaitForInstallComplete(ctx)
	} else {
		a.logger.LogWarn("Server power state is: %s", powerState)
		a.logger.LogWarn("Skipping wait-for install-complete.")
	}
	
	return nil
}

// showUsage shows the usage information
func (a *EnhancedApp) showUsage() error {
	fmt.Println("Usage: openshift-sno-hub-installer [command]")
	fmt.Println("")
	fmt.Println("Commands:")
	fmt.Println("  config         - Create configuration file")
	fmt.Println("  power-on       - Power on the system via iDRAC")
	fmt.Println("  power-off      - Power off the system via iDRAC")
	fmt.Println("  status         - Get system power and health status")
	fmt.Println("  info           - Get system information")
	fmt.Println("  eject-media    - Eject virtual media")
	fmt.Println("  insert-media   - Insert virtual media (requires ISO URL)")
	fmt.Println("  set-boot-cd    - Set boot device to Virtual CD/DVD")
	fmt.Println("  set-boot-cd-enhanced - Set boot device to Virtual CD/DVD (Enhanced)")
	fmt.Println("  virtual-media-info - Get virtual media information")
	fmt.Println("  lifecycle-controller - Get iDRAC lifecycle controller information")
	fmt.Println("  manage-virtual-boot - Manage complete virtual media boot process (requires ISO URL)")
	fmt.Println("  set-boot-hdd   - Set boot device to HDD")
	fmt.Println("  restart        - Restart the system")
	fmt.Println("  cleanup        - Perform cleanup (optionally power off)")
	fmt.Println("  install        - Run full OpenShift SNO hub installation (default)")
	return nil
}

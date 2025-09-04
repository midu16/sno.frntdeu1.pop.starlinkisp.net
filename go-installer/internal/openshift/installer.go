package openshift

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/logger"
)

// Installer handles OpenShift installation operations
type Installer struct {
	config *config.Config
	logger *logger.Logger
}

// NewInstaller creates a new OpenShift installer
func NewInstaller(cfg *config.Config, log *logger.Logger) *Installer {
	return &Installer{
		config: cfg,
		logger: log,
	}
}

// ExtractInstaller extracts the OpenShift installer from the release
func (i *Installer) ExtractInstaller(ctx context.Context) error {
	i.logger.LogInfo("Extracting OpenShift installer...")

	// Check if registry auth file exists
	if _, err := os.Stat(i.config.OpenShift.RegistryAuthFile); os.IsNotExist(err) {
		return fmt.Errorf("registry auth file not found: %s", i.config.OpenShift.RegistryAuthFile)
	}

	// Get release digest
	releaseDigest, err := i.getReleaseDigest(ctx)
	if err != nil {
		return fmt.Errorf("failed to get release digest: %w", err)
	}

	i.logger.LogInfo("Release digest: %s", releaseDigest)

	// Extract openshift-install command
	cmd := exec.CommandContext(ctx, "oc", "adm", "release", "extract",
		"-a", i.config.OpenShift.RegistryAuthFile,
		"--command=openshift-install",
		releaseDigest)

	i.logger.LogInfo("Running: %s", strings.Join(cmd.Args, " "))
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		i.logger.LogError("Failed to extract installer: %s", string(output))
		return fmt.Errorf("failed to extract installer: %w", err)
	}

	i.logger.LogSuccess("OpenShift installer extracted successfully")
	return nil
}

// getReleaseDigest gets the release digest for the specified version
func (i *Installer) getReleaseDigest(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "oc", "adm", "release", "info",
		"quay.io/openshift-release-dev/ocp-release:"+i.config.OpenShift.Version+"-x86_64",
		"--registry-config", i.config.OpenShift.RegistryAuthFile)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("failed to get release info: %w", err)
	}

	// Parse output to find Pull From line
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.Contains(line, "Pull From:") {
			parts := strings.Fields(line)
			if len(parts) >= 3 {
				return parts[2], nil
			}
		}
	}

	return "", fmt.Errorf("could not find release digest in output")
}

// PrepareWorkDir prepares the working directory for installation
func (i *Installer) PrepareWorkDir(ctx context.Context) error {
	i.logger.LogInfo("Preparing work directory...")

	// Clean existing workdir if it exists
	if err := i.cleanWorkDir(); err != nil {
		return fmt.Errorf("failed to clean work directory: %w", err)
	}

	// Create workdir
	if err := os.MkdirAll(i.config.Paths.WorkDir, 0755); err != nil {
		return fmt.Errorf("failed to create work directory: %w", err)
	}

	// Copy openshift directory
	if err := i.copyOpenshiftDir(); err != nil {
		return fmt.Errorf("failed to copy openshift directory: %w", err)
	}

	// Copy configuration files
	if err := i.copyConfigFiles(); err != nil {
		return fmt.Errorf("failed to copy configuration files: %w", err)
	}

	i.logger.LogSuccess("Work directory prepared successfully")
	return nil
}

// cleanWorkDir cleans the existing work directory
func (i *Installer) cleanWorkDir() error {
	if _, err := os.Stat(i.config.Paths.WorkDir); os.IsNotExist(err) {
		return nil // Directory doesn't exist, nothing to clean
	}

	i.logger.LogInfo("Cleaning existing work directory...")
	return os.RemoveAll(i.config.Paths.WorkDir)
}

// copyOpenshiftDir copies the openshift directory from source
func (i *Installer) copyOpenshiftDir() error {
	sourceOpenshiftDir := filepath.Join(i.config.Paths.SourceDir, "openshift")
	destOpenshiftDir := filepath.Join(i.config.Paths.WorkDir, "openshift")

	if _, err := os.Stat(sourceOpenshiftDir); os.IsNotExist(err) {
		return fmt.Errorf("source openshift directory not found: %s", sourceOpenshiftDir)
	}

	i.logger.LogInfo("Copying %s -> %s", sourceOpenshiftDir, destOpenshiftDir)
	
	cmd := exec.Command("cp", "-r", sourceOpenshiftDir, i.config.Paths.WorkDir)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to copy openshift directory: %s", string(output))
	}

	return nil
}

// copyConfigFiles copies configuration files from source
func (i *Installer) copyConfigFiles() error {
	configFiles := []string{"agent-config.yaml", "install-config.yaml"}

	for _, filename := range configFiles {
		sourceFile := filepath.Join(i.config.Paths.SourceDir, filename)
		destFile := filepath.Join(i.config.Paths.WorkDir, filename)

		if _, err := os.Stat(sourceFile); os.IsNotExist(err) {
			return fmt.Errorf("configuration file not found: %s", sourceFile)
		}

		i.logger.LogInfo("Copying %s -> %s", sourceFile, destFile)
		
		cmd := exec.Command("cp", sourceFile, destFile)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to copy %s: %s", filename, string(output))
		}
	}

	return nil
}

// CreateAgentImage creates the agent image
func (i *Installer) CreateAgentImage(ctx context.Context) error {
	i.logger.LogInfo("Creating agent image...")

	// Check if installer exists and is executable
	if _, err := os.Stat(i.config.Paths.InstallerPath); os.IsNotExist(err) {
		return fmt.Errorf("openshift-install not found: %s", i.config.Paths.InstallerPath)
	}

	// Make installer executable
	if err := os.Chmod(i.config.Paths.InstallerPath, 0755); err != nil {
		return fmt.Errorf("failed to make installer executable: %w", err)
	}

	// Run openshift-install agent create image
	cmd := exec.CommandContext(ctx, i.config.Paths.InstallerPath,
		"agent", "create", "image",
		"--dir", i.config.Paths.WorkDir,
		"--log-level", "debug")

	i.logger.LogInfo("Running: %s", strings.Join(cmd.Args, " "))
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		i.logger.LogError("Failed to create agent image: %s", string(output))
		return fmt.Errorf("failed to create agent image: %w", err)
	}

	i.logger.LogSuccess("Agent image created successfully")
	return nil
}

// WaitForInstallComplete waits for the installation to complete
func (i *Installer) WaitForInstallComplete(ctx context.Context) error {
	i.logger.LogInfo("Waiting for installation to complete...")

	// Set KUBECONFIG environment variable
	kubeconfigPath := filepath.Join(i.config.Paths.WorkDir, "auth", "kubeconfig")
	if err := os.Setenv("KUBECONFIG", kubeconfigPath); err != nil {
		return fmt.Errorf("failed to set KUBECONFIG: %w", err)
	}

	// Run openshift-install agent wait-for install-complete
	cmd := exec.CommandContext(ctx, i.config.Paths.InstallerPath,
		"agent", "wait-for", "install-complete",
		"--dir", i.config.Paths.WorkDir)

	i.logger.LogInfo("Running: %s", strings.Join(cmd.Args, " "))
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		i.logger.LogError("Installation wait failed: %s", string(output))
		return fmt.Errorf("installation wait failed: %w", err)
	}

	i.logger.LogSuccess("Installation completed successfully")
	return nil
}

// GetISOFilePath returns the path to the generated ISO file
func (i *Installer) GetISOFilePath() string {
	return i.config.GetISOFilePath()
}

// CheckISOExists checks if the ISO file exists
func (i *Installer) CheckISOExists() bool {
	isoPath := i.GetISOFilePath()
	_, err := os.Stat(isoPath)
	return err == nil
}
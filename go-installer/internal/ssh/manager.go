package ssh

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

// Manager handles SSH operations
type Manager struct {
	config *config.Config
	logger *logger.Logger
}

// NewManager creates a new SSH manager
func NewManager(cfg *config.Config, log *logger.Logger) *Manager {
	return &Manager{
		config: cfg,
		logger: log,
	}
}

// CheckSSHKey checks if SSH key exists, generates one if not
func (m *Manager) CheckSSHKey(ctx context.Context) error {
	m.logger.LogInfo("Checking SSH key...")

	sshKeyPath := m.config.GetSSHKeyPrivatePath()
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		m.logger.LogInfo("SSH key not found. Generating a new ed25519 key...")
		return m.generateSSHKey(ctx)
	}

	m.logger.LogInfo("SSH key found: %s", sshKeyPath)
	return nil
}

// generateSSHKey generates a new SSH key
func (m *Manager) generateSSHKey(ctx context.Context) error {
	sshKeyPath := m.config.GetSSHKeyPrivatePath()
	
	m.logger.LogInfo("Generating SSH key at %s...", sshKeyPath)

	cmd := exec.CommandContext(ctx, "ssh-keygen",
		"-t", "ed25519",
		"-f", sshKeyPath,
		"-N", "",
		"-q")

	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.LogError("Failed to generate SSH key: %s", string(output))
		return fmt.Errorf("failed to generate SSH key: %w", err)
	}

	m.logger.LogSuccess("SSH key generated successfully")
	return nil
}

// SetupSSHKey copies SSH key to remote host
func (m *Manager) SetupSSHKey(ctx context.Context) error {
	m.logger.LogInfo("Setting up SSH key on remote host...")

	sshKeyPath := m.config.Paths.SSHKeyPath
	if _, err := os.Stat(sshKeyPath); os.IsNotExist(err) {
		return fmt.Errorf("SSH public key not found: %s", sshKeyPath)
	}

	// Use sshpass to copy the key
	cmd := exec.CommandContext(ctx, "sshpass",
		"-p", m.config.IDRAC.Password,
		"ssh-copy-id",
		"-i", sshKeyPath,
		"-o", "StrictHostKeyChecking=no",
		fmt.Sprintf("%s@%s", m.config.Remote.User, m.config.Remote.Host))

	m.logger.LogInfo("Copying SSH key to %s@%s...", m.config.Remote.User, m.config.Remote.Host)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.LogError("Failed to copy SSH key: %s", string(output))
		return fmt.Errorf("failed to copy SSH key: %w", err)
	}

	m.logger.LogSuccess("SSH key copied successfully")
	return nil
}

// CopyFileToRemote copies a file to the remote host
func (m *Manager) CopyFileToRemote(ctx context.Context, localPath, remotePath string) error {
	m.logger.LogInfo("Copying file to remote host...")

	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		return fmt.Errorf("local file not found: %s", localPath)
	}

	// Use scp to copy the file
	cmd := exec.CommandContext(ctx, "scp",
		"-r",
		localPath,
		fmt.Sprintf("%s@%s:%s", m.config.Remote.User, m.config.Remote.Host, remotePath))

	m.logger.LogInfo("Copying %s to %s@%s:%s", localPath, m.config.Remote.User, m.config.Remote.Host, remotePath)
	
	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.LogError("Failed to copy file: %s", string(output))
		return fmt.Errorf("failed to copy file: %w", err)
	}

	m.logger.LogSuccess("File copied successfully")
	return nil
}

// CopyISOToRemote copies the ISO file to the remote host
func (m *Manager) CopyISOToRemote(ctx context.Context, isoPath string) error {
	m.logger.LogInfo("Copying ISO to remote host...")

	if _, err := os.Stat(isoPath); os.IsNotExist(err) {
		m.logger.LogWarn("ISO file not found: %s", isoPath)
		return fmt.Errorf("ISO file not found: %s", isoPath)
	}

	remotePath := filepath.Join(m.config.Remote.Path, filepath.Base(isoPath))
	return m.CopyFileToRemote(ctx, isoPath, remotePath)
}

// ExecuteRemoteCommand executes a command on the remote host
func (m *Manager) ExecuteRemoteCommand(ctx context.Context, command string) error {
	m.logger.LogInfo("Executing remote command: %s", command)

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		fmt.Sprintf("%s@%s", m.config.Remote.User, m.config.Remote.Host),
		command)

	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.LogError("Remote command failed: %s", string(output))
		return fmt.Errorf("remote command failed: %w", err)
	}

	m.logger.LogInfo("Remote command output: %s", string(output))
	return nil
}

// TestSSHConnection tests SSH connection to remote host
func (m *Manager) TestSSHConnection(ctx context.Context) error {
	m.logger.LogInfo("Testing SSH connection to remote host...")

	cmd := exec.CommandContext(ctx, "ssh",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=10",
		"-o", "BatchMode=yes",
		fmt.Sprintf("%s@%s", m.config.Remote.User, m.config.Remote.Host),
		"echo 'SSH connection successful'")

	output, err := cmd.CombinedOutput()
	if err != nil {
		m.logger.LogError("SSH connection test failed: %s", string(output))
		return fmt.Errorf("SSH connection test failed: %w", err)
	}

	m.logger.LogSuccess("SSH connection test successful: %s", strings.TrimSpace(string(output)))
	return nil
}

// GetSSHKeyPath returns the path to the SSH public key
func (m *Manager) GetSSHKeyPath() string {
	return m.config.Paths.SSHKeyPath
}

// GetSSHKeyContent returns the content of the SSH public key
func (m *Manager) GetSSHKeyContent() (string, error) {
	sshKeyPath := m.GetSSHKeyPath()
	content, err := os.ReadFile(sshKeyPath)
	if err != nil {
		return "", fmt.Errorf("failed to read SSH key: %w", err)
	}
	return strings.TrimSpace(string(content)), nil
}
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the application
type Config struct {
	IDRAC     IDRACConfig     `yaml:"idrac"`
	OpenShift OpenShiftConfig `yaml:"openshift"`
	Remote    RemoteConfig    `yaml:"remote"`
	Paths     PathsConfig     `yaml:"paths"`
}

// IDRACConfig holds iDRAC-specific configuration
type IDRACConfig struct {
	IP         string `yaml:"ip"`
	Username   string `yaml:"username"`
	Password   string `yaml:"password"`
	VerifySSL  bool   `yaml:"verify_ssl"`
	Timeout    int    `yaml:"timeout"`
}

// OpenShiftConfig holds OpenShift-specific configuration
type OpenShiftConfig struct {
	Version     string `yaml:"version"`
	ClusterName string `yaml:"cluster_name"`
	RegistryAuthFile string `yaml:"registry_auth_file"`
}

// RemoteConfig holds remote host configuration
type RemoteConfig struct {
	User     string `yaml:"user"`
	Host     string `yaml:"host"`
	Path     string `yaml:"path"`
	ISOURL   string `yaml:"iso_url"`
}

// PathsConfig holds file and directory paths
type PathsConfig struct {
	WorkDir     string `yaml:"workdir"`
	SourceDir   string `yaml:"source_dir"`
	SSHKeyPath  string `yaml:"ssh_key_path"`
	InstallerPath string `yaml:"installer_path"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		IDRAC: IDRACConfig{
			IP:        "192.168.1.228",
			Username:  "root",
			VerifySSL: false,
			Timeout:   30,
		},
		OpenShift: OpenShiftConfig{
			Version:     "4.16.45",
			ClusterName: "sno-hub",
			RegistryAuthFile: "./config.json",
		},
		Remote: RemoteConfig{
			User:   "rock",
			Host:   "192.168.1.21",
			Path:   "/apps/webcache/OSs/",
			ISOURL: "http://192.168.1.21:8080/OSs/agent.x86_64.iso",
		},
		Paths: PathsConfig{
			WorkDir:       "./workdir",
			SourceDir:     "./abi-master-0",
			SSHKeyPath:    os.Getenv("HOME") + "/.ssh/id_ed25519.pub",
			InstallerPath: "./openshift-install",
		},
	}
}

// LoadConfig loads configuration from file or creates default
func LoadConfig() (*Config, error) {
	configFile := "idrac_config.yaml"
	
	// Check if config file exists
	if _, err := os.Stat(configFile); os.IsNotExist(err) {
		// Create default config file
		if err := createDefaultConfigFile(configFile); err != nil {
			return nil, fmt.Errorf("failed to create default config file: %w", err)
		}
	}

	// Read config file
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// Unmarshal into struct
	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("configuration validation failed: %w", err)
	}

	return &config, nil
}


// createDefaultConfigFile creates a default configuration file
func createDefaultConfigFile(filename string) error {
	config := DefaultConfig()
	
	// Set ISO URL based on remote host
	config.Remote.ISOURL = fmt.Sprintf("http://%s:8080/OSs/agent.x86_64.iso", config.Remote.Host)

	data, err := yaml.Marshal(config)
	if err != nil {
		return fmt.Errorf("failed to marshal default config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Validate validates the configuration
func (c *Config) Validate() error {
	if c.IDRAC.IP == "" {
		return fmt.Errorf("idrac.ip is required")
	}
	if c.IDRAC.Username == "" {
		return fmt.Errorf("idrac.username is required")
	}
	if c.IDRAC.Password == "" {
		return fmt.Errorf("idrac.password is required")
	}
	if c.OpenShift.Version == "" {
		return fmt.Errorf("openshift.version is required")
	}
	if c.OpenShift.ClusterName == "" {
		return fmt.Errorf("openshift.cluster_name is required")
	}
	if c.Remote.Host == "" {
		return fmt.Errorf("remote.host is required")
	}
	if c.Remote.User == "" {
		return fmt.Errorf("remote.user is required")
	}
	return nil
}

// GetISOFilePath returns the full path to the ISO file
func (c *Config) GetISOFilePath() string {
	return filepath.Join(c.Paths.WorkDir, "agent.x86_64.iso")
}

// GetSSHKeyPrivatePath returns the path to the private SSH key
func (c *Config) GetSSHKeyPrivatePath() string {
	return c.Paths.SSHKeyPath[:len(c.Paths.SSHKeyPath)-4] // Remove .pub extension
}

// Save saves the configuration to file
func (c *Config) Save(filename string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}
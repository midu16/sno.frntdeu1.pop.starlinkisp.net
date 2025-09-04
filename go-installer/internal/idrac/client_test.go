package idrac

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"openshift-sno-hub-installer/internal/config"
	"openshift-sno-hub-installer/internal/logger"
)

// Mock iDRAC server for testing
func createMockIDRACServer() *httptest.Server {
	mux := http.NewServeMux()

	// Mock system info endpoint
	mux.HandleFunc("/redfish/v1/Systems/System.Embedded.1", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			systemInfo := map[string]interface{}{
				"Manufacturer": "Dell Inc.",
				"Model":        "PowerEdge R640",
				"SerialNumber": "ABC123456",
				"BiosVersion":  "2.15.0",
				"PowerState":   "On",
				"Status": map[string]interface{}{
					"Health": "OK",
				},
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(systemInfo)
		case "PATCH":
			// Mock boot configuration
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Mock system reset endpoint
	mux.HandleFunc("/redfish/v1/Systems/System.Embedded.1/Actions/ComputerSystem.Reset", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	// Mock virtual media endpoints
	mux.HandleFunc("/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.EjectMedia", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	mux.HandleFunc("/redfish/v1/Managers/iDRAC.Embedded.1/VirtualMedia/CD/Actions/VirtualMedia.InsertMedia", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}

func TestIDRACClient(t *testing.T) {
	// Create mock server
	server := createMockIDRACServer()
	defer server.Close()

	// Create test configuration
	cfg := &config.IDRACConfig{
		IP:        "localhost",
		Username:  "root",
		Password:  "password",
		VerifySSL: false,
		Timeout:   30,
	}

	// Create logger
	log := logger.NewLogger()
	defer log.Close()

	// Create client with mock server URL
	client := &Client{
		config:     cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     log,
		baseURL:    server.URL,
	}

	ctx := context.Background()

	t.Run("CheckConnectivity", func(t *testing.T) {
		err := client.CheckConnectivity(ctx)
		if err != nil {
			t.Errorf("CheckConnectivity failed: %v", err)
		}
	})

	t.Run("GetSystemInfo", func(t *testing.T) {
		info, err := client.GetSystemInfo(ctx)
		if err != nil {
			t.Errorf("GetSystemInfo failed: %v", err)
		}
		if info.Manufacturer != "Dell Inc." {
			t.Errorf("Expected manufacturer 'Dell Inc.', got '%s'", info.Manufacturer)
		}
		if info.Model != "PowerEdge R640" {
			t.Errorf("Expected model 'PowerEdge R640', got '%s'", info.Model)
		}
	})

	t.Run("GetSystemPowerState", func(t *testing.T) {
		state, err := client.GetSystemPowerState(ctx)
		if err != nil {
			t.Errorf("GetSystemPowerState failed: %v", err)
		}
		if state != "On" {
			t.Errorf("Expected power state 'On', got '%s'", state)
		}
	})

	t.Run("GetSystemHealth", func(t *testing.T) {
		health, err := client.GetSystemHealth(ctx)
		if err != nil {
			t.Errorf("GetSystemHealth failed: %v", err)
		}
		if health != "OK" {
			t.Errorf("Expected health 'OK', got '%s'", health)
		}
	})

	t.Run("SetVirtualCDBoot", func(t *testing.T) {
		err := client.SetVirtualCDBoot(ctx)
		if err != nil {
			t.Errorf("SetVirtualCDBoot failed: %v", err)
		}
	})

	t.Run("SetHDDBoot", func(t *testing.T) {
		err := client.SetHDDBoot(ctx)
		if err != nil {
			t.Errorf("SetHDDBoot failed: %v", err)
		}
	})

	t.Run("EjectVirtualMedia", func(t *testing.T) {
		err := client.EjectVirtualMedia(ctx)
		if err != nil {
			t.Errorf("EjectVirtualMedia failed: %v", err)
		}
	})

	t.Run("InsertVirtualMedia", func(t *testing.T) {
		err := client.InsertVirtualMedia(ctx, "http://example.com/test.iso")
		if err != nil {
			t.Errorf("InsertVirtualMedia failed: %v", err)
		}
	})

	t.Run("PowerOnSystem", func(t *testing.T) {
		err := client.PowerOnSystem(ctx)
		if err != nil {
			t.Errorf("PowerOnSystem failed: %v", err)
		}
	})

	t.Run("PowerOffSystem", func(t *testing.T) {
		err := client.PowerOffSystem(ctx)
		if err != nil {
			t.Errorf("PowerOffSystem failed: %v", err)
		}
	})

	t.Run("RestartSystem", func(t *testing.T) {
		err := client.RestartSystem(ctx)
		if err != nil {
			t.Errorf("RestartSystem failed: %v", err)
		}
	})
}

func TestIDRACClientErrorHandling(t *testing.T) {
	// Create client with invalid configuration
	cfg := &config.IDRACConfig{
		IP:        "invalid-host",
		Username:  "root",
		Password:  "password",
		VerifySSL: false,
		Timeout:   1, // Very short timeout
	}

	log := logger.NewLogger()
	defer log.Close()

	client := NewClient(cfg, log)
	ctx := context.Background()

	t.Run("ConnectivityFailure", func(t *testing.T) {
		err := client.CheckConnectivity(ctx)
		if err == nil {
			t.Error("Expected connectivity check to fail with invalid host")
		}
	})
}

// Benchmark tests
func BenchmarkGetSystemInfo(b *testing.B) {
	server := createMockIDRACServer()
	defer server.Close()

	cfg := &config.IDRACConfig{
		IP:        "localhost",
		Username:  "root",
		Password:  "password",
		VerifySSL: false,
		Timeout:   30,
	}

	log := logger.NewLogger()
	defer log.Close()

	client := &Client{
		config:     cfg,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     log,
		baseURL:    server.URL,
	}

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := client.GetSystemInfo(ctx)
		if err != nil {
			b.Fatalf("GetSystemInfo failed: %v", err)
		}
	}
}
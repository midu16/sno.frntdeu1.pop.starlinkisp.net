// Package redfish is a small Redfish client for Dell iDRAC that replaces the
// sushy + sushy-oem-idrac python stack. It covers session auth, system
// status, virtual-media discovery/eject/insert, Dell OEM one-time boot, and
// system reset.
package redfish

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Boot devices for the Dell OEM OneShotBootDevice.
const (
	BootVirtualMediaCD  = "OneShot.VirtualMedia"
	BootVirtualMediaHDD = "OneShot.HDD"
)

// ResetType enumerates ComputerSystem.Reset reset actions.
type ResetType string

const (
	ResetTypeForceRestart ResetType = "Restart"
	ResetTypeOn           ResetType = "On"
	ResetTypeForceOff     ResetType = "ForceOff"
)

// PowerStateOn is the PowerState value meaning the system is powered on.
const PowerStateOn = "On"

// SystemStatus is the Systems/1 projection used by the installer.
type SystemStatus struct {
	Model      string `json:"Model"`
	PowerState string `json:"PowerState"`
	UUID       string `json:"UUID"`
}

// VirtualMedia is the Chassis VirtualMedia projection used by the installer.
type VirtualMedia struct {
	URI        string   `json:"-"`
	Inserted   bool     `json:"Inserted"`
	Image      string   `json:"Image"`
	MediaTypes []string `json:"MediaTypes"`
}

// Client talks to one iDRAC over HTTPS (TLS verification disabled, matching
// the historical sushy verify=False behavior for self-signed iDRAC certs).
type Client struct {
	baseURL   string
	username  string
	password  string
	http      *http.Client
	SessionID string
}

// New returns a Client for idracIP over HTTPS.
func New(idracIP, username, password string) *Client {
	return NewWithURL("https://"+idracIP, username, password)
}

// NewWithURL returns a Client for an explicit base URL (used by tests that
// point the client at an embedded iDRAC emulator).
func NewWithURL(baseURL, username, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(baseURL, "/"),
		username: username,
		password: password,
		http: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		},
	}
}

// Connect creates a Redfish session (best-effort) and returns system status.
func (c *Client) Connect(ctx context.Context) (*SystemStatus, error) {
	if err := c.createSession(ctx); err != nil {
		return nil, err
	}
	return c.SystemStatus(ctx)
}

// SystemStatus GETs Systems/1.
func (c *Client) SystemStatus(ctx context.Context) (*SystemStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/redfish/v1/Systems/1", nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redfish systems GET: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &SystemStatus{PowerState: "Unknown"}, nil
	}
	var status SystemStatus
	out, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(out, &status); err != nil {
		return nil, fmt.Errorf("redfish system status: %w", err)
	}
	return &status, nil
}

// VirtualMediaList enumerates the chassis virtual media collections and
// returns the individual media device objects.
func (c *Client) VirtualMediaList(ctx context.Context) ([]VirtualMedia, error) {
	coll, err := c.getJSON(ctx, "/redfish/v1/Chassis/1/VirtualMedia")
	if err != nil {
		return nil, err
	}
	var items []struct {
		URI string `json:"@odata.id"`
	}
	if m, ok := coll["Members"].([]any); ok {
		for _, m := range m {
			if s, ok := m.(string); ok {
				items = append(items, struct {
					URI string `json:"@odata.id"`
				}{s})
			}
		}
	}
	var out []VirtualMedia
	for _, it := range items {
		vm, err := c.getJSON(ctx, it.URI)
		if err != nil {
			return nil, err
		}
		dev := VirtualMedia{
			Inserted: boolField(vm, "Inserted"),
			Image:    strField(vm, "Image"),
			URI:      it.URI,
		}
		if mt, ok := vm["MediaTypes"].([]any); ok {
			for _, t := range mt {
				if s, ok := t.(string); ok {
					dev.MediaTypes = append(dev.MediaTypes, s)
				}
			}
		}
		out = append(out, dev)
	}
	return out, nil
}

// FindCDDevice returns the first virtual media device whose MediaTypes
// include "Cd", or a zero value with empty URI when none exists.
func (c *Client) FindCDDevice(ctx context.Context) (VirtualMedia, error) {
	devs, err := c.VirtualMediaList(ctx)
	if err != nil {
		return VirtualMedia{}, err
	}
	for _, d := range devs {
		for _, t := range d.MediaTypes {
			if strings.EqualFold(t, "Cd") {
				return d, nil
			}
		}
	}
	return VirtualMedia{}, nil
}

// SetOneShotBoot sets a Dell OEM one-time boot device.
func (c *Client) SetOneShotBoot(ctx context.Context, device string) error {
	payload := fmt.Sprintf(`{"OneShotBootDevice":%q,"PersistentBootOrder":false}`, device)
	return c.post(ctx, "/redfish/v1/Managers/1/Oem/Dell/DellOEMBoot", payload)
}

// Reset issues a system reset.
func (c *Client) Reset(ctx context.Context, reset ResetType) error {
	payload := fmt.Sprintf(`{"ResetType":%q}`, reset)
	return c.post(ctx, "/redfish/v1/Systems/1/Actions/ComputerSystem.Reset", payload)
}

// EjectMedia ejects virtual media from device. StatusConflict means nothing
// was mounted (treated as success, mirroring the python behavior).
func (c *Client) EjectMedia(ctx context.Context, device VirtualMedia) error {
	if device.URI == "" {
		return fmt.Errorf("no virtual CD device found on iDRAC")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+device.URI+"/Actions/VirtualMedia.Eject", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("redfish eject-media: %s", resp.Status)
	}
	return nil
}

// InsertMedia mounts an ISO from an HTTP URL into device.
func (c *Client) InsertMedia(ctx context.Context, device VirtualMedia, sourceURL string) error {
	if device.URI == "" {
		return fmt.Errorf("no virtual CD device found on iDRAC")
	}
	payload := fmt.Sprintf(`{"Image":%q,"Inserted":true,"Verified":false}`, sourceURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+device.URI, strings.NewReader(payload))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("virtual media insert failed: iDRAC rejected the mount or could not fetch the ISO.\n  URL: %s\n  Redfish: %s %s\n  Check: iDRAC management network can reach that host:port (routing/firewall), HTTP serves the file, and the path matches where copy-iso placed agent.x86_64.iso.",
			sourceURL, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// Refresh re-fetches the current Inserted/Image state of a media device.
func (c *Client) Refresh(ctx context.Context, device *VirtualMedia) error {
	vm, err := c.getJSON(ctx, device.URI)
	if err != nil {
		return err
	}
	device.Inserted = boolField(vm, "Inserted")
	device.Image = strField(vm, "Image")
	return nil
}

func strField(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// getJSON performs an authenticated GET and decodes the JSON body.
func (c *Client) getJSON(ctx context.Context, path string) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("redfish GET %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("redfish GET %s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	out, _ := io.ReadAll(resp.Body)
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("redfish GET %s decode: %w", path, err)
	}
	return parsed, nil
}

// post performs an authenticated POST to the given redfish path.
func (c *Client) post(ctx context.Context, path, payload string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+path, strings.NewReader(payload))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("redfish POST %s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// createSession POSTs credentials to the session service. A failed session
// is non-fatal: the client falls back to Basic auth on every request.
func (c *Client) createSession(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/redfish/v1/Session/Sessions", strings.NewReader(`{}`))
	if err != nil {
		return err
	}
	c.applyHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusCreated {
		return nil
	}
	// Redfish returns the token in the X-Auth-Token response header; the
	// "Identity" body field carries the same value on some BMCs.
	if tok := strings.TrimSpace(resp.Header.Get("X-Auth-Token")); tok != "" {
		c.SessionID = tok
		return nil
	}
	var parsed struct {
		Identity string `json:"Identity"`
	}
	if err := json.Unmarshal(body, &parsed); err == nil {
		c.SessionID = strings.TrimSpace(parsed.Identity)
	}
	return nil
}

// applyHeaders sets auth + accept headers, preferring a session token and
// falling back to Basic auth.
func (c *Client) applyHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	if c.SessionID != "" {
		req.Header.Set("X-Auth-Token", c.SessionID)
	} else {
		req.SetBasicAuth(c.username, c.password)
	}
}

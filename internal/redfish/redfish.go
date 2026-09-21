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
	"sync"
	"time"
)

// Boot devices are expressed as standard Redfish
// ComputerSystem.Boot.BootSourceOverrideTarget values (not the Dell OEM
// OneShotBootDevice strings, which this iDRAC generation does not expose).
//
// The iDRAC's boot menu / web UI labels the virtual optical device
// "Virtual CD/DVD/ISO"; the Redfish enum value the firmware accepts for that
// device is "Cd" (see the system's
// BootSourceOverrideTarget@Redfish.AllowableValues:
// None|Pxe|Cd|Floppy|Hdd|BiosSetup|Utilities|UefiTarget|SDCard|UefiHttp).
// "Cd" is therefore the wire encoding of the "Virtual CD/DVD/ISO" boot
// device. BootVirtualMediaCD/BootVirtualMediaHDD are the canonical device
// constants used throughout the installer.
const (
	// BootVirtualMediaCD is the Redfish BootSourceOverrideTarget for the
	// "Virtual CD/DVD/ISO" optical device.
	BootVirtualMediaCD = "Cd"
	// BootVirtualMediaHDD is the Redfish BootSourceOverrideTarget for the
	// local hard disk.
	BootVirtualMediaHDD = "Hdd"
	// virtualCDDVDISOLabel is the human-readable iDRAC name of the virtual
	// optical boot device, used in log/error text so the intent (boot from
	// the virtual CD/DVD/ISO, not the normal local disk) is unambiguous.
	virtualCDDVDISOLabel = "Virtual CD/DVD/ISO"
)

// VirtualCDDVDISOLabel returns the iDRAC human-readable name of the virtual
// optical boot device ("Virtual CD/DVD/ISO"). Exposed for log/error text so
// the intent (boot from the virtual CD/DVD/ISO, not the normal local disk) is
// self-documenting at every call site.
func VirtualCDDVDISOLabel() string { return virtualCDDVDISOLabel }

// ResetType enumerates ComputerSystem.Reset reset actions.
type ResetType string

const (
	// Note: "Restart" is not an acceptable ComputerSystem.Reset ResetType on
	// modern iDRAC firmware (the allowable set is On/ForceOff/ForceRestart/
	// GracefulShutdown/...); the historical Python tool used
	// sushy.RESET_TYPE_FORCE_RESTART, which maps to ForceRestart.
	ResetTypeForceRestart ResetType = "ForceRestart"
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
//
// Rather than assume a fixed resource layout, the client discovers the
// concrete member URIs from the Redfish collections on first use. This is
// required because Dell iDRAC generations use different addressing schemes:
// older iDRAC7 firmware (e.g. 2.83) exposes named members such as
// "Chassis/System.Embedded.1" and "Managers/iDRAC.Embedded.1" and hosts the
// VirtualMedia collection under the Manager, while newer iDRAC8/9 use the
// numeric "/redfish/v1/Systems/1" form. Discovery makes one client work
// across all of them.
type Client struct {
	baseURL   string
	username  string
	password  string
	http      *http.Client
	SessionID string

	// uriMu guards discovery of the member URIs below (lazy + memoised).
	uriMu             sync.Mutex
	cachedSystemURI   string
	cachedManagerURIs []string
	cachedChassisURIs []string
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

// systemURI returns the concrete ComputerSystem member URI for this iDRAC,
// discovered from the Systems collection on first use. iDRAC generations
// differ here (numeric "/1" vs named "System.Embedded.1"), so the member id
// must be read rather than assumed.
func (c *Client) systemURI(ctx context.Context) (string, error) {
	c.uriMu.Lock()
	defer c.uriMu.Unlock()
	if c.cachedSystemURI != "" {
		return c.cachedSystemURI, nil
	}
	coll, err := c.getJSON(ctx, "/redfish/v1/Systems")
	if err != nil {
		return "", err
	}
	id := firstMemberURI(coll)
	if id == "" {
		return "", fmt.Errorf("redfish: no system found in /redfish/v1/Systems")
	}
	c.cachedSystemURI = id
	return id, nil
}

// managerURIs returns the Manager member URIs (normally a single iDRAC).
func (c *Client) managerURIs(ctx context.Context) ([]string, error) {
	c.uriMu.Lock()
	defer c.uriMu.Unlock()
	if c.cachedManagerURIs != nil {
		return c.cachedManagerURIs, nil
	}
	coll, err := c.getJSON(ctx, "/redfish/v1/Managers")
	if err != nil {
		return nil, err
	}
	c.cachedManagerURIs = memberURIs(coll)
	return c.cachedManagerURIs, nil
}

// chassisURIs returns the Chassis member URIs.
func (c *Client) chassisURIs(ctx context.Context) ([]string, error) {
	c.uriMu.Lock()
	defer c.uriMu.Unlock()
	if c.cachedChassisURIs != nil {
		return c.cachedChassisURIs, nil
	}
	coll, err := c.getJSON(ctx, "/redfish/v1/Chassis")
	if err != nil {
		return nil, err
	}
	c.cachedChassisURIs = memberURIs(coll)
	return c.cachedChassisURIs, nil
}

// memberURIs extracts the ordered list of "@odata.id" member URIs from a
// collection document.
func memberURIs(coll map[string]any) []string {
	var out []string
	if m, ok := coll["Members"].([]any); ok {
		for _, it := range m {
			if mm, ok := it.(map[string]any); ok {
				if id, ok := mm["@odata.id"].(string); ok && id != "" {
					out = append(out, id)
				}
			}
		}
	}
	return out
}

// firstMemberURI returns the first member URI of a collection, or "".
func firstMemberURI(coll map[string]any) string {
	if ids := memberURIs(coll); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// Connect creates a Redfish session (best-effort) and returns system status.
func (c *Client) Connect(ctx context.Context) (*SystemStatus, error) {
	if err := c.createSession(ctx); err != nil {
		return nil, err
	}
	return c.SystemStatus(ctx)
}

// SystemStatus GETs the discovered ComputerSystem member.
func (c *Client) SystemStatus(ctx context.Context) (*SystemStatus, error) {
	sysURI, err := c.systemURI(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+sysURI, nil)
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

// VirtualMediaList enumerates the virtual media collections and returns the
// individual media device objects. The collection is probed under every
// discovered Manager and Chassis member (in that order), because on older
// iDRACs (e.g. iDRAC7) it is exposed under the Manager
// ("Managers/iDRAC.Embedded.1/VirtualMedia") while on newer iDRACs it is
// exposed under the embedded Chassis. The first collection that answers is
// used.
func (c *Client) VirtualMediaList(ctx context.Context) ([]VirtualMedia, error) {
	var candidates []string
	if mgrs, err := c.managerURIs(ctx); err == nil {
		for _, m := range mgrs {
			candidates = append(candidates, m+"/VirtualMedia")
		}
	}
	if chs, err := c.chassisURIs(ctx); err == nil {
		for _, ch := range chs {
			candidates = append(candidates, ch+"/VirtualMedia")
		}
	}
	if len(candidates) == 0 {
		candidates = []string{
			"/redfish/v1/Managers/1/VirtualMedia",
			"/redfish/v1/Chassis/1/VirtualMedia",
		}
	}
	var lastErr error
	for _, path := range candidates {
		coll, err := c.getJSON(ctx, path)
		if err != nil {
			lastErr = err // keep probing; a 404 on one parent is expected
			continue
		}
		devs, err := c.mediaDevicesFromCollection(ctx, coll)
		if err != nil {
			lastErr = err
			continue
		}
		if len(devs) > 0 {
			return devs, nil
		}
		lastErr = fmt.Errorf("redfish: virtual media collection %s has no members", path)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("redfish: no virtual media collection found on iDRAC (last error: %w)", lastErr)
	}
	return nil, nil
}

// mediaDevicesFromCollection turns a VirtualMedia collection document into
// its device projections, tolerating member entries that are plain
// "@odata.id" strings or full member objects.
func (c *Client) mediaDevicesFromCollection(ctx context.Context, coll map[string]any) ([]VirtualMedia, error) {
	var items []struct {
		URI string `json:"@odata.id"`
	}
	if m, ok := coll["Members"].([]any); ok {
		for _, m := range m {
			switch v := m.(type) {
			case string:
				if v != "" {
					items = append(items, struct {
						URI string `json:"@odata.id"`
					}{v})
				}
			case map[string]any:
				if id, ok := v["@odata.id"].(string); ok && id != "" {
					items = append(items, struct {
						URI string `json:"@odata.id"`
					}{id})
				}
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

// normalizeBootTarget maps the caller's requested boot device to the firmware
// valid ComputerSystem.Boot.BootSourceOverrideTarget value. The iDRAC accepts
// only a fixed enum (None|Pxe|Cd|Floppy|Hdd|...); "Virtual CD/DVD/ISO" is the
// iDRAC web-UI label for that optical device and its Redfish value is "Cd".
// This normalization is what lets the caller explicitly request "Virtual
// CD/DVD/ISO" while the wire payload carries the value the firmware will
// actually accept.
func normalizeBootTarget(device string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(device)) {
	case "cd", "virtual cd/dvd/iso", "virtual cd", "vdvd", "vcd", "virtual_media", "one-shot.virtualmedia", "virtualmedia":
		return "Cd", nil
	case "hdd", "hard disk", "harddisk", "localhdd", "hddlist.1-1", "one-shot.hdd":
		return "Hdd", nil
	case "pxe", "floppy", "sd":
		// Pass through: these already match the enum (Pxe/Floppy/SDCard).
		if device == "SD" {
			return "SDCard", nil
		}
		return device, nil
	case "":
		return "", fmt.Errorf("no boot device specified")
	default:
		return "", fmt.Errorf("unsupported boot device %q (expected %s or HDD)", device, virtualCDDVDISOLabel)
	}
}

// bootTargetConfirmed reads back the discovered system's Boot block and
// reports whether the one-time boot override is enabled with the expected
// target, returning the observed target for diagnostics.
func (c *Client) bootTargetConfirmed(ctx context.Context, sysURI, wantTarget string) (ok bool, gotTarget string) {
	sys, err := c.getJSON(ctx, sysURI)
	if err != nil {
		return false, ""
	}
	boot, okB := sys["Boot"].(map[string]any)
	if !okB {
		return false, ""
	}
	en, _ := boot["BootSourceOverrideEnabled"].(string)
	tg, _ := boot["BootSourceOverrideTarget"].(string)
	gotTarget = tg
	return en == "Once" && tg == wantTarget, tg
}

// SetOneShotBoot sets a one-time boot device using the standard Redfish
// ComputerSystem.Boot override. It patches the discovered system with
// BootSourceOverrideEnabled="Once" and the requested BootSourceOverrideTarget
// (the "Virtual CD/DVD/ISO" device is encoded as "Cd"), then READS THE VALUE
// BACK and confirms it took. It never silently falls through: if the boot
// device cannot be confirmed as the Virtual CD/DVD/ISO, it returns an error so
// the installer aborts before the restart — the node must NOT be left to boot
// its normal local disk in place of the install ISO.
func (c *Client) SetOneShotBoot(ctx context.Context, device string) error {
	target, err := normalizeBootTarget(device)
	if err != nil {
		return err
	}
	sysURI, err := c.systemURI(ctx)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf(`{"Boot":{"BootSourceOverrideEnabled":"Once","BootSourceOverrideTarget":%q}}`, target)
	const retries = 3
	var lastGot string
	for attempt := 1; attempt <= retries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := c.patch(ctx, sysURI, payload); err != nil {
			if attempt == retries {
				return fmt.Errorf("could not set the one-time boot device to %s (target %q): %w", virtualCDDVDISOLabel, target, err)
			}
			time.Sleep(3 * time.Second)
			continue
		}
		ok, got := c.bootTargetConfirmed(ctx, sysURI, target)
		if ok {
			return nil
		}
		lastGot = got
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf(
		"one-time boot to %s could not be confirmed (read-back target %q, want %q); aborting rather than restart into a fallback/Normal boot",
		virtualCDDVDISOLabel, lastGot, target)
}

// Reset issues a system reset against the discovered system member.
func (c *Client) Reset(ctx context.Context, reset ResetType) error {
	sysURI, err := c.systemURI(ctx)
	if err != nil {
		return err
	}
	payload := fmt.Sprintf(`{"ResetType":%q}`, reset)
	return c.post(ctx, sysURI+"/Actions/ComputerSystem.Reset", payload)
}

// MediaActions returns the eject and insert action URIs for a media device.
// The action targets are read from the device's Actions map when present
// (e.g. iDRAC7 exposes "VirtualMedia.EjectMedia"/"VirtualMedia.InsertMedia");
// otherwise the canonical Redfish action names are assumed. Both actions are
// invoked with Content-Type application/json (the iDRAC requires it even for
// the empty eject body).
func (c *Client) MediaActions(ctx context.Context, device VirtualMedia, deviceDoc map[string]any) (ejectURI, insertURI string) {
	const (
		defaultEject  = "VirtualMedia.Eject"
		defaultInsert = "VirtualMedia.Insert"
	)
	if actions, ok := deviceDoc["Actions"].(map[string]any); ok {
		for k, v := range actions {
			am, ok := v.(map[string]any)
			if !ok {
				continue
			}
			target, _ := am["target"].(string)
			switch {
			case strings.Contains(k, "Eject") && target != "":
				ejectURI = target
			case strings.Contains(k, "Insert") && target != "":
				insertURI = target
			}
		}
	}
	if ejectURI == "" {
		ejectURI = device.URI + "/Actions/" + defaultEject
	}
	if insertURI == "" {
		insertURI = device.URI + "/Actions/" + defaultInsert
	}
	return ejectURI, insertURI
}

// EjectMedia ejects virtual media from device. StatusConflict means nothing
// was mounted (treated as success, mirroring the python behavior).
func (c *Client) EjectMedia(ctx context.Context, device VirtualMedia) error {
	if device.URI == "" {
		return fmt.Errorf("no virtual CD device found on iDRAC")
	}
	doc, err := c.getJSON(ctx, device.URI)
	if err != nil {
		return err
	}
	ejectURI, _ := c.MediaActions(ctx, device, doc)
	if err := c.post(ctx, ejectURI, "{}"); err != nil {
		if strings.Contains(err.Error(), "Conflict") {
			// 409: nothing was mounted; treat as success.
			return nil
		}
		return fmt.Errorf("redfish eject-media: %w", err)
	}
	return nil
}

// InsertMedia mounts an ISO from an HTTP URL into device.
func (c *Client) InsertMedia(ctx context.Context, device VirtualMedia, sourceURL string) error {
	if device.URI == "" {
		return fmt.Errorf("no virtual CD device found on iDRAC")
	}
	doc, err := c.getJSON(ctx, device.URI)
	if err != nil {
		return err
	}
	_, insertURI := c.MediaActions(ctx, device, doc)
	payload := fmt.Sprintf(`{"Image":%q,"Inserted":true,"Verified":false}`, sourceURL)
	if err := c.post(ctx, insertURI, payload); err != nil {
		return fmt.Errorf("virtual media insert failed: iDRAC rejected the mount or could not fetch the ISO.\n  URL: %s\n  Redfish: %v\n  Check: iDRAC management network can reach that host:port (routing/firewall), HTTP serves the file, and the path matches where copy-iso placed agent.x86_64.iso.",
			sourceURL, err)
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

// patch performs an authenticated PATCH to the given redfish path (used for
// the standard Redfish boot-override one-time boot, which this iDRAC accepts
// via PATCH on the System resource but not via POST).
func (c *Client) patch(ctx context.Context, path, payload string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch,
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
		return fmt.Errorf("redfish PATCH %s: %s %s", path, resp.Status, strings.TrimSpace(string(body)))
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

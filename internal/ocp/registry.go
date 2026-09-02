package ocp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

// DefaultRegistryURL is Red Hat's public OpenShift version catalog. It is
// the single source of truth for "currently supported" OpenShift versions
// used by the CI matrix and the MCP versions tool.
const DefaultRegistryURL = "https://api.openshift.com/products/openshift"

// RegistryError marks failures while querying the version catalog.
type RegistryError struct{ Err error }

// Error implements error.
func (e RegistryError) Error() string { return e.Err.Error() }

// Unwrap supports errors.Is/As.
func (e RegistryError) Unwrap() error { return e.Err }

// RegistryClient fetches supported OCP versions.
type RegistryClient struct {
	// BaseURL of the version catalog (override with OCP_VERSION_REGISTRY_URL;
	// CI can point it at a pinned fixture or a mirror).
	BaseURL string
	// HTTP client for calls (default built when nil).
	HTTP *http.Client
}

// NewRegistryClient builds a catalog client honouring the
// OCP_VERSION_REGISTRY_URL environment override.
func NewRegistryClient() *RegistryClient {
	base := strings.TrimSpace(os.Getenv("OCP_VERSION_REGISTRY_URL"))
	if base == "" {
		base = DefaultRegistryURL
	}
	return &RegistryClient{
		BaseURL: base,
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

// Supported returns the de-duplicated, sorted list of OCP versions the
// catalog currently lists. The catalog JSON shape is walked defensively
// (any string value matching the version grammar is collected) so the
// matrix keeps adapting to catalog schema changes without code updates.
func (c *RegistryClient) Supported(ctx context.Context) ([]Version, error) {
	if c.HTTP == nil {
		c = NewRegistryClient()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL, nil)
	if err != nil {
		return nil, RegistryError{err}
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, RegistryError{fmt.Errorf("version catalog: %w", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, RegistryError{fmt.Errorf("version catalog: HTTP %d from %s", resp.StatusCode, c.BaseURL)}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, RegistryError{fmt.Errorf("version catalog: read: %w", err)}
	}

	seen := map[string]struct{}{}
	var raws []string
	collectVersionStrings(body, &raws, &seen)

	var out []Version
	for _, r := range raws {
		v, err := Parse(r)
		if err != nil {
			continue
		}
		if v.Channel != ChannelGA {
			continue // the CI matrix targets GA releases only
		}
		out = append(out, v)
	}
	// Newest first (major, minor, patch descending).
	sort.Slice(out, func(a, b int) bool {
		if out[a].Major != out[b].Major {
			return out[a].Major > out[b].Major
		}
		if out[a].Minor != out[b].Minor {
			return out[a].Minor > out[b].Minor
		}
		return out[a].Patch > out[b].Patch
	})
	if len(out) == 0 {
		return nil, RegistryError{fmt.Errorf("version catalog: no GA versions found in %s", c.BaseURL)}
	}
	// Deduplicate by raw version string (keep first).
	uniq := map[string]struct{}{}
	var dedup []Version
	for _, v := range out {
		if _, ok := uniq[v.Raw]; ok {
			continue
		}
		uniq[v.Raw] = struct{}{}
		dedup = append(dedup, v)
	}
	return dedup, nil
}

// Latest returns the newest GA version from the catalog.
func (c *RegistryClient) Latest(ctx context.Context) (Version, error) {
	versions, err := c.Supported(ctx)
	if err != nil {
		return Version{}, err
	}
	return versions[0], nil
}

// collectVersionStrings depth-first walks arbitrary JSON collecting every
// string value that parses as a full X.Y.Z version. Deduplication is via
// the seen map; raws may then contain unparseable candidates (filtered by
// the caller via Parse).
func collectVersionStrings(data []byte, raws *[]string, seen *map[string]struct{}) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	walkJSONValue(dec, raws, seen)
}

// walkJSONValue reads one JSON value from dec recursively (the decoder
// must be positioned at a value start).
func walkJSONValue(dec *json.Decoder, raws *[]string, seen *map[string]struct{}) {
	tok, err := dec.Token()
	if err != nil {
		return
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '[':
			for dec.More() {
				walkJSONValue(dec, raws, seen)
			}
			_, _ = dec.Token() // ']' (tolerate EOF on malformed input)
		case '{':
			for dec.More() {
				if _, err := dec.Token(); err != nil { // object key
					return
				}
				walkJSONValue(dec, raws, seen)
			}
			_, _ = dec.Token() // '}'
		}
	case string:
		collectOne(t, raws, seen)
	}
}

// collectOne records a version-grammar string.
func collectOne(s string, raws *[]string, seen *map[string]struct{}) {
	s = strings.TrimSpace(s)
	if !looksLikeFullVersion(s) {
		return
	}
	if _, ok := (*seen)[s]; ok {
		return
	}
	(*seen)[s] = struct{}{}
	*raws = append(*raws, s)
}

// looksLikeFullVersion is a fast pre-filter before the strict Parse.
func looksLikeFullVersion(s string) bool {
	parts := strings.SplitN(s, ".", 3)
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts[:2] {
		if !allDigits(p) {
			return false
		}
	}
	// Third part may be "0" or "0-nightly..." etc.
	return allDigits(prefixBeforeDash(parts[2]))
}

// prefixBeforeDash returns the substring before the first dash (or all).
func prefixBeforeDash(s string) string {
	if i := strings.IndexByte(s, '-'); i >= 0 {
		return s[:i]
	}
	return s
}

// allDigits reports whether s is non-empty decimal.
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

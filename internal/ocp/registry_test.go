package ocp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseGA(t *testing.T) {
	v, err := Parse("4.18.6")
	if err != nil {
		t.Fatal(err)
	}
	if v.Major != 4 || v.Minor != 18 || v.Patch != 6 || v.Channel != ChannelGA {
		t.Fatalf("unexpected parse: %+v", v)
	}
	if got := v.Normalized(); got != "4.18.6" {
		t.Fatalf("normalized = %q", got)
	}
}

func TestParsePreGA(t *testing.T) {
	v, err := Parse("5.0.0-ec.6")
	if err != nil {
		t.Fatal(err)
	}
	if v.Channel != ChannelEC || v.ChannelN != 6 {
		t.Fatalf("unexpected: %+v", v)
	}
	if !v.IsPreGA() {
		t.Fatal("expected pre-GA")
	}
}

func TestParseNightly(t *testing.T) {
	v, err := Parse("5.0.0-0.nightly-2026-08-21-033959")
	if err != nil {
		t.Fatal(err)
	}
	if !v.IsNightly() || v.Major != 5 || v.Minor != 0 {
		t.Fatalf("unexpected: %+v", v)
	}
	if v.NightlyStamp != "2026-08-21-033959" {
		t.Fatalf("stamp = %q", v.NightlyStamp)
	}
}

func TestParseInvalid(t *testing.T) {
	for _, bad := range []string{"", "abc", "4.", "4.18", "1.2.3.4", "4.x.1"} {
		if _, err := Parse(bad); err == nil {
			t.Fatalf("expected error for %q", bad)
		}
	}
}

func TestQuayPullspec(t *testing.T) {
	v := Version{Raw: "4.18.6", Major: 4, Minor: 18, Patch: 6, Channel: ChannelGA}
	if got := v.DefaultPullSpec(); got != QuayRelease+":4.18.6-x86_64" {
		t.Fatalf("pullspec = %q", got)
	}
	n := Version{Raw: "5.0.0-0.nightly-2026-08-21-033959", Major: 5, Minor: 0, Channel: ChannelNightly}
	// CI nightlies live in per-minor imagestreams (ocp/release-5.0), not
	// a major-only stream.
	if got := n.DefaultPullSpec(); got != CINightlyBase+"/release-5.0:5.0.0-0.nightly-2026-08-21-033959" {
		t.Fatalf("nightly pullspec = %q", got)
	}
}

// fixture shape loosely mirrors api.openshift.com/products/openshift.
const catalogFixture = `{
  "releases": [
    {
      "versions": [
        {"id": "4.19.2", "version": "4.19.2"},
        {"id": "4.19.1", "version": "4.19.1"},
        {"id": "4.18.11", "version": "4.18.11"},
        {"id": "5.0.4", "version": "5.0.4"}
      ]
    }
  ],
  "channels": {
    "zstream": [
      {"name": "4-stable", "versions": [{"id": "4.19.2"}, {"id": "4.18.11"}]}
    ]
  },
  "notes": "not a version 9.9",
  "other": {"deep": {"version": "4.17.50"}}
}`

func TestRegistrySupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(catalogFixture))
	}))
	defer srv.Close()

	c := &RegistryClient{BaseURL: srv.URL, HTTP: srv.Client()}
	versions, err := c.Supported(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(versions))
	for _, v := range versions {
		got = append(got, v.Raw)
	}
	// Expect newest-first, GA only, de-duplicated, nested + nested-deep found.
	want := []string{"5.0.4", "4.19.2", "4.19.1", "4.18.11", "4.17.50"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestRegistryErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	c := &RegistryClient{BaseURL: srv.URL, HTTP: srv.Client()}
	if _, err := c.Supported(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

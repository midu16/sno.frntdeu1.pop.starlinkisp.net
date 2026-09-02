// Package olm implements the OLM-specific waits used by day-2 operator
// configuration: InstallPlan detection/approval, Subscription
// InstallPlan-existence checks, CSV phase waits and CRD Established waits.
// It is the native Go replacement for scripts/apply_operator_config.py
// (kubernetes python client) and talks to the API through the `oc` CLI.
package olm

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sno/internal/ocx"
)

const (
	// RequestTimeoutSec bounds long API calls (mirrors the 600s oc
	// subprocess timeout of the python implementation).
	RequestTimeoutSec = 600
)

// olmspec/status projections used by the waits.
type objectMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type installPlan struct {
	Metadata objectMeta `json:"metadata"`
	Spec     struct {
		Approved                   *bool    `json:"approved"`
		ClusterServiceVersionNames []string `json:"clusterServiceVersionNames"`
	} `json:"spec"`
}

type subscriptionStatus struct {
	InstallPlanRef json.RawMessage `json:"installPlanRef"`
	CurrentCSV     string          `json:"currentCSV"`
	State          string          `json:"state"`
}

type crdCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type crd struct {
	Metadata objectMeta `json:"metadata"`
	Status   struct {
		Conditions []crdCondition `json:"conditions"`
	} `json:"status"`
}

// Client wraps an ocx.Runner for OLM operations.
type Client struct {
	oc *ocx.Runner
}

// New returns a Client.
func New(oc *ocx.Runner) *Client { return &Client{oc: oc} }

// ListAllInstallPlans lists InstallPlans cluster-wide.
func (c *Client) ListAllInstallPlans() ([]installPlan, error) {
	out, err := c.oc.GetJSON("get", "installplans", "-A")
	if err != nil {
		return nil, fmt.Errorf("list installplans (cluster) failed: %w", err)
	}
	var list struct {
		Items []installPlan `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// UnapprovedInstallPlans returns (namespace, name) pairs where
// spec.approved == false.
func (c *Client) UnapprovedInstallPlans() ([][2]string, error) {
	items, err := c.ListAllInstallPlans()
	if err != nil {
		return nil, err
	}
	var out [][2]string
	for _, ip := range items {
		if ip.Spec.Approved == nil || *ip.Spec.Approved {
			continue
		}
		if ip.Metadata.Name == "" || ip.Metadata.Namespace == "" {
			continue
		}
		out = append(out, [2]string{ip.Metadata.Namespace, ip.Metadata.Name})
	}
	return out, nil
}

// ApprovePending merges approved=true onto every unapproved InstallPlan and
// returns how many were approved.
func (c *Client) ApprovePending() (int, error) {
	return c.approvePending(false)
}

func (c *Client) approvePending(quietIfNone bool) (int, error) {
	pending, err := c.UnapprovedInstallPlans()
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		if !quietIfNone {
			fmt.Println("No unapproved InstallPlans (nothing to approve).")
		}
		return 0, nil
	}
	fmt.Printf("Approving %d pending InstallPlan(s) ...\n", len(pending))
	for _, pn := range pending {
		fmt.Printf("  approve installplan/%s in %s\n", pn[1], pn[0])
		if err := c.oc.PatchMerge("installplan", pn[1], pn[0], `{"spec":{"approved":true}}`); err != nil {
			return 0, fmt.Errorf("patch installplan/%s in %s failed: %w", pn[1], pn[0], err)
		}
	}
	fmt.Println("InstallPlan approval complete.")
	return len(pending), nil
}

// SubscriptionHasInstallPlan reports whether the given Subscription already
// has an InstallPlan assigned (installPlanRef set, or a namespace InstallPlan
// naming its currentCSV — matches the python fallback).
func (c *Client) SubscriptionHasInstallPlan(namespace, name string) (bool, error) {
	out, err := c.oc.GetJSON("get", "subscription", name, "-n", namespace)
	if err != nil {
		if ocx.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get subscription/%s in %s failed: %w", name, namespace, err)
	}
	var sub struct {
		Status subscriptionStatus `json:"status"`
	}
	if err := json.Unmarshal([]byte(out), &sub); err != nil {
		return false, err
	}
	if len(sub.Status.InstallPlanRef) > 0 && string(sub.Status.InstallPlanRef) != "null" {
		return true, nil
	}
	installPlans, err := c.ListAllInstallPlans()
	if err != nil {
		return false, err
	}
	currentCSV := sub.Status.CurrentCSV
	for _, ip := range installPlans {
		if ip.Metadata.Namespace != namespace {
			continue
		}
		csvs := ip.Spec.ClusterServiceVersionNames
		if currentCSV != "" && contains(csvs, currentCSV) {
			return true, nil
		}
		if currentCSV == "" && len(csvs) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// WaitForInstallPlans blocks until every (ns,name) subscription has an
// InstallPlan, or returns an error on timeout.
func (c *Client) WaitForInstallPlans(required [][2]string, timeout, poll time.Duration) error {
	fmt.Printf("Waiting for required Subscriptions to produce InstallPlans (up to %s) ...\n", timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		var missing []string
		for _, rn := range required {
			ok, err := c.SubscriptionHasInstallPlan(rn[0], rn[1])
			if err == nil && ok {
				continue
			}
			missing = append(missing, rn[0]+"/"+rn[1])
		}
		if len(missing) == 0 {
			fmt.Println("Required InstallPlans are present.")
			return nil
		}
		fmt.Printf("  still waiting for InstallPlan(s): %s\n", strings.Join(missing, ", "))
		select {
		case <-c.oc.Ctx.Done():
			return c.oc.Ctx.Err()
		case <-time.After(poll):
		}
	}
	return fmt.Errorf("timeout after %s waiting for InstallPlans from required subscriptions", timeout)
}

// csvEntry is a minimal CSV projection.
type csvEntry struct {
	Metadata objectMeta `json:"metadata"`
	Status   struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// WaitCSVSucceeded blocks until the first CSV in namespace whose name
// starts with prefix reaches phase Succeeded. onTick runs before each poll
// (used for late InstallPlan approvals / MCO remediation). Diagnostics are
// printed on timeout.
func (c *Client) WaitCSVSucceeded(namespace, namePrefix, description string, timeout, poll time.Duration, onTick func()) error {
	fmt.Printf("Waiting for %s CSV (name prefix %s) in %s (up to %s) ...\n",
		description, namePrefix, namespace, timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if onTick != nil {
			onTick()
		}
		data, err := c.oc.GetJSON("get", "csv", "-n", namespace)
		if err != nil {
			fmt.Printf("  (list csv failed: %v)\n", err)
			select {
			case <-c.oc.Ctx.Done():
				return c.oc.Ctx.Err()
			case <-time.After(poll):
			}
			continue
		}
		var list struct {
			Items []csvEntry `json:"items"`
		}
		_ = json.Unmarshal([]byte(data), &list)
		matched := false
		for _, csv := range list.Items {
			if !strings.HasPrefix(csv.Metadata.Name, namePrefix) {
				continue
			}
			matched = true
			phase := csv.Status.Phase
			if phase == "" {
				phase = "unknown"
			}
			fmt.Printf("  %s: %s\n", csv.Metadata.Name, phase)
			if phase == "Succeeded" {
				fmt.Printf("  %s ready.\n", description)
				return nil
			}
			break
		}
		if !matched {
			fmt.Printf("  (no CSV with prefix %s in %s yet)\n", namePrefix, namespace)
		}
		select {
		case <-c.oc.Ctx.Done():
			return c.oc.Ctx.Err()
		case <-time.After(poll):
		}
	}
	fmt.Printf("Timeout waiting for %s CSV in %s.\n", description, namespace)
	c.PrintCSVTimeoutDiagnostics(namespace)
	return fmt.Errorf("timeout waiting for %s CSV in %s after %s", description, namespace, timeout)
}

// PrintCSVTimeoutDiagnostics dumps subscriptions, CSVs, pods and recent
// events in a namespace after a CSV wait failure.
func (c *Client) PrintCSVTimeoutDiagnostics(namespace string) {
	for _, plural := range []string{"subscriptions", "clusterserviceversions"} {
		fmt.Printf("--- %s (%s) ---\n", plural, namespace)
		out, err := c.oc.GetJSON("get", plural, "-n", namespace)
		if err != nil {
			fmt.Printf("  (list %s failed: %v)\n", plural, err)
			continue
		}
		var list struct {
			Items []struct {
				Metadata objectMeta `json:"metadata"`
				Status   struct {
					Phase string `json:"phase"`
					State string `json:"state"`
				} `json:"status"`
			} `json:"items"`
		}
		if json.Unmarshal([]byte(out), &list) != nil {
			continue
		}
		for _, it := range list.Items {
			phase := it.Status.Phase
			if phase == "" {
				phase = it.Status.State
			}
			fmt.Printf("  %s\t%s\n", it.Metadata.Name, phase)
		}
	}
	fmt.Printf("--- pods (%s) ---\n", namespace)
	out, err := c.oc.GetJSON("get", "pods", "-n", namespace)
	if err != nil {
		fmt.Printf("  (list pods failed: %v)\n", err)
	} else {
		var pods []struct {
			Metadata objectMeta `json:"metadata"`
			Status   struct {
				Phase string `json:"phase"`
			} `json:"status"`
		}
		if json.Unmarshal([]byte(out), &pods) == nil {
			for _, p := range pods {
				fmt.Printf("  %s\t%s\n", p.Metadata.Name, p.Status.Phase)
			}
		}
	}
	fmt.Println("Recent events:")
	if ev, err := c.oc.CaptureTail("get", "events", "-n", namespace, "--sort-by=.lastTimestamp", "-o", "name"); err == nil {
		for _, l := range ocx.ReadLines(ev) {
			fmt.Println("  " + l)
		}
	}
}

// WaitCRD blocks until the CRD exists; if Established is not True within
// 120s it continues anyway (matches python behavior).
func (c *Client) WaitCRD(crdName string, timeout time.Duration) error {
	fmt.Printf("Waiting for CRD %s (up to %s) ...\n", crdName, timeout)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := c.oc.GetJSON("get", "crd", crdName)
		if err != nil {
			if !ocx.IsNotFound(err) {
				fmt.Printf("  (get crd failed: %v)\n", err)
			}
			select {
			case <-c.oc.Ctx.Done():
				return c.oc.Ctx.Err()
			case <-time.After(5 * time.Second):
			}
			continue
		}
		var crdObj crd
		_ = json.Unmarshal([]byte(out), &crdObj)
		estDeadline := time.Now().Add(120 * time.Second)
		for time.Now().Before(estDeadline) {
			if crdHasEstablished(crdObj) {
				fmt.Printf("CRD %s available.\n", crdName)
				return nil
			}
			out, _ = c.oc.GetJSON("get", "crd", crdName)
			_ = json.Unmarshal([]byte(out), &crdObj)
			select {
			case <-c.oc.Ctx.Done():
				return c.oc.Ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		fmt.Printf("Note: Established condition not True for %s within 120s; continuing.\n", crdName)
		fmt.Printf("CRD %s available.\n", crdName)
		return nil
	}
	fmt.Printf("Timeout waiting for CRD %s after %s. Relevant CRDs (lvm/sriov):\n", crdName, timeout)
	if out, err := c.oc.GetJSON("get", "crd", "-o", "name"); err == nil {
		for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
			name := strings.TrimPrefix(l, "customresourcedefinition.apiextensions.k8s.io/")
			if strings.Contains(strings.ToLower(name), "lvm") || strings.Contains(strings.ToLower(name), "sriov") {
				fmt.Printf("  %s\n", name)
			}
		}
	}
	return fmt.Errorf("timeout waiting for CRD %s after %s", crdName, timeout)
}

func crdHasEstablished(c crd) bool {
	for _, cond := range c.Status.Conditions {
		if cond.Type == "Established" && cond.Status == "True" {
			return true
		}
	}
	return false
}

func contains(list []string, v string) bool {
	for _, item := range list {
		if item == v {
			return true
		}
	}
	return false
}

// Compute: the instances behind the machines on the cluster page.
//
// A node in guard is declared — a name, an address, a health path — and that
// is deliberately independent of any provider. Linking one to an instance
// adds what the provider knows and guard cannot see from outside: whether the
// box is powered on at all, what it costs, how much bandwidth it has left,
// and the snapshots it can be rolled back to.
//
// The split matters when something breaks. A health check says "the service
// did not answer"; the provider says "the machine is stopped" — and those
// call for different people at different hours.

package vultr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Instance is one compute instance, as much of it as a dashboard has any use
// for. The provider's answer is wider; the fields left out are the ones that
// only mean something inside Vultr's own console.
type Instance struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Hostname string `json:"hostname,omitempty"`
	OS       string `json:"os,omitempty"`
	Region   string `json:"region"`
	Plan     string `json:"plan"`
	// MainIP is the public address. A machine still installing has none, and
	// answers 0.0.0.0 rather than an empty string — normalised here, because
	// "0.0.0.0" rendered as a link is a link to nowhere.
	MainIP     string `json:"main_ip,omitempty"`
	InternalIP string `json:"internal_ip,omitempty"`
	MainIPv6   string `json:"main_ipv6,omitempty"`
	VCPUs      int    `json:"vcpu_count"`
	RAMMB      int    `json:"ram_mb"`
	DiskGB     int    `json:"disk_gb"`
	// AllowedBandwidthGB is the month's allowance. Paired with the usage from
	// Bandwidth it is the one provider number that turns into a surprise bill.
	AllowedBandwidthGB int `json:"allowed_bandwidth_gb"`
	// Status is the subscription — active, pending, suspended. PowerStatus is
	// the switch — running, stopped. ServerStatus is what the provider makes
	// of the guest — ok, installingbooting, locked, none.
	//
	// Three, because they fail apart: an active subscription can be stopped,
	// and a running instance can still be installing.
	Status       string    `json:"status"`
	PowerStatus  string    `json:"power_status"`
	ServerStatus string    `json:"server_status"`
	Created      time.Time `json:"created"`
	Tags         []string  `json:"tags,omitempty"`
	// MonthlyCost is what the plan bills, as the provider reports it.
	MonthlyCost float64 `json:"monthly_cost,omitempty"`
}

// vultrInstance is the provider's shape, which is flatter and noisier than
// what the dashboard reads.
type vultrInstance struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	Hostname         string   `json:"hostname"`
	OS               string   `json:"os"`
	Region           string   `json:"region"`
	Plan             string   `json:"plan"`
	MainIP           string   `json:"main_ip"`
	InternalIP       string   `json:"internal_ip"`
	V6MainIP         string   `json:"v6_main_ip"`
	VCPUCount        int      `json:"vcpu_count"`
	RAM              int      `json:"ram"`
	Disk             int      `json:"disk"`
	AllowedBandwidth int      `json:"allowed_bandwidth"`
	Status           string   `json:"status"`
	PowerStatus      string   `json:"power_status"`
	ServerStatus     string   `json:"server_status"`
	DateCreated      string   `json:"date_created"`
	Tags             []string `json:"tags"`
}

func (v vultrInstance) instance() Instance {
	created, _ := time.Parse(time.RFC3339, v.DateCreated)
	out := Instance{
		ID: v.ID, Label: v.Label, Hostname: v.Hostname, OS: v.OS,
		Region: v.Region, Plan: v.Plan,
		MainIP: v.MainIP, InternalIP: v.InternalIP, MainIPv6: v.V6MainIP,
		VCPUs: v.VCPUCount, RAMMB: v.RAM, DiskGB: v.Disk,
		AllowedBandwidthGB: v.AllowedBandwidth,
		Status:             v.Status, PowerStatus: v.PowerStatus, ServerStatus: v.ServerStatus,
		Created: created, Tags: v.Tags,
	}
	// A machine mid-install reports 0.0.0.0. That is not an address anybody
	// can use, and shown as one it is a link that fails.
	if out.MainIP == "0.0.0.0" {
		out.MainIP = ""
	}
	if out.InternalIP == "0.0.0.0" {
		out.InternalIP = ""
	}
	return out
}

// Instances lists every instance the key can see, oldest label first so the
// picker does not reshuffle between openings.
func (c *Client) Instances(ctx context.Context, apiKey string) ([]Instance, error) {
	var out []Instance
	err := c.paged(ctx, apiKey, "/v2/instances", func(raw []byte) (string, error) {
		var page struct {
			Instances []vultrInstance `json:"instances"`
			nextCursor
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", fmt.Errorf("vultr answered something unreadable: %w", err)
		}
		for _, v := range page.Instances {
			out = append(out, v.instance())
		}
		return page.Meta.Links.Next, nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(a, b int) bool {
		return strings.ToLower(out[a].Label) < strings.ToLower(out[b].Label)
	})
	return out, nil
}

// Instance reads one instance. This is the call behind a machine's provider
// strip, so it is one request and never a filtered list: the cluster page has
// enough machines that "list everything to find one" is somebody's rate limit.
func (c *Client) Instance(ctx context.Context, apiKey, instanceID string) (Instance, error) {
	var answer struct {
		Instance vultrInstance `json:"instance"`
	}
	address := c.base + "/v2/instances/" + url.PathEscape(instanceID)
	if err := c.vultr(ctx, apiKey, http.MethodGet, address, &answer); err != nil {
		return Instance{}, err
	}
	return answer.Instance.instance(), nil
}

// PowerActions are the three switches, and the whole of them. Named here
// because the endpoint validates against this list rather than pasting a
// caller's word into a URL.
var PowerActions = []string{"start", "halt", "reboot"}

// Power flips one instance. This is the provider's switch, not the guest's:
// halt is the power cable, where the stored SSH action "sudo reboot" is the
// operating system being asked politely. The difference is the whole reason
// both exist — a box that stopped answering SSH can still be power-cycled.
func (c *Client) Power(ctx context.Context, apiKey, instanceID, action string) error {
	if !validPower(action) {
		return fmt.Errorf("%q is not a power action", action)
	}
	address := c.base + "/v2/instances/" + url.PathEscape(instanceID) + "/" + action
	return c.vultr(ctx, apiKey, http.MethodPost, address, nil)
}

func validPower(action string) bool {
	for _, candidate := range PowerActions {
		if candidate == action {
			return true
		}
	}
	return false
}

// Bandwidth is the month's transfer, day by day, in bytes.
type Bandwidth struct {
	Days []BandwidthDay `json:"days"`
	// In and Out are the totals over the days returned, which is the current
	// billing month as the provider reports it.
	In  int64 `json:"in_bytes"`
	Out int64 `json:"out_bytes"`
}

// BandwidthDay is one day's transfer. Date is the provider's own key
// ("2026-08-13"), kept as text because it is a label on a bar, not a moment.
type BandwidthDay struct {
	Date string `json:"date"`
	In   int64  `json:"in_bytes"`
	Out  int64  `json:"out_bytes"`
}

// BandwidthFor reads one instance's transfer for the current month.
func (c *Client) BandwidthFor(ctx context.Context, apiKey, instanceID string) (Bandwidth, error) {
	var answer struct {
		Bandwidth map[string]struct {
			IncomingBytes int64 `json:"incoming_bytes"`
			OutgoingBytes int64 `json:"outgoing_bytes"`
		} `json:"bandwidth"`
	}
	address := c.base + "/v2/instances/" + url.PathEscape(instanceID) + "/bandwidth"
	if err := c.vultr(ctx, apiKey, http.MethodGet, address, &answer); err != nil {
		return Bandwidth{}, err
	}
	out := Bandwidth{Days: make([]BandwidthDay, 0, len(answer.Bandwidth))}
	for date, day := range answer.Bandwidth {
		out.Days = append(out.Days, BandwidthDay{Date: date, In: day.IncomingBytes, Out: day.OutgoingBytes})
		out.In += day.IncomingBytes
		out.Out += day.OutgoingBytes
	}
	// The provider answers a map, and a map has no order. The chart needs one.
	sort.Slice(out.Days, func(a, b int) bool { return out.Days[a].Date < out.Days[b].Date })
	return out, nil
}

// Snapshot is one stored disk image.
//
// Note what is not here: the instance it came from. Vultr's snapshot carries
// no such field, so "the snapshots of this machine" is a question only guard
// can answer, and only about snapshots guard took — which is why the link is
// stored locally while everything on this struct is read live.
type Snapshot struct {
	ID             string    `json:"id"`
	Description    string    `json:"description"`
	Created        time.Time `json:"created"`
	SizeBytes      int64     `json:"size_bytes"`
	CompressedSize int64     `json:"compressed_size_bytes"`
	// Status is "pending" while the image is still being written, and
	// "complete" when it can be restored. Restoring a pending snapshot is a
	// thing the provider refuses, so the dashboard has to be able to say why.
	Status string `json:"status"`
	OSID   int    `json:"os_id,omitempty"`
	AppID  int    `json:"app_id,omitempty"`
}

type vultrSnapshot struct {
	ID             string `json:"id"`
	DateCreated    string `json:"date_created"`
	Description    string `json:"description"`
	Size           int64  `json:"size"`
	CompressedSize int64  `json:"compressed_size"`
	Status         string `json:"status"`
	OSID           int    `json:"os_id"`
	AppID          int    `json:"app_id"`
}

func (v vultrSnapshot) snapshot() Snapshot {
	created, err := time.Parse(time.RFC3339, v.DateCreated)
	if err != nil {
		created, _ = time.Parse("2006-01-02 15:04:05", v.DateCreated)
	}
	return Snapshot{
		ID: v.ID, Description: v.Description, Created: created,
		SizeBytes: v.Size, CompressedSize: v.CompressedSize,
		Status: v.Status, OSID: v.OSID, AppID: v.AppID,
	}
}

// Snapshots lists every snapshot on the account, newest first. Account-wide
// because that is the only listing the provider has; the cluster page filters
// it down to the ones it took.
func (c *Client) Snapshots(ctx context.Context, apiKey string) ([]Snapshot, error) {
	var out []Snapshot
	err := c.paged(ctx, apiKey, "/v2/snapshots", func(raw []byte) (string, error) {
		var page struct {
			Snapshots []vultrSnapshot `json:"snapshots"`
			nextCursor
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return "", fmt.Errorf("vultr answered something unreadable: %w", err)
		}
		for _, v := range page.Snapshots {
			out = append(out, v.snapshot())
		}
		return page.Meta.Links.Next, nil
	})
	if err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Created.After(out[b].Created) })
	return out, nil
}

// CreateSnapshot takes an image of one instance. It returns as soon as the
// provider accepts the job: the snapshot exists immediately and is "pending"
// for as long as the disk takes, which for a real machine is minutes.
func (c *Client) CreateSnapshot(ctx context.Context, apiKey, instanceID, description string) (Snapshot, error) {
	var answer struct {
		Snapshot vultrSnapshot `json:"snapshot"`
	}
	body := map[string]string{"instance_id": instanceID, "description": description}
	if err := c.call(ctx, apiKey, http.MethodPost, c.base+"/v2/snapshots", body, &answer); err != nil {
		return Snapshot{}, err
	}
	return answer.Snapshot.snapshot(), nil
}

// UpdateSnapshot changes the provider-side description (the label Vultr
// displays). The instance snapshot API calls this an update rather than a
// rename and accepts no other editable snapshot fields.
func (c *Client) UpdateSnapshot(ctx context.Context, apiKey, snapshotID, description string) error {
	address := c.base + "/v2/snapshots/" + url.PathEscape(snapshotID)
	body := map[string]string{"description": description}
	return c.call(ctx, apiKey, http.MethodPatch, address, body, nil)
}

// DeleteSnapshot forgets one image.
func (c *Client) DeleteSnapshot(ctx context.Context, apiKey, snapshotID string) error {
	address := c.base + "/v2/snapshots/" + url.PathEscape(snapshotID)
	return c.vultr(ctx, apiKey, http.MethodDelete, address, nil)
}

// Restore writes a snapshot back over one instance's disk.
//
// This is the destructive one on this file. Everything on the machine that is
// not in the image is gone, the instance reboots into the restored disk, and
// there is no undo beyond another snapshot. The endpoint above it is gated
// accordingly; the client itself just does as it is told.
func (c *Client) Restore(ctx context.Context, apiKey, instanceID, snapshotID string) error {
	address := c.base + "/v2/instances/" + url.PathEscape(instanceID) + "/restore"
	body := map[string]string{"snapshot_id": snapshotID}
	return c.call(ctx, apiKey, http.MethodPost, address, body, nil)
}

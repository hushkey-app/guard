// Package monitors is the rules over what the cluster page already measures.
//
// A rule is which number, which way, how far, for how long, and where to say
// so. Every metric it can watch is one guard already polls for the card — the
// health check, its latency, the day's uptime share, and what the machine says
// about its own CPU, memory and disk — so a rule adds a comparison and a POST,
// never a new source of truth.
package monitors

import (
	"time"

	"github.com/hushkey-app/guard/internal/telemetry/model"
	"github.com/hushkey-app/guard/server/apis/store"
	"github.com/mirairoad/howl-go/core/api"
)

// Catalogue is the rules and the vocabulary to read them with.
//
// Both in one answer, because a page that draws a rule editor needs the metric
// list to label a threshold box, and a second request for eight constants is a
// second request for eight constants.
type Catalogue struct {
	Monitors []model.Monitor    `json:"monitors"`
	Metrics  []model.MetricUnit `json:"metrics"`
	Webhooks []model.Webhook    `json:"webhooks"`
	// Categories is the order to draw the headings in, and Jobs and Views are
	// what the other two watchers put their rules under. They are listed here
	// even though this endpoint does not own them, because the page's job is to
	// answer "what is being watched" — a rules list that silently omitted the
	// backup alerts and the panel alerts would be a rules list somebody trusts
	// and should not.
	Categories []string      `json:"categories"`
	Jobs       []WatchedJob  `json:"jobs"`
	Views      []WatchedView `json:"views"`
}

// WatchedJob is a stored command with a staleness budget: the rule lives on the
// command, and is edited where the command is.
type WatchedJob struct {
	NodeID    int64  `json:"node_id"`
	Node      string `json:"node"`
	ActionID  int64  `json:"action_id"`
	Action    string `json:"action"`
	Schedule  string `json:"schedule,omitempty"`
	AfterSecs int    `json:"stale_after_seconds"`
	WebhookID int64  `json:"webhook_id,omitempty"`
	Firing    bool   `json:"firing"`
	LastOK    string `json:"last_ok,omitempty"`
}

// WatchedView is a saved panel with a line drawn across it, edited in the
// builder drawer where the query is.
type WatchedView struct {
	ViewID    int64   `json:"view_id"`
	View      string  `json:"view"`
	Op        string  `json:"op"`
	Threshold float64 `json:"threshold"`
	WebhookID int64   `json:"webhook_id"`
	Firing    bool    `json:"firing"`
	Value     float64 `json:"value"`
}

var List = api.Define(api.Spec[api.None, api.None, Catalogue]{
	Name:  "Cluster Monitors",
	Roles: []string{"admin"},
	Handler: func(r *api.Request[api.None, api.None]) (Catalogue, error) {
		monitors, err := store.Get().Monitors()
		if err != nil {
			return Catalogue{}, err
		}
		hooks, err := store.Get().Webhooks()
		if err != nil {
			return Catalogue{}, err
		}
		// The other two watchers' rules, read-only: they are edited where they
		// were written — a backup's budget beside the command, a panel's line
		// beside its query — and a second editor for them here would be a
		// second place for the two to disagree.
		nodes, err := store.Get().Nodes()
		if err != nil {
			return Catalogue{}, err
		}
		jobs := []WatchedJob{}
		for _, node := range nodes {
			for _, action := range node.Actions {
				if action.StaleAfterSeconds <= 0 {
					continue
				}
				stale, _ := action.Stale(time.Now())
				job := WatchedJob{
					NodeID: node.ID, Node: node.Name, ActionID: action.ID, Action: action.Name,
					Schedule: action.Schedule, AfterSecs: action.StaleAfterSeconds,
					WebhookID: action.WebhookID, Firing: stale,
				}
				if !action.LastOKAt.IsZero() {
					job.LastOK = action.LastOKAt.Format(time.RFC3339)
				}
				jobs = append(jobs, job)
			}
		}
		watchedViews, err := store.Get().WatchedViews()
		if err != nil {
			return Catalogue{}, err
		}
		views := []WatchedView{}
		for _, view := range watchedViews {
			views = append(views, WatchedView{
				ViewID: view.ID, View: view.Name, Op: view.Alert.Op,
				Threshold: view.Alert.Threshold, WebhookID: view.Alert.WebhookID,
				Firing: view.Alert.Firing, Value: view.Alert.Value,
			})
		}
		return Catalogue{
			Monitors: monitors, Metrics: model.MonitorMetrics, Webhooks: hooks,
			Categories: model.MonitorCategories, Jobs: jobs, Views: views,
		}, nil
	},
})

package telemetry

// Which services run on which machine.
//
// Guard is told two things independently: telemetry arrives carrying a service
// name, and someone types a health URL into settings. Nothing connects them —
// until you notice that the telemetry usually says where it was served from.
// A span with url.full=http://vps-1:8000/api/health was answered by whatever
// listens at that host, and the node watching that host is the machine it ran
// on.
//
// So the join is on hosts, taken from the attributes the OpenTelemetry
// conventions put them in. Never on names: two services called "api" on two
// boxes are two different things, and assuming otherwise would file half a
// cluster under the wrong machine.

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mirairoad/guard/internal/telemetry/model"
)

// hostAttributes are where a host can be found, best evidence first.
//
// url.full is last because it is the least direct: on a client span it is the
// host that was *called*, not the host that ran. It is also the only one many
// SDKs emit, which is why it is here at all — see hostsFor.
var hostAttributes = []string{
	"host.name",
	"server.address",
	"net.host.name",
	"net.peer.name",
	"url.full",
}

// topologyTTL is how long a computed map is reused. The dashboard asks every
// three seconds; which service runs on which box changes on the timescale of a
// deploy.
const topologyTTL = 30 * time.Second

type topologyCache struct {
	mu       sync.Mutex
	at       time.Time
	topology model.ClusterTopology
}

// ClusterTopology groups the known instances under the nodes their telemetry
// points at, and reports the rest as unassigned.
func (s *Store) ClusterTopology() (model.ClusterTopology, error) {
	s.topology.mu.Lock()
	defer s.topology.mu.Unlock()
	if time.Since(s.topology.at) < topologyTTL {
		return s.topology.topology, nil
	}

	nodes, err := s.Nodes()
	if err != nil {
		return model.ClusterTopology{}, err
	}
	summary, err := s.Snapshot()
	if err != nil {
		return model.ClusterTopology{}, err
	}

	// Two rounds of matching, strict then loose.
	//
	// Exact is the host as the node URL writes it — localhost:8000. Loose
	// drops the port. Both are needed: a node URL without a port and telemetry
	// that records one describe the same machine. But loose alone would file
	// everything on localhost:9000 under the node watching localhost:8000, so
	// every instance gets its chance at an exact match before any node is
	// offered a fuzzy one.
	exact := make([]map[string]bool, len(nodes))
	loose := make([]map[string]bool, len(nodes))
	for i, node := range nodes {
		exact[i], loose[i] = hostsOfURL(node.URL)
	}

	topology := model.ClusterTopology{
		Groups:     make([]model.ClusterGroup, 0, len(nodes)),
		Unassigned: make([]model.Instance, 0),
	}
	grouped := make([][]model.Instance, len(nodes))
	matched := make([]map[string]bool, len(nodes))
	for i := range matched {
		matched[i] = map[string]bool{}
	}

	seen := make([]map[string]bool, 0, len(summary.Instances))
	for _, instance := range summary.Instances {
		hosts, err := s.hostsFor(instance.Service, instance.Instance)
		if err != nil {
			return model.ClusterTopology{}, err
		}
		seen = append(seen, hosts)
	}

	place := func(index int, candidates []map[string]bool) bool {
		for i := range nodes {
			for host := range seen[index] {
				if candidates[i][host] {
					grouped[i] = append(grouped[i], summary.Instances[index])
					matched[i][host] = true
					return true
				}
			}
		}
		return false
	}

	placed := make([]bool, len(summary.Instances))
	for index := range summary.Instances {
		placed[index] = place(index, exact)
	}
	for index := range summary.Instances {
		if !placed[index] {
			placed[index] = place(index, loose)
		}
	}
	for index, done := range placed {
		if !done {
			topology.Unassigned = append(topology.Unassigned, summary.Instances[index])
		}
	}

	for i := range nodes {
		node := nodes[i]
		hosts := make([]string, 0, len(matched[i]))
		for host := range matched[i] {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		if grouped[i] == nil {
			grouped[i] = []model.Instance{}
		}
		topology.Groups = append(topology.Groups, model.ClusterGroup{
			Node: &node, Instances: grouped[i], Hosts: hosts,
		})
	}

	s.topology.at = time.Now()
	s.topology.topology = topology
	return topology, nil
}

// hostsFor is every host mentioned by one instance's recent telemetry.
//
// A sample rather than a scan: an instance that has served a host in its last
// few hundred events is an instance running there, and reading a million rows
// to find the same answer would make this the most expensive thing on the page.
func (s *Store) hostsFor(service, instance string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT attributes_json FROM events
WHERE service = ? AND instance = ? AND json_type(attributes_json) = 'object'
ORDER BY id DESC LIMIT 300`, service, instance)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	hosts := map[string]bool{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var attributes map[string]any
		if err := json.Unmarshal([]byte(raw), &attributes); err != nil {
			continue
		}
		for _, key := range hostAttributes {
			value, ok := attributes[key].(string)
			if !ok || value == "" {
				continue
			}
			for host := range hostsOf(key, value) {
				hosts[host] = true
			}
		}
	}
	return hosts, rows.Err()
}

// hostsOf normalises one attribute into every host form it could match on —
// the instance side does not distinguish strict from loose, because a span
// recording vps-1:8000 should match a node written either way.
func hostsOf(key, value string) map[string]bool {
	strict, loose := hostsOfURL(value)
	if key != "url.full" {
		strict, loose = hostForms(strings.TrimSpace(value))
	}
	for host := range loose {
		strict[host] = true
	}
	return strict
}

// hostsOfURL returns the host exactly as the URL writes it, and the same host
// without its port.
func hostsOfURL(raw string) (exact, loose map[string]bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return map[string]bool{}, map[string]bool{}
	}
	return hostForms(parsed.Host)
}

func hostForms(host string) (exact, loose map[string]bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "" {
		return map[string]bool{}, map[string]bool{}
	}
	exact = map[string]bool{host: true}
	loose = map[string]bool{}
	// An IPv6 literal is [::1]:8000; the colons inside the brackets are part of
	// the address, not a port separator.
	if index := strings.LastIndex(host, ":"); index > 0 && !strings.Contains(host[index:], "]") {
		loose[host[:index]] = true
	}
	return exact, loose
}

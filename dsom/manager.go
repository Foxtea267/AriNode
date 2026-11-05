package dsom

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Foxtea267/AriNode/common/nodepolicy"
	log "github.com/sirupsen/logrus"
)

type Address struct {
	Host string   `json:"host,omitempty"`
	Port int      `json:"port,omitempty"`
	IPs  []string `json:"ips,omitempty"`
}
type Change struct {
	At  time.Time `json:"at"`
	Via string    `json:"via"`
	IPs []string  `json:"ips"`
}
type Status struct {
	Tag             string         `json:"tag"`
	Mode            string         `json:"mode"`
	Active          string         `json:"active"`
	ActiveAddress   Address        `json:"active_address"`
	Gateway         Address        `json:"gateway"`
	ExitIPs         []string       `json:"exit_ips"`
	ExitConfirmedAt time.Time      `json:"exit_confirmed_at,omitempty"`
	ExitFresh       bool           `json:"exit_fresh"`
	Metadata        map[string]Geo `json:"metadata,omitempty"`
	DirectState     string         `json:"direct_state"`
	DirectReason    string         `json:"direct_reason,omitempty"`
	LastCheck       time.Time      `json:"last_check,omitempty"`
	LastSwitch      time.Time      `json:"last_switch,omitempty"`
	LastFailure     string         `json:"last_failure,omitempty"`
	ProbeLatencyMS  int64          `json:"probe_latency_ms"`
	History         []Change       `json:"history,omitempty"`
}

type selection struct {
	name     string
	endpoint endpoint
	timeout  int
	dnsTTL   int
}

func identity(e endpoint) string {
	hash := sha256.Sum256([]byte(fmt.Sprintf("%q:%d:%q:%q", e.Server, e.Port, e.username, e.password)))
	return hex.EncodeToString(hash[:])
}

type health struct {
	result              probeResult
	successes, failures int
	verified            bool
	verifiedAt          time.Time
}
type candidate struct {
	name     string
	endpoint endpoint
}
type geoCache struct {
	value   Geo
	expires time.Time
}

type worker struct {
	mu            sync.Mutex
	checkMu       sync.Mutex
	config        Config
	generation    uint64
	status        Status
	health        map[string]*health
	geo           map[string]geoCache
	selected      atomic.Pointer[selection]
	dns           resolver
	limiter       chan struct{}
	ctx           context.Context
	cancel        context.CancelFunc
	probeCancel   context.CancelFunc
	wake          chan struct{}
	wg            sync.WaitGroup
	listener      net.Listener
	token         string
	historyPath   string
	connectionsMu sync.Mutex
	connections   map[net.Conn]bool
	closed        bool
}

type Manager struct {
	mu      sync.RWMutex
	workers map[string]*worker
	root    string
	limiter chan struct{}
	closed  bool
}

func New(root string) *Manager {
	if root == "" {
		root = "/var/lib/arinode/dsom"
	}
	return &Manager{root: root, workers: map[string]*worker{}, limiter: make(chan struct{}, 4)}
}

// Reconcile retains relay addresses, active tunnels and health state for existing
// tags. New listeners are prepared before publishing a changed configuration.
func (m *Manager) Reconcile(configs []Config) error {
	if err := Validate(configs); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("DSOM: manager is stopped")
	}
	next := map[string]*worker{}
	var created []*worker
	for _, input := range configs {
		c, _ := normalize(input)
		if previous := m.workers[c.Tag]; previous != nil {
			next[c.Tag] = previous
			continue
		}
		w, err := newWorker(c, m.root, m.limiter)
		if err != nil {
			for _, item := range created {
				item.close()
			}
			return err
		}
		next[c.Tag] = w
		created = append(created, w)
	}
	for _, input := range configs {
		c, _ := normalize(input)
		next[c.Tag].update(c)
	}
	old := m.workers
	m.workers = next
	for _, w := range created {
		w.wg.Add(1)
		go w.run()
	}
	for tag, w := range old {
		if next[tag] == nil {
			w.close()
		}
	}
	return nil
}

func (m *Manager) Outbounds() []nodepolicy.OutboundConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	tags := make([]string, 0, len(m.workers))
	for tag := range m.workers {
		tags = append(tags, tag)
	}
	sort.Strings(tags)
	result := make([]nodepolicy.OutboundConfig, 0, len(tags))
	for _, tag := range tags {
		w := m.workers[tag]
		port := w.listener.Addr().(*net.TCPAddr).Port
		result = append(result, nodepolicy.OutboundConfig{Tag: tag, Protocol: "socks", Settings: map[string]any{"servers": []any{map[string]any{"address": "127.0.0.1", "port": port, "users": []any{map[string]any{"user": "dsom", "pass": w.token}}}}}})
	}
	return result
}

func (m *Manager) Snapshot() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]Status, 0, len(m.workers))
	for _, w := range m.workers {
		w.mu.Lock()
		// Marshal only the public status: no config, auth, local token or raw error.
		data, _ := json.Marshal(w.status)
		w.mu.Unlock()
		var item Status
		_ = json.Unmarshal(data, &item)
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Tag < result[j].Tag })
	return result
}

func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	m.closed = true
	for _, w := range m.workers {
		w.close()
	}
}

func newWorker(c Config, root string, limiter chan struct{}) (*worker, error) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("DSOM: cannot create loopback relay")
	}
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		listener.Close()
		return nil, fmt.Errorf("DSOM: relay authentication initialization failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &worker{config: c, ctx: ctx, cancel: cancel, listener: listener, token: hex.EncodeToString(token[:]), wake: make(chan struct{}, 1), limiter: limiter,
		health: map[string]*health{}, geo: map[string]geoCache{}, connections: map[net.Conn]bool{}, historyPath: filepath.Join(root, c.Tag, "history.json"),
		status: Status{Tag: c.Tag, Mode: c.Mode, Active: "unavailable", DirectState: "DIRECT_UNAVAILABLE"}}
	w.status.History = loadHistory(w.historyPath)
	if c.Enabled && c.Mode != "direct" && c.Gateway != nil {
		if endpoint, err := resolve(*c.Gateway); err == nil {
			w.selected.Store(&selection{name: "gateway", endpoint: endpoint, timeout: c.HealthCheck.Timeout, dnsTTL: c.HealthCheck.DNSTTL})
			w.status.Active = "gateway"
			w.status.ActiveAddress = Address{Host: endpoint.Server, Port: endpoint.Port}
		}
	}
	w.wg.Add(1)
	go w.accept()
	return w, nil
}

func (w *worker) update(c Config) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if reflect.DeepEqual(w.config, c) {
		return
	}
	w.config = c
	w.generation++
	w.status.Mode = c.Mode
	old := w.selected.Load()
	if !c.Enabled || (c.Mode == "direct" && (!c.Direct.Enabled || old == nil || old.name != "direct")) {
		w.selected.Store(nil)
		w.status.Active = "unavailable"
		w.status.ActiveAddress = Address{}
		w.status.ExitIPs = nil
		w.status.ExitFresh = false
		w.status.ExitConfirmedAt = time.Time{}
		w.status.Metadata = nil
		if old != nil {
			w.status.LastSwitch = time.Now().UTC()
		}
	}
	if c.Enabled && c.Mode != "direct" && old != nil && old.name == "direct" && (c.Mode == "gateway" || !c.Direct.Enabled) {
		w.selected.Store(nil)
		w.status.Active = "unavailable"
		w.status.ActiveAddress = Address{}
		w.status.ExitIPs = nil
		w.status.ExitFresh = false
		w.status.ExitConfirmedAt = time.Time{}
		w.status.Metadata = nil
		if c.Gateway != nil {
			if e, err := resolve(*c.Gateway); err == nil {
				w.selected.Store(&selection{name: "gateway", endpoint: e, timeout: c.HealthCheck.Timeout, dnsTTL: c.HealthCheck.DNSTTL})
				w.status.Active = "gateway"
				w.status.ActiveAddress = Address{Host: e.Server, Port: e.Port}
			}
		}
		w.status.LastSwitch = time.Now().UTC()
	}
	if w.probeCancel != nil {
		w.probeCancel()
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) run() {
	defer w.wg.Done()
	for {
		select {
		case <-w.wake:
		default:
		}
		w.check(w.ctx)
		w.mu.Lock()
		interval := w.config.HealthCheck.Interval
		w.mu.Unlock()
		timer := time.NewTimer(time.Duration(interval) * time.Second)
		select {
		case <-w.ctx.Done():
			timer.Stop()
			return
		case <-w.wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

func (w *worker) check(parent context.Context) {
	w.checkMu.Lock()
	defer w.checkMu.Unlock()
	w.mu.Lock()
	c, generation := w.config, w.generation
	ctx, cancel := context.WithCancel(parent)
	w.probeCancel = cancel
	w.mu.Unlock()
	defer cancel()
	var candidates []candidate
	var failure string
	if c.Enabled {
		gateways := append([]Endpoint(nil), c.FallbackGateways...)
		if c.Gateway != nil {
			gateways = append([]Endpoint{*c.Gateway}, gateways...)
		}
		for i, value := range gateways {
			e, err := resolve(value)
			if err != nil {
				failure = "gateway_credentials_or_configuration_invalid"
				continue
			}
			name := "gateway"
			if i > 0 {
				name = "fallback-" + strconv.Itoa(i)
			}
			candidates = append(candidates, candidate{name: name, endpoint: e})
		}
	}
	directReason := "direct_address_not_configured"
	if c.Enabled && c.Mode != "gateway" && c.Direct.Enabled {
		value := c.Direct.Endpoint
		var err error
		if c.Direct.EndpointFile != "" {
			err = readPrivateJSON(c.Direct.EndpointFile, &value)
		}
		if err == nil && value.Server != "" {
			e, resolveErr := resolve(value)
			if resolveErr == nil {
				candidates = append(candidates, candidate{name: "direct", endpoint: e})
				directReason = "direct_not_yet_verified"
			} else {
				directReason = "direct_credentials_or_configuration_invalid"
			}
		} else if err != nil {
			directReason = "direct_endpoint_file_invalid"
		}
	}
	active := w.selected.Load()
	if active != nil && c.Enabled {
		found := false
		for _, item := range candidates {
			if identity(item.endpoint) == identity(active.endpoint) {
				found = true
			}
		}
		// Probe the last working address while validating a replacement. This is
		// rollback to an explicit, previously verified endpoint, never an exit IP.
		allowed := active.name != "direct" || (c.Mode != "gateway" && c.Direct.Enabled)
		if !found && allowed {
			candidates = append(candidates, candidate{name: "previous", endpoint: active.endpoint})
		}
	}
	unique := candidates[:0]
	seen := map[string]bool{}
	for _, item := range candidates {
		key := identity(item.endpoint)
		if !seen[key] {
			unique = append(unique, item)
			seen[key] = true
		} else if item.name == "direct" {
			directReason = "direct_endpoint_is_gateway"
		}
	}
	candidates = unique
	results := make([]probeResult, len(candidates))
	var wg sync.WaitGroup
	for i, item := range candidates {
		wg.Add(1)
		go func(i int, item candidate) {
			defer wg.Done()
			select {
			case w.limiter <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-w.limiter }()
			limit := time.Duration(c.HealthCheck.Timeout*(c.HealthCheck.Retries+1)+1) * time.Second
			probeCtx, done := context.WithTimeout(ctx, limit)
			defer done()
			results[i] = probe(probeCtx, item.endpoint, c.HealthCheck, &w.dns)
		}(i, item)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return
	}
	healthyPeer := false
	for i, item := range candidates {
		if item.name == "direct" {
			for _, gateway := range candidates {
				if gateway.name != "direct" && gateway.name != "previous" && sameAddress(item.endpoint, gateway.endpoint, &w.dns) {
					results[i] = probeResult{Outcome: "failed", Reason: "direct_endpoint_resolves_to_gateway"}
					break
				}
			}
		}
		if results[i].Outcome == "healthy" {
			healthyPeer = true
		}
	}
	// A working peer proves that the configured query services are available.
	// This distinguishes a broken forwarding path from a shared source outage.
	if healthyPeer {
		for i := range results {
			if results[i].Outcome == "unknown" && results[i].Reason == "probe_sources_unavailable" {
				results[i].Outcome = "failed"
				results[i].Reason = "proxy_forwarding_failed_compared_with_healthy_peer"
			}
		}
	}
	w.mu.Lock()
	if generation != w.generation {
		w.mu.Unlock()
		return
	}
	now := time.Now().UTC()
	w.status.LastCheck = now
	w.status.LastFailure = failure
	w.status.DirectReason = directReason
	w.status.DirectState = "DIRECT_UNAVAILABLE"
	for i, item := range candidates {
		key := identity(item.endpoint)
		value := w.health[key]
		if value == nil {
			value = &health{}
			w.health[key] = value
		}
		previousIPs := value.result.IPs
		value.result = results[i]
		if results[i].Outcome != "healthy" {
			value.result.IPs = previousIPs
		}
		switch results[i].Outcome {
		case "healthy":
			value.successes++
			value.failures = 0
			value.verified = true
			value.verifiedAt = now
		case "failed":
			value.failures++
			value.successes = 0
		default:
			value.successes = 0
			value.failures = 0
		}
		if item.name == "direct" {
			w.status.DirectReason = results[i].Reason
			if results[i].Outcome == "healthy" && value.successes >= c.HealthCheck.SuccessThreshold {
				w.status.DirectState = "VERIFIED"
			} else if results[i].Outcome == "healthy" {
				w.status.DirectReason = "direct_success_threshold_pending"
			}
		}
	}
	if len(w.health) > 128 {
		for key := range w.health {
			if !seen[key] {
				delete(w.health, key)
			}
		}
	}
	w.choose(c, candidates, now)
	chosen := w.selected.Load()
	var geoEndpoint endpoint
	var ips []string
	if chosen != nil {
		w.status.Active = chosen.name
		w.status.ActiveAddress = Address{Host: chosen.endpoint.Server, Port: chosen.endpoint.Port, IPs: w.dns.peek(chosen.endpoint.Server)}
		if health := w.health[identity(chosen.endpoint)]; health != nil {
			w.status.ExitIPs = append([]string(nil), health.result.IPs...)
			w.status.ExitConfirmedAt = health.verifiedAt
			w.status.ExitFresh = health.result.Outcome == "healthy"
			w.status.ProbeLatencyMS = health.result.LatencyMS
			if health.result.Reason != "" {
				w.status.LastFailure = health.result.Reason
			}
			if health.result.Outcome == "healthy" {
				ips = append([]string(nil), health.result.IPs...)
				geoEndpoint = chosen.endpoint
			}
		}
	} else {
		w.status.Active = "unavailable"
		w.status.ActiveAddress = Address{}
		w.status.ExitIPs = nil
		w.status.ExitConfirmedAt = time.Time{}
		w.status.ExitFresh = false
	}
	if c.Gateway != nil {
		w.status.Gateway = Address{Host: c.Gateway.Server, Port: c.Gateway.Port, IPs: w.dns.peek(c.Gateway.Server)}
	}
	changed := false
	if len(ips) > 0 {
		history := w.status.History
		if len(history) == 0 || !reflect.DeepEqual(history[len(history)-1].IPs, ips) || history[len(history)-1].Via != w.status.Active {
			w.status.History = append(history, Change{At: now, Via: w.status.Active, IPs: append([]string(nil), ips...)})
			if len(w.status.History) > 100 {
				w.status.History = w.status.History[len(w.status.History)-100:]
			}
			changed = true
		}
	}
	history := append([]Change(nil), w.status.History...)
	metadata := map[string]Geo{}
	for _, ip := range w.status.ExitIPs {
		if value, ok := w.status.Metadata[ip]; ok {
			metadata[ip] = value
		}
	}
	w.mu.Unlock()
	if changed {
		if err := saveHistory(w.historyPath, history); err != nil {
			log.WithField("outbound", c.Tag).Warn("DSOM history persistence failed")
		}
		log.WithFields(log.Fields{"outbound": c.Tag, "exit_ips": ips}).Info("DSOM confirmed exit observation changed")
	}
	for _, ip := range ips {
		key := c.Metadata.URL + "|" + ip
		cached := w.geo[key]
		if !time.Now().Before(cached.expires) {
			value := geolocate(ctx, geoEndpoint, ip, c, &w.dns)
			ttl := 24 * time.Hour
			if value.Source == "" {
				ttl = time.Hour
			}
			cached = geoCache{value: value, expires: time.Now().Add(ttl)}
			w.geo[key] = cached
		}
		metadata[ip] = cached.value
	}
	if len(w.geo) > 256 {
		for key := range w.geo {
			delete(w.geo, key)
			if len(w.geo) <= 256 {
				break
			}
		}
	}
	w.mu.Lock()
	if generation == w.generation {
		w.status.Metadata = metadata
	}
	w.mu.Unlock()
}

func sameAddress(a, b endpoint, dns *resolver) bool {
	if a.Port != b.Port {
		return false
	}
	for _, left := range dns.peek(a.Server) {
		for _, right := range dns.peek(b.Server) {
			if left == right {
				return true
			}
		}
	}
	return false
}

// All decisions use explicit endpoints plus protocol/forwarding results. Exit
// address changes alone cannot change the selected endpoint.
func (w *worker) choose(c Config, candidates []candidate, now time.Time) {
	old := w.selected.Load()
	ready := func(item candidate) bool {
		h := w.health[identity(item.endpoint)]
		return h != nil && h.verified && h.failures == 0 && h.successes >= c.HealthCheck.SuccessThreshold
	}
	failed := func(selected *selection) bool {
		if selected == nil {
			return true
		}
		if selected.name == "direct" {
			for _, item := range candidates {
				if item.name != "direct" && item.name != "previous" && sameAddress(selected.endpoint, item.endpoint, &w.dns) {
					return true
				}
			}
		}
		h := w.health[identity(selected.endpoint)]
		return h != nil && h.failures >= c.HealthCheck.FailureThreshold
	}
	cooled := w.status.LastSwitch.IsZero() || now.Sub(w.status.LastSwitch) >= time.Duration(c.HealthCheck.Cooldown)*time.Second
	var direct, gateway *candidate
	for i, item := range candidates {
		if item.name == "direct" && ready(item) {
			direct = &candidates[i]
		}
		if item.name != "direct" && item.name != "previous" && ready(item) && gateway == nil {
			gateway = &candidates[i]
		}
	}
	var next *selection
	makeSelection := func(item *candidate) *selection {
		if item == nil {
			return nil
		}
		return &selection{name: item.name, endpoint: item.endpoint, timeout: c.HealthCheck.Timeout, dnsTTL: c.HealthCheck.DNSTTL}
	}
	switch {
	case !c.Enabled:
		next = nil
	case c.Mode == "direct":
		if direct != nil && (old == nil || cooled || identity(old.endpoint) == identity(direct.endpoint)) {
			next = makeSelection(direct)
		} else if old != nil && old.name == "direct" && c.Direct.Enabled && !failed(old) {
			next = old
		}
	case c.Mode == "auto" && direct != nil && (cooled || (old != nil && old.name == "direct" && identity(old.endpoint) == identity(direct.endpoint))):
		next = makeSelection(direct)
	case c.Mode == "auto" && old != nil && old.name == "direct" && c.Direct.Enabled && !failed(old):
		next = old
	default:
		if old != nil && old.name != "direct" && !failed(old) {
			next = old
		}
		if gateway != nil && (next == nil || failed(old) || cooled) {
			next = makeSelection(gateway)
		}
		if next == nil {
			for _, item := range candidates {
				if item.name == "gateway" {
					next = makeSelection(&item)
					break
				}
			}
		}
	}
	if next != nil {
		next = &selection{name: next.name, endpoint: next.endpoint, timeout: c.HealthCheck.Timeout, dnsTTL: c.HealthCheck.DNSTTL}
	}
	if (old == nil) != (next == nil) || (old != nil && next != nil && (old.name != next.name || identity(old.endpoint) != identity(next.endpoint))) {
		if old != nil {
			w.status.LastSwitch = now
		}
		active := "unavailable"
		if next != nil {
			active = next.name
		}
		log.WithFields(log.Fields{"outbound": c.Tag, "active": active}).Info("DSOM upstream switched for new connections")
	}
	w.selected.Store(next)
}

func (w *worker) close() {
	w.cancel()
	w.listener.Close()
	w.connectionsMu.Lock()
	w.closed = true
	for connection := range w.connections {
		connection.Close()
	}
	w.connectionsMu.Unlock()
	w.wg.Wait()
	w.mu.Lock()
	w.status.Active = "stopped"
	w.mu.Unlock()
}

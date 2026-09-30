package komari

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/Foxtea267/ariNode/common/sysstatus"
	"github.com/Foxtea267/ariNode/conf"
	log "github.com/sirupsen/logrus"
)

type Status struct {
	Name       string    `json:"name"`
	Endpoint   string    `json:"endpoint"`
	State      string    `json:"state"`
	LastReport time.Time `json:"last_report,omitempty"`
}

type worker struct {
	config      conf.KomariBinding
	stop        context.CancelFunc
	done        chan struct{}
	mu          sync.RWMutex
	status      Status
	client      *http.Client
	lastNetwork networkCounters
	lastSample  time.Time
}

type Manager struct {
	mu      sync.RWMutex
	workers map[string]*worker
}

var readStatus = sysstatus.Read

func New() *Manager { return &Manager{workers: make(map[string]*worker)} }

func (m *Manager) Reconcile(bindings []conf.KomariBinding) {
	m.mu.Lock()
	keep := make(map[string]*worker, len(bindings))
	var removed []*worker
	for _, binding := range bindings {
		if existing, ok := m.workers[binding.Name]; ok && reflect.DeepEqual(existing.config, binding) {
			keep[binding.Name] = existing
			continue
		}
		if existing, ok := m.workers[binding.Name]; ok {
			existing.stop()
			removed = append(removed, existing)
		}
		ctx, cancel := context.WithCancel(context.Background())
		w := &worker{config: binding, stop: cancel, done: make(chan struct{}), client: &http.Client{Timeout: 10 * time.Second}, status: Status{Name: binding.Name, Endpoint: binding.Endpoint, State: "connecting"}}
		keep[binding.Name] = w
		go w.run(ctx)
	}
	for name, existing := range m.workers {
		if _, ok := keep[name]; !ok {
			existing.stop()
			removed = append(removed, existing)
		}
	}
	m.workers = keep
	m.mu.Unlock()
	for _, old := range removed {
		<-old.done
	}
}

func (m *Manager) Snapshot() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.workers))
	for _, w := range m.workers {
		w.mu.RLock()
		out = append(out, w.status)
		w.mu.RUnlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (m *Manager) Close() {
	m.mu.Lock()
	workers := m.workers
	m.workers = make(map[string]*worker)
	for _, w := range workers {
		w.stop()
	}
	m.mu.Unlock()
	for _, w := range workers {
		<-w.done
	}
}

func (w *worker) run(ctx context.Context) {
	defer close(w.done)
	interval := time.Duration(w.config.Interval) * time.Second
	if interval < 5*time.Second {
		interval = 6 * time.Second
	}
	reportTicker := time.NewTicker(interval)
	defer reportTicker.Stop()
	infoTicker := time.NewTicker(10 * time.Minute)
	defer infoTicker.Stop()
	w.sendBasicInfo(ctx)
	w.sendReport(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-infoTicker.C:
			w.sendBasicInfo(ctx)
		case <-reportTicker.C:
			w.sendReport(ctx)
		}
	}
}

func (w *worker) sendBasicInfo(ctx context.Context) {
	status, err := readStatus()
	if err != nil {
		w.failed("host metrics unavailable")
		return
	}
	info := map[string]any{
		"arch": runtime.GOARCH, "cpu_cores": runtime.NumCPU(), "cpu_physical_cores": 0,
		"cpu_name": "", "disk_total": status.Disk.Total, "gpu_name": "",
		"ipv4": "", "ipv6": "", "mem_total": status.Mem.Total, "os": runtime.GOOS,
		"kernel_version": kernelVersion(), "swap_total": status.Swap.Total,
		"version": "AriNode", "virtualization": "",
	}
	if err := w.call(ctx, "agent.basicInfo", map[string]any{"info": info}); err != nil {
		w.failed("basic info report failed")
	}
}

func (w *worker) sendReport(ctx context.Context) {
	status, err := readStatus()
	if err != nil {
		w.failed("host metrics unavailable")
		return
	}
	load, uptime, processes, network := hostExtras()
	now := time.Now()
	up, down := int64(0), int64(0)
	if !w.lastSample.IsZero() {
		seconds := now.Sub(w.lastSample).Seconds()
		if seconds > 0 {
			if network.up >= w.lastNetwork.up {
				up = int64(float64(network.up-w.lastNetwork.up) / seconds)
			}
			if network.down >= w.lastNetwork.down {
				down = int64(float64(network.down-w.lastNetwork.down) / seconds)
			}
		}
	}
	w.lastNetwork, w.lastSample = network, now
	report := map[string]any{
		"cpu": map[string]any{"usage": status.CPU},
		"ram": status.Mem, "swap": status.Swap,
		"load":        map[string]any{"load1": load[0], "load5": load[1], "load15": load[2]},
		"disk":        status.Disk,
		"network":     map[string]any{"up": up, "down": down, "totalUp": network.up, "totalDown": network.down},
		"connections": map[string]int{"tcp": 0, "udp": 0},
		"uptime":      uptime, "process": processes, "message": "",
	}
	if err := w.call(ctx, "agent.report", map[string]any{"report": report}); err != nil {
		w.failed("status report failed")
		return
	}
	w.mu.Lock()
	w.status.State = "reporting"
	w.status.LastReport = now.UTC()
	w.mu.Unlock()
}

func (w *worker) failed(reason string) {
	w.mu.Lock()
	w.status.State = "retrying"
	w.mu.Unlock()
	log.WithFields(log.Fields{"komari": w.config.Name, "reason": reason}).Warn("Komari binding unavailable; other bindings continue")
}

func (w *worker) call(ctx context.Context, method string, params any) error {
	endpoint, err := url.Parse(w.config.Endpoint + "/api/clients/v2/rpc")
	if err != nil {
		return err
	}
	query := endpoint.Query()
	query.Set("token", w.config.Token)
	endpoint.RawQuery = query.Encode()
	// This binding only reports metrics. A notification asks Komari not to
	// dispatch remote events that AriNode cannot acknowledge or execute.
	payload, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nil, "method": method, "params": params})
	if err != nil {
		return err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("Komari request failed")
	} // URL contains a token; never return it.
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Komari returned HTTP %d", resp.StatusCode)
	}
	var answer struct {
		Error *struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&answer); err != nil && err != io.EOF {
		return fmt.Errorf("Komari returned invalid RPC response")
	}
	if answer.Error != nil {
		return fmt.Errorf("Komari RPC error %d", answer.Error.Code)
	}
	return nil
}

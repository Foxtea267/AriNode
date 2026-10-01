package node

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	log "github.com/sirupsen/logrus"
)

const defaultRetryInterval = 30 * time.Second

type BindingStatus struct {
	Panel     string `json:"panel"`
	NodeID    int    `json:"id"`
	NodeType  string `json:"type"`
	MachineID int    `json:"machine_id,omitempty"`
	State     string `json:"state"`
	Core      string `json:"core,omitempty"`
	Port      int    `json:"port,omitempty"`
	Error     string `json:"error,omitempty"`
}

type managedNode struct {
	config     conf.NodeConfig
	controller *Controller
	status     BindingStatus
	runtimeTag string
}

type Node struct {
	mu            sync.Mutex
	entries       []*managedNode
	core          vCore.Core
	stop          chan struct{}
	done          chan struct{}
	retryInterval time.Duration
	snapshot      atomic.Value // []BindingStatus
}

func New() *Node {
	n := &Node{retryInterval: defaultRetryInterval}
	n.snapshot.Store([]BindingStatus{})
	return n
}

func bindingKey(c conf.NodeConfig) string {
	a := c.ApiConfig
	return fmt.Sprintf("%s|%d|%s|%d", strings.TrimRight(a.APIHost, "/"), a.NodeID, a.NodeType, a.MachineID)
}

func machineKey(c conf.NodeConfig) string {
	a := c.ApiConfig
	return fmt.Sprintf("%s|%d|%s", strings.TrimRight(a.APIHost, "/"), a.MachineID, a.Key)
}

func newManaged(c conf.NodeConfig) *managedNode {
	a := c.ApiConfig
	return &managedNode{config: c, status: BindingStatus{Panel: a.APIHost, NodeID: a.NodeID, NodeType: a.NodeType, MachineID: a.MachineID, State: "pending"}}
}

// Start attempts every binding. A failed binding stays pending for automatic retry.
func (n *Node) Start(nodes []conf.NodeConfig, core vCore.Core) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no node bindings configured")
	}
	if err := validateBindings(nodes); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stop != nil {
		return fmt.Errorf("node manager is already running")
	}
	n.core = core
	n.entries = make([]*managedNode, 0, len(nodes))
	for _, c := range nodes {
		n.entries = append(n.entries, newManaged(c))
	}
	n.tryPendingLocked()
	n.stop = make(chan struct{})
	n.done = make(chan struct{})
	interval := n.retryInterval
	if interval <= 0 {
		interval = defaultRetryInterval
	}
	go n.retryLoop(interval, n.stop, n.done)
	return nil
}

func (n *Node) retryLoop(interval time.Duration, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			n.mu.Lock()
			n.tryPendingLocked()
			n.mu.Unlock()
		}
	}
}

func (n *Node) tryPendingLocked() {
	primaries := map[string]bool{}
	for _, entry := range n.entries {
		if entry.controller != nil && entry.config.ApiConfig.MachineID > 0 {
			key := machineKey(entry.config)
			entry.controller.machinePrimary.Store(!primaries[key])
			primaries[key] = true
		}
	}
	for _, entry := range n.entries {
		if entry.controller != nil {
			continue
		}
		c := entry.config // panel.New normalizes NodeType; keep the stored config unchanged.
		p, err := panel.New(&c.ApiConfig)
		if err != nil {
			n.failLocked(entry, err)
			continue
		}
		controller := NewController(n.core, p, &c.Options)
		key := machineKey(c)
		controller.machinePrimary.Store(c.ApiConfig.MachineID > 0 && !primaries[key])
		if err := startController(controller); err != nil {
			if closeErr := closeController(controller); closeErr != nil {
				log.WithError(closeErr).Warn("Clean up failed node")
			}
			n.failLocked(entry, err)
			continue
		}
		entry.controller = controller
		entry.runtimeTag = controller.tag
		entry.status.State = "running"
		entry.status.Error = ""
		entry.status.Core = controller.Options.Core
		if reporter, ok := n.core.(interface{ NodeCore(string) string }); ok {
			entry.status.Core = reporter.NodeCore(controller.tag)
		}
		entry.status.Port = controller.info.Common.ServerPort
		if controller.machinePrimary.Load() {
			primaries[key] = true
		}
		log.WithFields(log.Fields{"panel": c.ApiConfig.APIHost, "node_id": c.ApiConfig.NodeID, "core": entry.status.Core, "port": entry.status.Port}).Info("Node started")
	}
	n.publishLocked()
}

func startController(c *Controller) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("node startup panic: %v", p)
		}
	}()
	return c.Start()
}

func closeController(c *Controller) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("node cleanup panic: %v", p)
		}
	}()
	return c.Close()
}

func (n *Node) failLocked(entry *managedNode, err error) {
	entry.status.State = "retrying"
	entry.status.Error = "node startup failed; see service logs"
	log.WithError(err).WithFields(log.Fields{"panel": entry.status.Panel, "node_id": entry.status.NodeID}).Warn("Node unavailable; other nodes remain running")
}

func (n *Node) publishLocked() {
	status := make([]BindingStatus, len(n.entries))
	for i, entry := range n.entries {
		status[i] = entry.status
		if reporter, ok := n.core.(interface{ NodeCore(string) string }); ok && entry.controller != nil {
			status[i].Core = reporter.NodeCore(entry.runtimeTag)
		}
	}
	n.snapshot.Store(status)
}

// Reconcile preserves unchanged bindings and only restarts added or changed ones.
func (n *Node) Reconcile(nodes []conf.NodeConfig) error {
	if len(nodes) == 0 {
		return fmt.Errorf("no node bindings configured")
	}
	if err := validateBindings(nodes); err != nil {
		return err
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stop == nil {
		return fmt.Errorf("node manager is not running")
	}
	old := make(map[string]*managedNode, len(n.entries))
	for _, entry := range n.entries {
		old[bindingKey(entry.config)] = entry
	}
	next := make([]*managedNode, 0, len(nodes))
	for _, c := range nodes {
		key := bindingKey(c)
		if entry, ok := old[key]; ok {
			delete(old, key)
			if reflect.DeepEqual(entry.config, c) {
				next = append(next, entry)
				continue
			}
			if entry.controller != nil {
				if err := closeController(entry.controller); err != nil {
					log.WithError(err).Warn("Close changed node failed")
				}
			}
		}
		next = append(next, newManaged(c))
	}
	for _, entry := range old {
		if entry.controller != nil {
			if err := closeController(entry.controller); err != nil {
				log.WithError(err).Warn("Close removed node failed")
			}
		}
	}
	n.entries = next
	n.tryPendingLocked()
	return nil
}

func validateBindings(nodes []conf.NodeConfig) error {
	seen := map[string]bool{}
	for _, c := range nodes {
		key := bindingKey(c)
		if seen[key] {
			return fmt.Errorf("duplicate node binding %s", key)
		}
		seen[key] = true
	}
	return nil
}

func (n *Node) Snapshot() []BindingStatus {
	value := n.snapshot.Load()
	if value == nil {
		return []BindingStatus{}
	}
	status := value.([]BindingStatus)
	return append([]BindingStatus(nil), status...)
}

func (n *Node) Close() {
	if n.stop != nil {
		close(n.stop)
		<-n.done
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, entry := range n.entries {
		if entry.controller != nil {
			if err := closeController(entry.controller); err != nil {
				log.WithError(err).Warn("Close node failed")
			}
		}
	}
	n.entries = nil
	n.core = nil
	n.stop = nil
	n.done = nil
	n.publishLocked()
}

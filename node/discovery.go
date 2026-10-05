package node

import (
	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	log "github.com/sirupsen/logrus"
)

// Machine bindings are authoritative only after a valid discovery response.
// A failed panel preserves that machine's existing bindings and healthy nodes.
func (n *Node) discoverBindingsLocked() []conf.NodeConfig {
	templates := map[string]conf.NodeConfig{}
	var machines []string
	var desired []conf.NodeConfig
	for _, c := range n.configured {
		if c.ApiConfig.Cluster != nil || c.ApiConfig.MachineID <= 0 || (c.ApiConfig.MachineAutoDiscover != nil && !*c.ApiConfig.MachineAutoDiscover) {
			desired = append(desired, c)
			continue
		}
		key := machineKey(c)
		if _, exists := templates[key]; !exists {
			templates[key] = c
			machines = append(machines, key)
		}
	}
	for _, key := range machines {
		template := templates[key]
		p, err := panel.New(&template.ApiConfig)
		var bindings []panel.MachineNode
		if err == nil {
			bindings, err = p.GetMachineNodes()
		}
		if err != nil {
			log.WithFields(log.Fields{"panel": template.ApiConfig.APIHost, "machine_id": template.ApiConfig.MachineID}).WithError(err).Warn("Machine discovery failed; preserving existing nodes")
			seen := map[string]bool{}
			for _, c := range n.configured {
				if c.ApiConfig.Cluster == nil && machineKey(c) == key {
					desired = append(desired, c)
					seen[bindingKey(c)] = true
				}
			}
			for _, entry := range n.entries {
				if entry.config.ApiConfig.Cluster == nil && machineKey(entry.config) == key && !seen[bindingKey(entry.config)] {
					desired = append(desired, entry.config)
				}
			}
			continue
		}
		for _, binding := range bindings {
			c := template
			cluster := false
			c.ApiConfig.NodeID, c.ApiConfig.NodeType = binding.ID, binding.Type
			c.Options.Name = ""
			for _, explicit := range n.configured {
				if machineKey(explicit) == key && explicit.ApiConfig.NodeID == binding.ID {
					cluster = explicit.ApiConfig.Cluster != nil
					c = explicit
					c.ApiConfig.NodeType = binding.Type
					break
				}
			}
			if cluster {
				continue // Explicit shared nodes were already added above.
			}
			desired = append(desired, c)
		}
	}
	return desired
}

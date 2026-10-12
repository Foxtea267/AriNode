package migrate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/Foxtea267/AriNode/common/json5"
	"github.com/Foxtea267/AriNode/conf"
)

type V2BXOptions struct {
	SourceDir   string
	WorkingDir  string
	Cluster     *conf.ClusterConfig
	DropCluster bool
}

// Preserve native Core, transport, certificate, route and limit settings. V2bX
// and AriNode share this configuration format; a lossy node-only copy is unsafe.
func FromV2BX(data []byte, opts V2BXOptions) (Result, error) {
	return convertV2BX(data, opts, false)
}
func ToV2BX(data []byte, opts V2BXOptions) (Result, error) {
	return convertV2BX(data, opts, true)
}
func readV2BXObject(data []byte) (map[string]any, error) {
	clean, err := io.ReadAll(json5.NewTrimNodeReader(bytes.NewReader(data)))
	if err != nil {
		return nil, fmt.Errorf("read node JSON: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(clean, &root); err != nil {
		return nil, fmt.Errorf("invalid node JSON (payload omitted)")
	}
	if root == nil {
		return nil, fmt.Errorf("configuration must be a JSON object")
	}
	return root, nil
}
func nodeObject(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok && m != nil
}
func stringValue(m map[string]any, key string) string { s, _ := m[key].(string); return s }

func expandV2BXNode(n map[string]any, base string, seen map[string]bool) (map[string]any, error) {
	if include := stringValue(n, "Include"); include != "" {
		if strings.Contains(include, "://") {
			return nil, fmt.Errorf("remote Include must be downloaded and reviewed before migration")
		}
		file := include
		if !filepath.IsAbs(file) {
			file = filepath.Join(base, file)
		}
		file, err := filepath.Abs(file)
		if err != nil {
			return nil, err
		}
		if len(seen) >= 16 || seen[file] {
			return nil, fmt.Errorf("Include cycle or nesting exceeds 16")
		}
		seen[file] = true
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read Include: %w", err)
		}
		n, err = readV2BXObject(data)
		if err != nil {
			return nil, err
		}
		return expandV2BXNode(n, filepath.Dir(file), seen)
	}
	// The nested schema has precedence over flat values, matching both loaders.
	for _, key := range []string{"ApiConfig", "Options"} {
		if nested, exists := n[key]; exists {
			m, ok := nodeObject(nested)
			if !ok {
				return nil, fmt.Errorf("%s must be an object", key)
			}
			for k, v := range m {
				n[k] = v
			}
			delete(n, key)
		}
	}
	return n, nil
}
func convertV2BX(data []byte, opts V2BXOptions, reverse bool) (Result, error) {
	if err := opts.Cluster.Validate(); err != nil {
		return Result{}, err
	}
	root, err := readV2BXObject(data)
	if err != nil {
		return Result{}, err
	}
	nodes, ok := root["Nodes"].([]any)
	if !ok || len(nodes) == 0 {
		return Result{}, fmt.Errorf("configuration requires a nonempty Nodes array")
	}
	cores, ok := root["Cores"].([]any)
	if !ok || len(cores) == 0 {
		return Result{}, fmt.Errorf("configuration requires a nonempty Cores array")
	}
	coreNames := map[string]string{}
	for i, value := range cores {
		core, ok := nodeObject(value)
		if !ok {
			return Result{}, fmt.Errorf("Cores[%d] must be an object", i)
		}
		typ, err := v2bxCore(stringValue(core, "Type"))
		if err != nil {
			return Result{}, fmt.Errorf("Cores[%d]: %w", i, err)
		}
		core["Type"] = typ
		if typ == "xray" && stringValue(core, "AssetPath") == "" {
			core["AssetPath"] = filepath.ToSlash(opts.SourceDir)
		}
		if name := stringValue(core, "Name"); name != "" {
			coreNames[name] = typ
		}
	}
	panels := map[string]map[string]any{}
	if values, ok := root["Panels"].([]any); ok {
		for _, value := range values {
			p, ok := nodeObject(value)
			if !ok || stringValue(p, "Name") == "" {
				return Result{}, fmt.Errorf("invalid named panel")
			}
			panels[stringValue(p, "Name")] = p
		}
	}
	var warnings []string
	if reverse {
		warnings = append(warnings, "Original V2bX does not apply AriNode panel custom routing/multiplex; verify the retained local Core routing before switching")
	}
	seen := map[string]bool{}
	for i, value := range nodes {
		n, ok := nodeObject(value)
		if !ok {
			return Result{}, fmt.Errorf("Nodes[%d] must be an object", i)
		}
		n, err = expandV2BXNode(n, opts.SourceDir, map[string]bool{})
		if err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		nodes[i] = n
		if name := stringValue(n, "Panel"); name != "" {
			p, ok := panels[name]
			if !ok {
				return Result{}, fmt.Errorf("Nodes[%d]: undefined Panel", i)
			}
			n["ApiHost"] = p["ApiHost"]
			key := stringValue(p, "ApiKey")
			if key == "" {
				key = os.Getenv(stringValue(p, "ApiKeyEnv"))
			}
			n["ApiKey"] = key
			if id, ok := p["MachineID"].(float64); ok && id > 0 {
				n["MachineID"] = id
			}
			delete(n, "Panel")
		}
		if err := validPanelURL(stringValue(n, "ApiHost")); err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		id, ok := n["NodeID"].(float64)
		if !ok || id <= 0 || id != float64(int(id)) || stringValue(n, "ApiKey") == "" {
			return Result{}, fmt.Errorf("Nodes[%d]: positive integer NodeID and ApiKey required", i)
		}
		typ, err := normalizeType(stringValue(n, "NodeType"))
		if err != nil {
			return Result{}, fmt.Errorf("Nodes[%d]: %w", i, err)
		}
		n["NodeType"] = typ
		identity := fmt.Sprintf("%s/%s/%d", strings.TrimRight(stringValue(n, "ApiHost"), "/"), typ, int(id))
		if seen[identity] {
			return Result{}, fmt.Errorf("Nodes[%d]: duplicate binding", i)
		}
		seen[identity] = true
		core := stringValue(n, "Core")
		if name := stringValue(n, "CoreName"); name != "" {
			var exists bool
			core, exists = coreNames[name]
			if !exists {
				return Result{}, fmt.Errorf("Nodes[%d]: undefined CoreName", i)
			}
		} else {
			if core == "" && len(cores) == 1 {
				core = stringValue(cores[0].(map[string]any), "Type")
			}
			if core == "" {
				return Result{}, fmt.Errorf("Nodes[%d]: Core is ambiguous", i)
			}
			core, err = v2bxCore(core)
			if err != nil {
				return Result{}, err
			}
			n["Core"] = core
		}
		for old, current := range map[string]string{"MinReportTraffic": "ReportMinTraffic", "TCPFastOpen": "EnableTFO", "SniffEnabled": "EnableSniff"} {
			if _, exists := n[current]; !exists {
				if v, exists := n[old]; exists {
					n[current] = v
				}
			}
			delete(n, old)
		}
		if reverse {
			if machine, ok := n["MachineID"].(float64); ok && machine > 0 {
				return Result{}, fmt.Errorf("Nodes[%d]: original V2bX has no machine authentication; configure a server token first", i)
			}
			if typ == "mieru" || typ == "anytls" {
				return Result{}, fmt.Errorf("Nodes[%d]: original V2bX does not support %s", i, typ)
			}
			if n["Cluster"] != nil {
				if !opts.DropCluster {
					return Result{}, fmt.Errorf("Nodes[%d]: V2bX cannot aggregate Cluster reports; use --drop-cluster to explicitly export without aggregation", i)
				}
				warnings = append(warnings, "V2bX export removes Cluster; shared-node device reporting will no longer be aggregated")
			}
			for _, key := range []string{"Cluster", "MachineID", "MachineAutoDiscover"} {
				delete(n, key)
			}
		} else if opts.Cluster != nil {
			n["Cluster"] = opts.Cluster
		}
	}
	if reverse {
		if root["Komari"] != nil {
			warnings = append(warnings, "Komari bindings are not supported by original V2bX")
		}
		delete(root, "Komari")
	}
	delete(root, "Panels")
	base := opts.WorkingDir
	if base == "" {
		base = opts.SourceDir
	}
	absoluteConfigPaths(root, base)
	encoded, err := json.MarshalIndent(root, "", "  ")
	return Result{Data: append(encoded, '\n'), Nodes: len(nodes), Warnings: warnings}, err
}

// Rewrite path prefixes throughout native Core JSON as well as application
// configuration; credentials and unrelated strings are retained verbatim.
func RebasePaths(data []byte, from, to string) ([]byte, error) {
	clean, err := io.ReadAll(json5.NewTrimNodeReader(bytes.NewReader(data)))
	if err != nil {
		return nil, err
	}
	var root any
	if err := json.Unmarshal(clean, &root); err != nil {
		return nil, fmt.Errorf("invalid JSON asset (payload omitted)")
	}
	from, to = strings.TrimRight(filepath.ToSlash(from), "/"), strings.TrimRight(filepath.ToSlash(to), "/")
	var visit func(any) any
	visit = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			for k, item := range v {
				switch strings.ToLower(k) {
				case "apikey", "apikeyenv", "token", "tokenenv", "password", "pass", "username", "uuid", "private_key", "privatekey", "secretkey", "secret_key":
					continue
				}
				v[k] = visit(item)
			}
		case []any:
			for i, item := range v {
				v[i] = visit(item)
			}
		case string:
			if v == from {
				return to
			}
			if strings.HasPrefix(v, from+"/") {
				return to + strings.TrimPrefix(v, from)
			}
		}
		return value
	}
	encoded, err := json.MarshalIndent(visit(root), "", "  ")
	return append(encoded, '\n'), err
}
func v2bxCore(value string) (string, error) {
	typ := conf.NormalizeCoreType(value)
	switch typ {
	case "sing", "xray", "hysteria2":
		return typ, nil
	}
	return "", fmt.Errorf("unsupported V2bX core %q", value)
}

func absoluteConfigPaths(value any, base string) {
	if base == "" {
		return
	}
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			absoluteConfigPaths(item, base)
		}
	case map[string]any:
		for key, item := range v {
			switch strings.ToLower(key) {
			case "originalpath", "assetpath", "dnsconfigpath", "routeconfigpath", "outboundconfigpath", "inboundconfigpath", "hysteria2configpath", "rulelistpath", "certfile", "keyfile", "accesspath", "errorpath", "output", "certificate_path", "key_path":
				if path, ok := item.(string); ok && path != "" && path != "stdout" && path != "stderr" && !strings.HasPrefix(path, "/") && !filepath.IsAbs(path) {
					v[key] = filepath.ToSlash(filepath.Join(base, path))
				}
			}
			absoluteConfigPaths(item, base)
		}
	}
}

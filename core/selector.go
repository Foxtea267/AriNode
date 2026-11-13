package core

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	log "github.com/sirupsen/logrus"
)

type Selector struct {
	cores        map[string]Core
	nodes        sync.Map
	fallbackMu   sync.Mutex
	fallbackXray Core
	fallbackSing Core
	closed       bool
}

func NewSelector(c []conf.CoreConfig) (Core, error) {
	cs := make(map[string]Core, len(c))
	var failures []error
	for _, t := range c {
		f, ok := cores[strings.ToLower(t.Type)]
		if !ok {
			failures = append(failures, errors.New("unknown core type: "+t.Type))
			continue
		}
		core1, err := createSafely(f, &t)
		if err != nil {
			failures = append(failures, fmt.Errorf("initialize core %s: %w", t.Type, err))
			continue
		}
		if t.Name == "" {
			cs[t.Type] = core1
		} else {
			cs[t.Name] = core1
		}
	}
	if len(cs) == 0 {
		return nil, errors.Join(failures...)
	}
	for _, err := range failures {
		log.WithError(err).Warn("Core unavailable; continuing with other cores")
	}
	return &Selector{
		cores: cs,
	}, nil
}

func (s *Selector) Start() error {
	var failures []error
	for name, configured := range s.cores {
		err := StartSafely(configured)
		if err != nil {
			failures = append(failures, fmt.Errorf("start core %s: %w", name, err))
			_ = CloseSafely(configured)
			delete(s.cores, name)
		}
	}
	if len(s.cores) == 0 {
		return errors.Join(failures...)
	}
	for _, err := range failures {
		log.WithError(err).Warn("Core unavailable; continuing with other cores")
	}
	return nil
}

func (s *Selector) Close() error {
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	s.closed = true
	var errs []error
	for i := range s.cores {
		if err := CloseSafely(s.cores[i]); err != nil {
			errs = append(errs, err)
		}
	}
	if s.fallbackXray != nil {
		if err := CloseSafely(s.fallbackXray); err != nil {
			errs = append(errs, err)
		}
	}
	if s.fallbackSing != nil {
		if err := CloseSafely(s.fallbackSing); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func isSupported(protocol string, protocols []string) bool {
	for i := range protocols {
		if protocol == protocols[i] {
			return true
		}
	}
	return false
}

func (s *Selector) AddNode(tag string, info *panel.NodeInfo, option *conf.Options) error {
	// Resolve per binding: switching a panel transport must not change its requested core.
	resolved := *option
	option = &resolved
	var core Core
	if option.CoreName == "" && option.Core == "" {
		if err := option.UseCore("sing"); err != nil {
			return err
		}
	}
	if len(option.CoreName) > 0 {
		// use name to select core
		if c, ok := s.cores[option.CoreName]; ok {
			core = c
		}
	} else {
		// Prefer the unnamed core of this type, then a stable named choice.
		core = s.cores[option.Core]
		if core == nil {
			names := make([]string, 0, len(s.cores))
			for name := range s.cores {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if c := s.cores[name]; c.Type() == option.Core {
					core = c
					break
				}
			}
		}
		if core == nil && option.ManagedPanel && (option.Core == "xray" || option.Core == "sing") {
			var err error
			core, err = s.defaultCore(option.Core)
			if err != nil {
				return err
			}
		}
	}
	if core == nil {
		return fmt.Errorf("requested core %q (name %q) is unavailable", option.Core, option.CoreName)
	}
	if option.Core != "" && option.Core != core.Type() {
		return fmt.Errorf("Core %q conflicts with CoreName %q (%s)", option.Core, option.CoreName, core.Type())
	}
	if !isSupported(info.Type, core.Protocols()) {
		return fmt.Errorf("core %s does not support protocol %s", core.Type(), info.Type)
	}
	if core.Type() == "sing" && info.VAllss != nil && (info.Type == "vless" || info.Type == "vmess") && (info.VAllss.Network == "xhttp" || info.VAllss.Network == "splithttp") {
		if option.CoreName != "" {
			return fmt.Errorf("named sing-box core %q does not support xhttp; select an Xray core", option.CoreName)
		}
		var err error
		core, err = s.defaultCore("xray")
		if err != nil {
			return err
		}
		if err := option.UseCore("xray"); err != nil {
			return fmt.Errorf("initialize xhttp Xray options: %w", err)
		}
		log.WithFields(log.Fields{"tag": tag, "transport": info.VAllss.Network, "core": "xray"}).Info("Using Xray for xhttp transport")
	}
	if len(option.Core) == 0 {
		err := option.UseCore(core.Type())
		if err != nil {
			return fmt.Errorf("unmarshal option error: %s", err)
		}
	}
	err := core.AddNode(tag, info, option)
	if err != nil {
		return err
	}
	s.nodes.Store(tag, core)
	return nil
}

// NodeCore reports the actual runtime choice, including the xhttp compatibility core.
func (s *Selector) NodeCore(tag string) string {
	if configured, ok := s.nodes.Load(tag); ok {
		return configured.(Core).Type()
	}
	return ""
}

func (s *Selector) UpdateNodePolicy(tag string, info *panel.NodeInfo, options *conf.Options) (err error) {
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("node %s: kernel rejected policy (panic payload omitted)", tag)
		}
	}()
	bound, ok := s.nodes.Load(tag)
	if !ok {
		return ErrPolicyReloadUnsupported
	}
	configured := bound.(Core)
	updater, ok := configured.(NodePolicyUpdater)
	if !ok {
		return ErrPolicyReloadUnsupported
	}
	resolved := *options
	if resolved.Core != configured.Type() {
		if err := resolved.UseCore(configured.Type()); err != nil {
			return err
		}
	}
	return updater.UpdateNodePolicy(tag, info, &resolved)
}

func (s *Selector) defaultCore(kind string) (Core, error) {
	if configured := s.cores[kind]; configured != nil && configured.Type() == kind {
		return configured, nil
	}
	names := make([]string, 0, len(s.cores))
	for name := range s.cores {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if configured := s.cores[name]; configured.Type() == kind {
			return configured, nil
		}
	}
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	if s.closed {
		return nil, errors.New("core selector is closed")
	}
	cache := &s.fallbackXray
	config := conf.CoreConfig{Type: "xray", XrayConfig: conf.NewXrayConfig()}
	if kind == "sing" {
		cache = &s.fallbackSing
		config = conf.DefaultCoreConfig()
	}
	if *cache != nil {
		return *cache, nil
	}
	factory := cores[kind]
	if factory == nil {
		return nil, fmt.Errorf("this build does not include the %s core", kind)
	}
	configured, err := createSafely(factory, &config)
	if err != nil {
		return nil, fmt.Errorf("initialize %s core: %w", kind, err)
	}
	if err := StartSafely(configured); err != nil {
		_ = CloseSafely(configured)
		return nil, fmt.Errorf("start %s core: %w", kind, err)
	}
	*cache = configured
	return configured, nil
}

func (s *Selector) DelNode(tag string) error {
	if t, e := s.nodes.Load(tag); e {
		err := t.(Core).DelNode(tag)
		if err != nil {
			return err
		}
		s.nodes.Delete(tag)
		return nil
	}
	return errors.New("the node is not have")
}

func (s *Selector) AddUsers(p *AddUsersParams) (added int, err error) {
	t, e := s.nodes.Load(p.Tag)
	if !e {
		return 0, errors.New("the node is not have")
	}
	return t.(Core).AddUsers(p)
}

func (s *Selector) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	t, e := s.nodes.Load(tag)
	if !e {
		return nil, errors.New("the node is not have")
	}
	return t.(Core).GetUserTrafficSlice(tag, reset)
}

func (s *Selector) DelUsers(users []panel.UserInfo, tag string, info *panel.NodeInfo) error {
	t, e := s.nodes.Load(tag)
	if !e {
		return errors.New("the node is not have")
	}
	return t.(Core).DelUsers(users, tag, info)
}

func (s *Selector) Protocols() []string {
	protocols := make([]string, 0)
	for i := range s.cores {
		protocols = append(protocols, s.cores[i].Protocols()...)
	}
	return protocols
}

func (s *Selector) Type() string {
	t := "Selector("
	var flag bool
	for n, c := range s.cores {
		if flag {
			t += " "
		} else {
			flag = true
		}
		if len(n) == 0 {
			t += c.Type()
		} else {
			t += n
		}
	}
	t += ")"
	return t
}

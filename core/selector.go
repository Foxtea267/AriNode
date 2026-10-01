package core

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/Foxtea267/AriNode/api/panel"
	"github.com/Foxtea267/AriNode/conf"
	log "github.com/sirupsen/logrus"
)

type Selector struct {
	cores map[string]Core
	nodes sync.Map
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
	var errs []error
	for i := range s.cores {
		if err := CloseSafely(s.cores[i]); err != nil {
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
	var core Core
	if len(option.CoreName) > 0 {
		// use name to select core
		if c, ok := s.cores[option.CoreName]; ok {
			core = c
		}
	} else {
		// use type to select core
		for _, c := range s.cores {
			if len(option.Core) == 0 {
				if !isSupported(info.Type, c.Protocols()) {
					continue
				}
			} else if option.Core != c.Type() {
				continue
			}
			core = c
		}
	}
	if core == nil {
		return errors.New("the node type is not support")
	}
	if len(option.Core) == 0 {
		option.Core = core.Type()
		err := option.UnmarshalJSON(option.RawOptions)
		if err != nil {
			return fmt.Errorf("unmarshal option error: %s", err)
		}
		option.RawOptions = nil
	}
	err := core.AddNode(tag, info, option)
	if err != nil {
		return err
	}
	s.nodes.Store(tag, core)
	return nil
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

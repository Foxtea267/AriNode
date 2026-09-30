package core

import (
	"errors"
	"fmt"

	"github.com/Foxtea267/ariNode/conf"
)

var (
	cores = map[string]func(c *conf.CoreConfig) (Core, error){}
)

func NewCore(c []conf.CoreConfig) (Core, error) {
	if len(c) == 0 {
		return nil, errors.New("no have vail core")
	}
	// multi core
	if len(c) > 1 {
		return NewSelector(c)
	}
	// one core
	if f, ok := cores[c[0].Type]; ok {
		return createSafely(f, &c[0])
	} else {
		return nil, errors.New("unknown core type")
	}
}

func createSafely(factory func(*conf.CoreConfig) (Core, error), config *conf.CoreConfig) (instance Core, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			instance = nil
			err = fmt.Errorf("core initialization panic: %v", recovered)
		}
	}()
	return factory(config)
}

func StartSafely(instance Core) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("core start panic: %v", recovered)
		}
	}()
	return instance.Start()
}

func CloseSafely(instance Core) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("core close panic: %v", recovered)
		}
	}()
	return instance.Close()
}

func RegisterCore(t string, f func(c *conf.CoreConfig) (Core, error)) {
	cores[t] = f
}

func RegisteredCore() []string {
	cs := make([]string, 0, len(cores))
	for k := range cores {
		cs = append(cs, k)
	}
	return cs
}

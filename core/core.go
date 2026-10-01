package core

import (
	"fmt"

	"github.com/Foxtea267/AriNode/conf"
)

var (
	cores = map[string]func(c *conf.CoreConfig) (Core, error){}
)

func NewCore(c []conf.CoreConfig) (Core, error) {
	if len(c) == 0 {
		c = []conf.CoreConfig{conf.DefaultCoreConfig()}
	}
	return NewSelector(c)
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

package cmd

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Foxtea267/AriNode/conf"
	vCore "github.com/Foxtea267/AriNode/core"
	"github.com/Foxtea267/AriNode/komari"
	"github.com/Foxtea267/AriNode/limiter"
	"github.com/Foxtea267/AriNode/node"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

var (
	config       string
	watch        bool
	statusListen string
)

var serverCommand = cobra.Command{
	Use:   "server",
	Short: "Run AriNode server",
	Run:   serverHandle,
	Args:  cobra.NoArgs,
}

func init() {
	serverCommand.PersistentFlags().
		StringVarP(&config, "config", "c",
			"/etc/arinode/config.json", "config file path")
	serverCommand.PersistentFlags().
		BoolVarP(&watch, "watch", "w",
			true, "watch file path change")
	serverCommand.Flags().StringVar(&statusListen, "status-listen", "127.0.0.1:18086", "local status API address (empty disables it)")
	command.AddCommand(&serverCommand)
}

func serverHandle(_ *cobra.Command, _ []string) {
	showVersion()
	c := conf.New()
	err := c.LoadFromPath(config)
	if err != nil {
		log.WithField("err", err).Error("Load config file failed")
		return
	}
	switch c.LogConfig.Level {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	}
	if c.LogConfig.Output != "" {
		f, err := os.OpenFile(c.LogConfig.Output, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			log.WithField("err", err).Error("Open log file failed, using stdout instead")
		} else {
			log.SetOutput(f)
			defer f.Close()
		}
	}
	limiter.Init()
	log.Info("Start AriNode...")
	vc, err := vCore.NewCore(c.CoresConfig)
	if err != nil {
		log.WithField("err", err).Error("new core failed")
		return
	}
	err = vCore.StartSafely(vc)
	if err != nil {
		log.WithField("err", err).Error("Start core failed")
		return
	}
	defer vCore.CloseSafely(vc)
	log.Info("Core ", vc.Type(), " started")
	nodes := node.New()
	err = nodes.Start(c.NodeConfig, vc)
	if err != nil {
		log.WithField("err", err).Error("Run nodes failed")
		return
	}
	log.Info("Nodes started")
	monitors := komari.New()
	monitors.Reconcile(c.Komari)
	defer monitors.Close()
	if statusListen != "" {
		err = serveStatus(statusListen, nodes, monitors)
		if err != nil {
			log.WithError(err).Error("Start local status API failed")
			return
		}
	}
	xdns := os.Getenv("XRAY_DNS_PATH")
	sdns := os.Getenv("SING_DNS_PATH")
	if watch {
		err = c.Watch(config, xdns, sdns, func(next *conf.Conf) error {
			if !reflect.DeepEqual(c.CoresConfig, next.CoresConfig) {
				return fmt.Errorf("core settings changed; use anctl restart to apply them")
			}
			if err := nodes.Reconcile(next.NodeConfig); err != nil {
				return err
			}
			monitors.Reconcile(next.Komari)
			log.Info("Node bindings reconciled")
			runtime.GC()
			return nil
		})
		if err != nil {
			log.WithField("err", err).Error("start watch failed")
			return
		}
	}
	// clear memory
	runtime.GC()
	// wait exit signal
	{
		osSignals := make(chan os.Signal, 1)
		signal.Notify(osSignals, syscall.SIGINT, syscall.SIGTERM)
		<-osSignals
	}
}

func serveStatus(address string, nodes *node.Node, monitors *komari.Manager) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "localhost" && net.ParseIP(host) == nil) {
		return fmt.Errorf("invalid --status-listen address")
	}
	if host != "localhost" && !net.ParseIP(host).IsLoopback() && os.Getenv("ARINODE_STATUS_TOKEN") == "" {
		return fmt.Errorf("ARINODE_STATUS_TOKEN is required for a non-loopback status listener")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	started := time.Now().UTC()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		if token := os.Getenv("ARINODE_STATUS_TOKEN"); token != "" {
			provided := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		bindings := nodes.Snapshot()
		komariBindings := monitors.Snapshot()
		state := "running"
		for _, binding := range bindings {
			if binding.State != "running" {
				state = "degraded"
				break
			}
		}
		for _, binding := range komariBindings {
			if binding.State != "reporting" {
				state = "degraded"
				break
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"service": "AriNode", "status": state, "started_at": started, "nodes": bindings, "komari": komariBindings})
	})
	go func() {
		if err := http.Serve(listener, mux); err != nil {
			log.WithError(err).Error("status API stopped")
		}
	}()
	return nil
}

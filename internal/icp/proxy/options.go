package proxy

import (
	"flag"
	"fmt"
	"io"
	"math"
	"time"
)

const (
	Front   = "front-proxy"
	Cluster = "cluster-router"
	L0      = "l0-passthrough"
)

type Options struct {
	Mode, Host, Namespace, Selector, SessionKey, Token, ConfigFile, Kong, Vector string
	Port, Connections                                                            int
	Connect, Dial, First, Idle                                                   time.Duration
}

func ParseOptions(args []string) (Options, error) {
	var o Options
	var sd string
	var connect, dial, read float64
	f := flag.NewFlagSet("neutree-gateway", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Mode, "mode", Front, "gateway mode")
	f.StringVar(&o.Host, "host", "0.0.0.0", "listen host")
	f.IntVar(&o.Port, "port", 8000, "listen port")
	f.StringVar(&sd, "service-discovery", "", "service discovery")
	f.StringVar(&o.Namespace, "k8s-namespace", "default", "Pod namespace")
	f.StringVar(&o.Selector, "k8s-label-selector", "app=inference", "Pod selector")
	f.StringVar(&o.SessionKey, "session-key", "session_id", "affinity field")
	f.IntVar(&o.Connections, "max-connections-per-backend", 100, "backend connection slots")
	f.Float64Var(&connect, "connect-timeout-seconds", 60, "pool and connection acquisition")
	f.Float64Var(&dial, "sock-connect-timeout-seconds", 10, "socket dial")
	f.Float64Var(&read, "sock-read-timeout-seconds", 600, "socket idle")
	f.StringVar(&o.Kong, "kong-url", "http://kong:8000", "Kong URL")
	f.StringVar(&o.Vector, "vector-url", "http://vector:30122", "usage and trace destination")
	f.StringVar(&o.ConfigFile, "config", "", "optional route config JSON")
	f.StringVar(&o.Token, "internal-token", "", "trusted front token")

	if err := f.Parse(args); err != nil {
		return o, err
	}

	set := map[string]bool{}

	f.Visit(func(fl *flag.Flag) { set[fl.Name] = true })

	if !set["mode"] && (sd == "k8s" || set["k8s-namespace"]) {
		o.Mode = Cluster
	}

	if f.NArg() > 0 {
		return o, fmt.Errorf("unexpected argument %q", f.Arg(0))
	}

	switch o.Mode {
	case Front, Cluster, L0:
	default:
		return o, fmt.Errorf("unknown mode %q", o.Mode)
	}

	if sd != "" && sd != "k8s" {
		return o, fmt.Errorf("unknown discovery %q", sd)
	}

	if o.Port < 0 || o.Port > 65535 || o.Connections < 0 || o.Connections > 65535 {
		return o, fmt.Errorf("port and connection limit must be in [0, 65535]")
	}

	if o.Mode != Cluster {
		if !set["connect-timeout-seconds"] {
			connect = 10
		}

		if !set["sock-read-timeout-seconds"] {
			read = 120
		}
	}

	for _, v := range []float64{connect, dial, read} {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 604800 {
			return o, fmt.Errorf("invalid timeout %v", v)
		}
	}

	o.Connect = time.Duration(connect * 1e9)
	o.Dial = time.Duration(dial * 1e9)
	o.Idle = time.Duration(read * 1e9)
	o.First = o.Idle

	if o.Mode != Cluster {
		o.First = 30 * time.Second
	}

	return o, nil
}

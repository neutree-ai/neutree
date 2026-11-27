package proxy

import (
	"testing"
	"time"
)

func TestRouterFlags(t *testing.T) {
	for _, tc := range []struct {
		args                       []string
		mode                       string
		slots                      int
		connect, first, dial, idle time.Duration
	}{
		{nil, Front, 100, 10, 30, 10, 120},
		{[]string{"--service-discovery", "k8s"}, Cluster, 100, 60, 600, 10, 600},
		{[]string{"--k8s-namespace=ws", "--max-connections-per-backend=0", "--connect-timeout-seconds=0", "--sock-connect-timeout-seconds=0", "--sock-read-timeout-seconds=0"}, Cluster, 0, 0, 0, 0, 0},
		{[]string{"--mode=l0-passthrough", "--service-discovery=k8s"}, L0, 100, 10, 30, 10, 120},
	} {
		o, err := ParseOptions(tc.args)
		if err != nil || o.Mode != tc.mode || o.Connections != tc.slots || o.Connect != tc.connect*time.Second || o.First != tc.first*time.Second || o.Dial != tc.dial*time.Second || o.Idle != tc.idle*time.Second {
			t.Errorf("args %v: %+v, %v", tc.args, o, err)
		}
	}
}

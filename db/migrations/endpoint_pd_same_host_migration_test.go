package migrations

import (
	"os"
	"strings"
	"testing"
)

func TestPDSameHostEndpointSpecMigrationIncludesKV(t *testing.T) {
	up, err := os.ReadFile("060_endpoint_pd_same_host_demo.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("060_endpoint_pd_same_host_demo.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	if !strings.Contains(string(up), "ALTER TYPE api.endpoint_spec ADD ATTRIBUTE kv") {
		t.Fatalf("up migration must add api.endpoint_spec.kv for spec.kv.transfer persistence")
	}
	if !strings.Contains(string(down), "ALTER TYPE api.endpoint_spec DROP ATTRIBUTE kv") {
		t.Fatalf("down migration must drop api.endpoint_spec.kv")
	}
}

func TestPDSameHostEndpointStatusMigrationIncludesReplicaCounters(t *testing.T) {
	up, err := os.ReadFile("060_endpoint_pd_same_host_demo.up.sql")
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	down, err := os.ReadFile("060_endpoint_pd_same_host_demo.down.sql")
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}

	for _, name := range []string{"total_replicas", "ready_replicas"} {
		if !strings.Contains(string(up), "ALTER TYPE api.endpoint_status ADD ATTRIBUTE "+name) {
			t.Fatalf("up migration must add api.endpoint_status.%s", name)
		}
		if !strings.Contains(string(down), "ALTER TYPE api.endpoint_status DROP ATTRIBUTE "+name) {
			t.Fatalf("down migration must drop api.endpoint_status.%s", name)
		}
	}
}

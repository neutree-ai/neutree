-- Cluster-level port allocator infrastructure.
-- See .claude/knowledge/neutree-cluster-port-allocator-zh.md
--
-- Ports are allocated by control plane (pkg/portalloc) and consumed by the
-- engine renderer (Ray runtime_env / K8s container.env). The allocation
-- granularity is (cluster × endpoint × replica × role × rank × position):
--   - cluster_id  ─ which cluster the port lives on
--   - endpoint_id ─ allocation lifecycle owner (cascade delete)
--   - replica_idx ─ 0..NumReplicas-1, matches Ray Serve native rank
--   - role_name   ─ matches plan.Role.Name (prefill / decode / engine / ...)
--   - rank_idx    ─ 0..Role.Instances-1
--   - position_idx ─ 0..Role.PortsPerRank-1, per-engine positional
--                   convention (vLLM: 0=HTTP, 1=NIXL side_channel)

ALTER TYPE api.cluster_spec ADD ATTRIBUTE port_range json;

CREATE TABLE api.cluster_port_allocations (
    cluster_id    integer NOT NULL REFERENCES api.clusters(id) ON DELETE CASCADE,
    port          integer NOT NULL,
    endpoint_id   integer NOT NULL REFERENCES api.endpoints(id) ON DELETE CASCADE,
    replica_idx   integer NOT NULL,
    role_name     text    NOT NULL,
    rank_idx      integer NOT NULL,
    position_idx  integer NOT NULL,
    allocated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cluster_id, port),
    -- Same (endpoint, replica, role, rank, position) tuple maps to exactly one port.
    UNIQUE (cluster_id, endpoint_id, replica_idx, role_name, rank_idx, position_idx)
);

CREATE INDEX idx_port_alloc_endpoint ON api.cluster_port_allocations(endpoint_id);
CREATE INDEX idx_port_alloc_cluster_role ON api.cluster_port_allocations(cluster_id, endpoint_id, role_name);

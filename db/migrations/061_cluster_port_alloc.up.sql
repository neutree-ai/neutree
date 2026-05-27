-- Cluster port allocator infrastructure.
-- See .claude/knowledge/neutree-cluster-port-allocator-zh.md
--
-- Ports are allocated by control plane (pkg/portalloc) and consumed by the
-- engine renderer (Ray runtime_env / K8s container.env). The allocation
-- granularity is (cluster × endpoint × role_group × role × rank):
--   - cluster_id  ─ which cluster the port lives on
--   - endpoint_id ─ allocation lifecycle owner (cascade delete)
--   - role_group_index ─ 0..NumReplicas-1, matches derived RoleGroup index
--   - role       ─ matches derived runtime role name (prefill / decode / engine / sidecar)
--   - rank       ─ 0..Role.Instances-1

CREATE TABLE api.cluster_port_allocations (
    id           BIGSERIAL PRIMARY KEY,
    cluster_id    integer NOT NULL REFERENCES api.clusters(id) ON DELETE CASCADE,
    port          integer NOT NULL,
    endpoint_id   integer NOT NULL REFERENCES api.endpoints(id) ON DELETE CASCADE,
    role_group_index integer NOT NULL,
    role          text    NOT NULL CHECK (role IN ('engine', 'sidecar', 'prefill', 'decode')),
    rank          integer NOT NULL DEFAULT 0,
    allocated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (cluster_id, port),
    -- Same (endpoint, RoleGroup, role, rank) tuple maps to exactly one port.
    UNIQUE (cluster_id, endpoint_id, role_group_index, role, rank)
);

CREATE INDEX idx_port_alloc_endpoint ON api.cluster_port_allocations(endpoint_id);

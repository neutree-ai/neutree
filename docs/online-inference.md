# Online Inference

This document describes how Neutree manages online inference services (Endpoints).

## Overview

Neutree provides OpenAI-compatible inference APIs with the following capabilities:

- **Multiple model types**: Text generation, embedding, reranker
- **Multiple inference engines**: vLLM, and other engines via plugin
- **High availability**: Multi-replica deployment with configurable placement
- **Load balancing**: KV cache-friendly routing strategies
- **Flexible GPU usage**: Tensor parallel (multi-GPU) and fractional GPU

## Architecture

Neutree handles two concerns for inference: **lifecycle management** and **request routing**.

```mermaid
flowchart LR
    Client --> Gateway["AI Gateway"]
    Gateway --> Cluster

    subgraph Cluster
        Proxy["Request Proxy"] --> R1["Replica 1"]
        Proxy --> R2["Replica 2"]
    end

    Core["neutree-core"] -.->|sync routes| Gateway
    Core -.->|deploy| Cluster
```

- **AI Gateway**: Authenticates API keys and routes requests to the target cluster
- **Request Proxy**: Routes requests to replicas (Router in Kubernetes mode, Ray Serve Proxy in Static Nodes mode)
- **neutree-core**: Deploys endpoints and syncs routing config to AI Gateway
- **Images**: Kubernetes mode uses different images to run inference on different accelerators, and these are usually community-maintained images.

## Engine

An Engine defines an inference runtime (e.g., vLLM). Each engine can have multiple versions with different configurations.

- **Supported tasks**: Defines what model types the engine supports (text-generation, text-embedding)
- **Version schema**: Each version defines configurable parameters via JSON schema
- **Deploy template**: Kubernetes mode uses templates to generate Deployment manifests

## Endpoint

An Endpoint is a deployed inference service. Key configurations:

- **Cluster**: Target cluster to deploy on
- **Engine**: Inference engine and version (e.g., vLLM v0.6.0)
- **Model**: Model source and name from a ModelRegistry
- **Resources**: CPU, memory, GPU per replica
- **Replicas**: Number of replica instances
- **Variables**: Engine-specific parameters (e.g., `gpu_memory_utilization` for vLLM), validated against engine's schema
- **Env**: Environment variables injected into the inference container

## Load Balancing

When an endpoint has multiple replicas, the routing strategy affects KV cache hit rates. Supported strategies:

- **Round Robin**: Distributes requests evenly across replicas in rotation.
- **Consistent Hashing with Bounded Loads**: Routes requests with similar prefixes to the same replica for better KV cache reuse. Falls back to next replica when load exceeds threshold.

### Consistent Hashing with Bounded Loads Algorithm

The CHWBL algorithm balances KV cache locality with load distribution:

**Hash Ring Construction**

Each replica is mapped to multiple positions on a hash ring using virtual nodes (default: 100 per replica). Virtual node positions are computed as `MD5(replica_id:index)`. More virtual nodes improve load distribution uniformity.

**Cache Key Extraction**

For OpenAI-compatible chat completions, the cache key is derived from:
- System prompt (if present)
- First N user messages (configurable, default: 2)

This ensures conversations with similar prefixes route to the same replica, maximizing KV cache reuse. For non-chat requests, the full payload is used as the cache key.

**Request Routing**

1. Hash the cache key and locate the nearest replica on the ring (binary search)
2. Check if the target replica meets the load constraint:
   - Calculate average load: `avg_load = (total_load + 1) / num_replicas`
   - Calculate threshold: `threshold = avg_load × load_factor` (default load_factor: 1.25)
   - Accept if `(replica_load + 1) ≤ threshold`
3. If rejected, check the next replica on the ring
4. If all replicas exceed the threshold, fall back to the initially selected replica

**Configuration Parameters**

| Parameter | Default | Description |
|-----------|---------|-------------|
| `virtual_nodes_per_replica` | 100 | Number of virtual nodes per replica on the hash ring |
| `load_factor` | 1.25 | Maximum load relative to average (1.25 = 25% above average) |
| `max_user_messages_for_cache` | 2 | Number of user messages included in cache key |

## GPU Allocation

Neutree uses `CUDA_VISIBLE_DEVICES` to control GPU access. Important notes:

- **No hard isolation**: Current version does not enforce GPU memory isolation at hardware level
- **Fractional GPU**: Multiple workloads sharing one GPU will all see the full device. Memory control relies on engine configuration (e.g., vLLM's `--gpu-memory-utilization`)
- **Tensor Parallel**: Workloads see multiple GPUs via `CUDA_VISIBLE_DEVICES`. Engine must correctly utilize all visible devices.

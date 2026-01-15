<p align="center">
  <img src="assets/logo.png" alt="Neutree" width="200">
</p>

# Neutree

Neutree is an open-source Large Language Model (LLM) infrastructure management platform.

## Features

- **Multi-cluster Management**: Deploy and manage inference workloads across Kubernetes clusters and static node clusters (Ray + Docker)
- **OpenAI-compatible API**: Unified inference gateway with API key authentication and usage tracking
- **Multi-tenancy**: Workspace-based resource isolation with fine-grained RBAC
- **Production-ready Observability**: Integrated metrics collection and Grafana dashboards
- **Flexible Model Storage**: Support for HuggingFace Hub and file-based model registries

## Documentation

Visit [docs.neutree.ai](https://docs.neutree.ai) for installation guides, tutorials, and API references.

## Development

### Design Documents

Technical design documents for contributors are available in the [`docs/`](docs/) directory:

- [Architecture Overview](docs/architecture.md)
- [Cluster Management](docs/cluster-management.md)
- [Online Inference](docs/online-inference.md)
- [Model Registry](docs/model-registry.md)
- [User Management](docs/user-management.md)
- [RBAC and Workspace](docs/rbac.md)
- [Cluster Monitoring](docs/cluster-monitoring.md)

### Contributing

**Prerequisites**

- Go 1.23+
- Docker
- Make

**Common workflows**

```bash
# Build all binaries
make build

# Run unit tests
make test

# Run linter
make lint

# Run database tests
make db-test

# Quick iteration: rebuild and restart local containers
make docker-test-api
make docker-test-core
```

## Roadmap

- More accelerator support (e.g., Intel XPU)
- Inference endpoint auto-scaling
- External KV cache integration
- Quota and usage limits
- GPU memory hard isolation
- More inference engine adapters
- External endpoint support for unified management of local and external model services

## Community

- [GitHub Issues](https://github.com/neutree-ai/neutree/issues) - Bug reports and feature requests
- [Discussions](https://github.com/neutree-ai/neutree/discussions) - Questions and community support

## License

Neutree is licensed under the [Apache License 2.0](LICENSE).

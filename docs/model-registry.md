# Model Registry

This document describes how Neutree manages model storage and retrieval through Model Registries.

## Overview

Neutree supports two types of model registries:

- **HuggingFace**: Connects to HuggingFace Hub for downloading public or private models
- **File-based**: Local or network-attached storage using BentoML's model format

This document focuses on the file-based registry, which is recommended for production environments where you need full control over model storage.

## File-based Registry

The file-based registry stores models on filesystem using BentoML's model format. Two connection types are supported:

| Type | URL Format                     | Use Case                              |
| ---- | ------------------------------ | ------------------------------------- |
| NFS  | `nfs://server:/path/to/models` | Multi-node deployment, shared storage |

For NFS, Neutree automatically mounts the remote path to a local mount point during registry connection.

## Directory Structure

Models are stored following BentoML's directory layout:

```
$BENTOML_HOME/
└── models/
    └── {model_name}/
        ├── latest              # Points to the latest version
        └── {version}/
            ├── model.yaml      # Model metadata
            └── ...             # Model files
```

The `model.yaml` file contains metadata including:

- `name`: Model name
- `version`: Version identifier (16-char base32 string)
- `module`: Framework module (e.g., transformers)
- `size`: Human-readable size
- `creation_time`: Timestamp

## Model Operations

### Import

Models are imported as `.bentomodel` archives (tar.gz format). The import process:

1. Extract archive to a temporary directory
2. Atomic rename to final destination
3. Update `latest` pointer

### Export

Export packages a model version into a `.bentomodel` archive for distribution.

### List

Traverses the models directory and reads `model.yaml` from each version, returning aggregated model information sorted by creation time.

## Integration with Endpoints

When an Endpoint references a model from a file-based registry:

1. **Kubernetes mode**: The model directory is mounted to the inference container via a Pod NFS Volume
2. **Static Nodes mode**: The model path is mounted into the Ray worker container

## Implementation Notes

The file-based registry implementation avoids calling BentoML CLI (Python) for core operations like listing and importing models. Instead, it directly reads and writes the filesystem structure. This eliminates the overhead of forking Python processes, significantly improving performance for frequent operations.

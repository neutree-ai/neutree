# Grafana Dashboard Conversion Scripts

This directory contains scripts and configurations for converting Grafana dashboards between different formats.

## Directory Structure

```
scripts/dashboard/
├── dashboard_converter.py          # Generic dashboard conversion tool
├── convert_vllm_dashboard.py       # vLLM dashboard conversion script
├── convert_ray_to_neutree.py       # Ray cluster label conversion script
├── sync-grafana-dashboards.sh      # Sync dashboards from upstream
├── configs/                        # Configuration files
│   ├── vllm_convert.json          # vLLM conversion configuration
│   └── ray_to_neutree_cluster.json # Ray cluster label conversion config
├── vllm-upstream/                  # vLLM upstream dashboard sources
└── ray-upstream/                   # Ray upstream dashboard sources
```

## Available Conversions

### 1. Ray to Neutree Cluster Label Conversion

Converts `ray_io_cluster` labels to `neutree_cluster` in Ray upstream dashboards.

**Usage:**

```bash
# Method 1: Using the convenience script (recommended)
cd scripts/dashboard
python3 convert_ray_to_neutree.py

# Method 2: Specify custom source and output files
python3 convert_ray_to_neutree.py source_dashboard.json output_dashboard.json

# Method 3: Using generic converter with config
python3 dashboard_converter.py configs/ray_to_neutree_cluster.json
```

**Configuration:**

Edit `configs/ray_to_neutree_cluster.json` to specify source and output files:

```json
{
  "source_file": "source_serve_deployment_grafana_dashboard.json",
  "output_file": "serve_deployment_grafana_dashboard.json",
  ...
}
```

**Example:**

Before conversion:
```promql
ray_serve_deployment_replica_healthy{
  application=~"$Application",
  ray_io_cluster=~"$Cluster"
}
```

After conversion:
```promql
ray_serve_deployment_replica_healthy{
  application=~"$Application",
  neutree_cluster=~"$Cluster"
}
```

### 2. vLLM Dashboard Conversion

Converts open-source vLLM dashboards to Neutree-integrated version.

**Usage:**

```bash
cd scripts/dashboard
python3 convert_vllm_dashboard.py

# Or using generic converter
python3 dashboard_converter.py configs/vllm_convert.json
```

**Features:**
- Converts `vllm:` metrics to `ray_vllm:`
- Replaces `model_name` filters with multi-dimensional Neutree filters
- Adds Application, Deployment, Replica, and Cluster variables

## Generic Dashboard Converter

The `dashboard_converter.py` script provides a flexible framework for dashboard conversions.

**Usage:**

```bash
python3 dashboard_converter.py <config_file> [dashboard_dir]
```

**Configuration Format:**

```json
{
  "name": "Conversion Name",
  "description": "Conversion description",
  "source_file": "source_dashboard.json",
  "output_file": "output_dashboard.json",
  "uid": null,
  "keep_datasource_variable": false,

  "metric_rules": [
    {
      "name": "rule_name",
      "description": "Rule description",
      "pattern": "old_pattern",
      "replacement": "new_pattern",
      "is_regex": true
    }
  ],

  "filter_rules": [],
  "custom_rules": [],
  "variables": []
}
```

## Dashboard Locations

- **Source dashboards (upstream)**: `scripts/dashboard/{vllm,ray}-upstream/`
- **Converted dashboards**: `observability/grafana/dashboards/`

## Syncing from Upstream

Use the sync script to pull latest dashboards from upstream repositories:

```bash
cd scripts/dashboard
./sync-grafana-dashboards.sh
```

This will:
1. Sync dashboards from upstream repositories using vendir
2. Apply necessary conversions
3. Place converted dashboards in the correct location

## Creating New Conversions

1. Create a configuration file in `configs/`:
   ```json
   {
     "name": "My Conversion",
     "description": "Description",
     "source_file": "source.json",
     "output_file": "output.json",
     "filter_rules": [...]
   }
   ```

2. (Optional) Create a convenience script:
   ```python
   #!/usr/bin/env python3
   from dashboard_converter import DashboardConverter, load_config_from_file

   config = load_config_from_file("configs/my_config.json")
   converter = DashboardConverter(config)
   converter.convert()
   ```

3. Test the conversion:
   ```bash
   python3 dashboard_converter.py configs/my_config.json
   ```

## Troubleshooting

### Import Errors

If you see "ModuleNotFoundError: No module named 'dashboard_converter'":
- Make sure you're running from the `scripts/dashboard/` directory
- The scripts automatically add the current directory to Python path

### File Not Found

If source files are not found:
- Check the `source_file` path in your configuration
- Ensure dashboards are synced from upstream: `./sync-grafana-dashboards.sh`
- Verify the dashboard directory structure

### Validation

To verify a conversion worked correctly:

```bash
# Count occurrences before and after
grep -o "ray_io_cluster" output_dashboard.json | wc -l  # Should be 0
grep -o "neutree_cluster" output_dashboard.json | wc -l  # Should be > 0
```

## Examples

### Convert All Ray Dashboards

```bash
#!/bin/bash
for file in ../../observability/grafana/dashboards/source_*_dashboard.json; do
    output=$(echo "$file" | sed 's/source_//')
    python3 convert_ray_to_neutree.py "$file" "$output"
done
```

### Batch Conversion

```bash
# Convert multiple dashboards with different configs
for config in configs/*.json; do
    echo "Processing $config..."
    python3 dashboard_converter.py "$config"
done
```

#!/bin/bash
set -e

mkdir -p output # ensure output directory exists
python3 ./convert_vllm_dashboard.py
# Upstream SGLang ships a single grafana JSON; rename to the canonical filename
# the converter expects, then run it through the same config-driven pipeline as
# vLLM so the dashboard picks up Neutree multi-tenant filter variables.
mv -f sglang-upstream/sglang-dashboard.json sglang-upstream/grafana.json
python3 ./convert_sglang_dashboard.py
python3 ./convert_ray_to_neutree.py ray-upstream/data_grafana_dashboard.json output/data_grafana_dashboard.json
python3 ./convert_ray_to_neutree.py ray-upstream/default_grafana_dashboard.json output/default_grafana_dashboard.json
python3 ./convert_ray_to_neutree.py ray-upstream/serve_deployment_grafana_dashboard.json output/serve_deployment_grafana_dashboard.json
python3 ./convert_ray_to_neutree.py ray-upstream/serve_grafana_dashboard.json output/serve_grafana_dashboard.json
echo "✅ All Grafana dashboards have been synchronized and converted successfully!"

mv output/*.json ../../observability/grafana/dashboards/
echo "📂 Moved converted dashboards to observability/grafana/dashboards/"

python3 ./inject_theme.py
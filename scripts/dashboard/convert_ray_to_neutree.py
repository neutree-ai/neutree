#!/usr/bin/env python3
"""
Ray to Neutree Cluster Label Conversion Script

This script converts ray_io_cluster label to neutree_cluster in Ray upstream
Grafana dashboards using the generic dashboard converter.

Usage:
    python3 convert_ray_to_neutree.py [source_file] [output_file]

If no arguments provided:
    - Uses config file to determine source and output files

Examples:
    # Convert using config (recommended)
    python3 convert_ray_to_neutree.py

    # Convert specific files
    python3 convert_ray_to_neutree.py source_custom.json output_custom.json
"""

import sys
import json
from pathlib import Path

# Add current directory to Python path for importing dashboard_converter
current_dir = Path(__file__).parent
sys.path.insert(0, str(current_dir))

# Import generic converter
from dashboard_converter import (
    DashboardConverter, DashboardConversionConfig, ConversionRule, VariableOverride,
)


CLUSTER_VARIABLE_DESCRIPTION = (
    "Filter queries to a specific Neutree cluster. Neutree attaches the cluster "
    "label to every scrape target."
)


def create_conversion_config(source_file: str, output_file: str) -> DashboardConversionConfig:
    """
    Create conversion configuration for ray_io_cluster to neutree_cluster conversion.

    Args:
        source_file: Path to source dashboard JSON file
        output_file: Path to output dashboard JSON file

    Returns:
        DashboardConversionConfig object
    """
    filter_rules = [
        ConversionRule(
            name="ray_io_cluster_to_neutree_cluster",
            description="Replace ray_io_cluster label with neutree_cluster",
            pattern="ray_io_cluster",
            replacement="neutree_cluster",
            is_regex=False
        )
    ]

    # Upstream describes the Cluster variable in KubeRay terms. Conversion rules
    # never touch descriptions, so state the Neutree behaviour by variable name.
    variable_overrides = [
        VariableOverride(
            name="Cluster",
            description=CLUSTER_VARIABLE_DESCRIPTION,
        )
    ]

    return DashboardConversionConfig(
        name="Ray to Neutree Cluster Label",
        description="Convert ray_io_cluster label to neutree_cluster in Ray upstream dashboards",
        source_file=source_file,
        output_file=output_file,
        uid=None,  # Keep original UID
        metric_rules=[],
        filter_rules=filter_rules,
        custom_rules=[],
        variables=[],
        # `variables=[]` plus `keep_datasource_variable=False` is load-bearing: the
        # converter only converts upstream variables in place on that combination.
        # Defining any variable here would switch it to the replace branch and drop
        # every upstream variable, including the $Cluster the panels reference.
        keep_datasource_variable=False,  # Keep original variables
        variable_overrides=variable_overrides,
    )


def main():
    """Main conversion function."""

    # Parse arguments
    if len(sys.argv) == 1:
        # Use config file
        config_file = "configs/ray_to_neutree_cluster.json"
        print(f"📋 Using configuration file: {config_file}")
        with open(config_file, 'r') as f:
            config_data = json.load(f)

        source_file = config_data['source_file']
        output_file = config_data['output_file']

        print(f"📖 Source: {source_file}")
        print(f"📝 Output: {output_file}")

    elif len(sys.argv) == 3:
        source_file = sys.argv[1]
        output_file = sys.argv[2]
    else:
        print(__doc__)
        sys.exit(1)

    print(f"🔄 Converting ray_io_cluster → neutree_cluster")

    try:
        # Create configuration
        config = create_conversion_config(source_file, output_file)

        # Create converter and perform conversion
        converter = DashboardConverter(config)
        success = converter.convert()

        if success:
            print(f"\n🎉 Conversion successful!")

        sys.exit(0 if success else 1)

    except Exception as e:
        print(f"❌ Conversion failed: {e}")
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == "__main__":
    main()

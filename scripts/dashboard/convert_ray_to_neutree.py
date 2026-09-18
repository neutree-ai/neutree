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
    DashboardConverter, DashboardConversionConfig, load_config_from_file,
)


CONFIG_FILE = current_dir / "configs" / "ray_to_neutree_cluster.json"


def create_conversion_config(source_file: str, output_file: str) -> DashboardConversionConfig:
    """
    Load the ray_io_cluster to neutree_cluster conversion configuration.

    The rules come from the JSON config so they have a single definition, the same
    way convert_vllm_dashboard.py and convert_sglang_dashboard.py load theirs. Only
    the file paths vary per invocation.

    Args:
        source_file: Path to source dashboard JSON file
        output_file: Path to output dashboard JSON file

    Returns:
        DashboardConversionConfig object
    """
    config = load_config_from_file(str(CONFIG_FILE))
    config.source_file = source_file
    config.output_file = output_file

    return config


def main():
    """Main conversion function."""

    # Parse arguments
    if len(sys.argv) == 1:
        # Use config file
        print(f"📋 Using configuration file: {CONFIG_FILE}")
        with open(CONFIG_FILE, 'r') as f:
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

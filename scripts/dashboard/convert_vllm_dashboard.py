#!/usr/bin/env python3
"""
vLLM Grafana Dashboard Conversion Shortcut Script

This is a convenience wrapper script that uses the generic converter with
the vllm_to_ray.json configuration file to convert vLLM Dashboard.

Usage with generic converter: python dashboard_converter.py configs/vllm_to_ray.json
Usage with this shortcut script: python convert_vllm_dashboard.py
"""

import sys, os
from pathlib import Path

# Import generic converter
from dashboard_converter import load_config_from_file, DashboardConverter


def main():
    """
    Main function - uses predefined vLLM conversion configuration
    """
    config_file = 'configs/vllm_convert.json'

    try:
        print(f"📋 Using configuration file: {config_file}")
        print("💡 Hint: To customize conversion rules, edit the config file or create a new one\n")
        print(os.getcwd())
        config = load_config_from_file(str(config_file))
        converter = DashboardConverter(config)
        success = converter.convert()

        if success:
            print("\n🎉 vLLM Dashboard conversion successful!")

        sys.exit(0 if success else 1)

    except Exception as e:
        print(f"❌ Conversion failed: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()

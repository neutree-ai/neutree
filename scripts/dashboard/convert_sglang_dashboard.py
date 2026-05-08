#!/usr/bin/env python3
"""
SGLang Grafana Dashboard Conversion Shortcut Script

This is a convenience wrapper script that uses the generic converter with
the sglang_convert.json configuration file to convert SGLang Dashboard.

Usage with generic converter: python dashboard_converter.py configs/sglang_convert.json
Usage with this shortcut script: python convert_sglang_dashboard.py
"""

import sys, os
from pathlib import Path

# Import generic converter
from dashboard_converter import load_config_from_file, DashboardConverter


def main():
    """
    Main function - uses predefined SGLang conversion configuration
    """
    config_file = 'configs/sglang_convert.json'

    try:
        print(f"📋 Using configuration file: {config_file}")
        print("💡 Hint: To customize conversion rules, edit the config file or create a new one\n")
        print(os.getcwd())
        config = load_config_from_file(str(config_file))
        converter = DashboardConverter(config)
        success = converter.convert()

        if success:
            print("\n🎉 SGLang Dashboard conversion successful!")

        sys.exit(0 if success else 1)

    except Exception as e:
        print(f"❌ Conversion failed: {e}", file=sys.stderr)
        import traceback
        traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()

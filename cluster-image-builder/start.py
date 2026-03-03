#!/usr/bin/env python3
import subprocess
import json
import sys
import os
import importlib

# Flags deprecated in Ray 2.53.0
DEPRECATED_FLAGS = {"--dashboard-grpc-port", "--dashboard-agent-grpc-port"}


def filter_deprecated_args(args):
    """Filter out deprecated Ray flags (--flag=value format)."""
    filtered = []
    for arg in args:
        flag_name = arg.split("=")[0]
        if flag_name in DEPRECATED_FLAGS:
            print(f"Filtering deprecated Ray flag: {arg}")
            continue
        filtered.append(arg)
    return filtered


def main():
    if len(sys.argv) < 1:
        print("Usage: {} [additional ray start arguments]".format(sys.argv[0]))
        sys.exit(1)

    additional_args = filter_deprecated_args(sys.argv[1:])

    accelerator_counts = {}
    accelerator_type = os.environ.get("ACCELERATOR_TYPE", "")
    if accelerator_type != "":
        acclerator = importlib.import_module(f"accelerator.{accelerator_type}")
        accelerator_counts = acclerator.get_accelerator_counts()

    resources_param = json.dumps(accelerator_counts)

    # Construct the ray start command with the head address and default parameters
    cmd = [
        "ray", "start",
        "--object-manager-port=8076",
        "--resources", resources_param
    ]

    # Append any additional arguments passed after the IP address
    cmd.extend(additional_args)

    print("Executing command:", " ".join(cmd))

    try:
        subprocess.run(cmd, check=True)
    except subprocess.CalledProcessError as e:
        print("Error executing ray start command:", e)
        sys.exit(1)

if __name__ == "__main__":
    main()
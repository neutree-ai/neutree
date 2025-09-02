#!/usr/bin/env python3
import subprocess
import json
import sys
import os
import importlib

def main():
    if len(sys.argv) < 1:
        print("Usage: {} [additional ray start arguments]".format(sys.argv[0]))
        sys.exit(1)

    additional_args = sys.argv[1:]

    accelerator_counts = {}
    accelerator_type = os.environ.get("ACCELERATOR_TYPE", "")
    accelerator_args = []
    if accelerator_type != "":
        acclerator = importlib.import_module(f"accelerator.{accelerator_type}")
        accelerator_counts = acclerator.get_accelerator_counts()
        if hasattr(acclerator, "get_start_args"):
            custom_args = acclerator.get_start_args()
            if custom_args:
                accelerator_args = custom_args if isinstance(custom_args, list) else [custom_args]

    resources_param = json.dumps(accelerator_counts)

    # Construct the ray start command with the head address and default parameters
    cmd = [
        "ray", "start",
        "--object-manager-port=8076",
        "--resources", resources_param
    ]

    cmd.extend(accelerator_args)

    cmd.extend(additional_args)

    print("Executing command:", " ".join(cmd))

    try:
        subprocess.run(cmd, check=True)
    except subprocess.CalledProcessError as e:
        print("Error executing ray start command:", e)
        sys.exit(1)

if __name__ == "__main__":
    main()
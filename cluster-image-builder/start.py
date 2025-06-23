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
    accelerator_type = os.environ.get("ACCELETRATOR_TYPE", "")
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

if __name__ == "__main__":
    main()
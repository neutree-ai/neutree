#!/usr/bin/env python3
import subprocess
import json
import sys
import os

def get_ascend_npu_names():
    """
    Call npu-smi to retrieve the names of all NPU devices.
    """
    try:
        output = subprocess.check_output(
            ["npu-smi", "info", "-m"],
            encoding="utf-8"
        )
        npu_names = []
        for index, line in enumerate(output.strip().splitlines()):
            if index == 0:
                continue
            if len(line.split()) != 5:
                continue
            npu_id,chip_id,chip_logic_id,chip_type,chip_name = line.split()
            npu_names.append(chip_type+chip_name)
        return npu_names
    except subprocess.CalledProcessError as e:
        print("Error calling npu-smi:", e)
        return []
    except FileNotFoundError:
        print("npu-smi not found, please ensure that the Ascend drivers are installed")
        return []    

def get_gpu_names():
    """
    Call nvidia-smi to retrieve the names of all GPUs.
    """
    try:
        output = subprocess.check_output(
            ["nvidia-smi", "--query-gpu=name", "--format=csv,noheader"],
            encoding="utf-8"
        )
        gpu_names = [line.strip() for line in output.strip().splitlines() if line.strip()]
        return gpu_names
    except subprocess.CalledProcessError as e:
        print("Error calling nvidia-smi:", e)
        return []
    except FileNotFoundError:
        print("nvidia-smi not found, please ensure that the NVIDIA drivers are installed")
        return []

def count_nvidia_accelerators(gpu_names):
    """
    Count the number of accelerator types based on the list of GPU names.
    """
    # Matching priority: longer markers first
    accelerator_markers = [
        ("A100-40G", "NVIDIA_A100_40G"),
        ("A100-80G", "NVIDIA_A100_80G"),
        ("V100", "NVIDIA_TESLA_V100"),
        ("P100", "NVIDIA_TESLA_P100"),
        ("T4", "NVIDIA_TESLA_T4"),
        ("P4", "NVIDIA_TESLA_P4"),
        ("K80", "NVIDIA_TESLA_K80"),
        ("A10G", "NVIDIA_TESLA_A10G"),
        ("L40S", "NVIDIA_L40S"),
        ("L4", "NVIDIA_L4"),
        ("L20", "NVIDIA_L20"),
        ("A100", "NVIDIA_A100"),
        ("H100", "NVIDIA_H100"),
        ("H200", "NVIDIA_H200"),
    ]

    counts = {label: 0 for _, label in accelerator_markers}
    for gpu in gpu_names:
        for marker, label in accelerator_markers:
            if marker in gpu:
                counts[label] += 1
                break
    # Keep only items with a count greater than 0
    return {k: v for k, v in counts.items() if v != 0}

def main():
    if len(sys.argv) < 2:
        print("Usage: {} <ray_head_ip> [additional ray start arguments]".format(sys.argv[0]))
        sys.exit(1)

    additional_args = sys.argv[1:]

    accelerator_counts = {}
    # Retrieve GPU names and accelerator counts
    gpu_names = get_gpu_names()
    accelerator_counts = count_nvidia_accelerators(gpu_names)

    npu_names = get_ascend_npu_names()
    for npu_name in npu_names:
        if "HUAWEI_"+npu_name not in accelerator_counts:
            accelerator_counts["HUAWEI_"+npu_name] = 1
        else:
            accelerator_counts["HUAWEI_"+npu_name] += 1
    # Convert the accelerator counts to a JSON string for the --resources parameter
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
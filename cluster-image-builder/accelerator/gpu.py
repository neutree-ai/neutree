import subprocess

def get_accelerator_counts():
    """
    Retrieve the number of GPUs and their names.
    """
    gpu_names = get_gpu_names()
    return count_nvidia_accelerators(gpu_names)

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
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
    accelerator_counts = {}
    for gpu in gpu_names:
        accelerator_type = gpu.replace(" ","")
        if not accelerator_type.startswith("NVIDIA_"):
            accelerator_type = "NVIDIA" + accelerator_type
        if accelerator_counts.get(accelerator_type) is None:
            accelerator_counts[accelerator_type] = 1
        else:
            accelerator_counts[accelerator_type] += 1
    return accelerator_counts
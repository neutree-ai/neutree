import subprocess


def get_accelerator_counts():
    """
    Get the number of AMD GPUs in the system.
    Tries rocminfo first, falls back to rocm-smi if rocminfo fails.
    """
    counts = _detect_via_rocminfo()
    if not counts:
        print("rocminfo detected no GPUs, trying rocm-smi fallback...")
        counts = _detect_via_rocm_smi()
    return counts


def _normalize_accelerator_type(name):
    """Normalize GPU name to accelerator type string."""
    accelerator_type = name.replace(" ", "_")
    if not accelerator_type.startswith("AMD_"):
        accelerator_type = "AMD_" + accelerator_type
    return accelerator_type


def _detect_via_rocminfo():
    """Detect AMD GPUs using rocminfo."""
    accelerator_counts = {}
    try:
        output = subprocess.check_output(
            ["rocminfo"],
            encoding="utf-8"
        )
        deviceList = []
        device = None
        for line in output.split("\n"):
            line = line.strip()
            if line.startswith("Agent "):
                if device is not None:
                    deviceList.append(device)
                device = {}
                continue
            if 'Marketing Name:' in line and device is not None:
                device["market_name"] = line.split('Marketing Name:')[1].strip()
            if 'Device Type' in line and device is not None:
                device["type"] = line.split('Device Type:')[1].strip()
        if device is not None:
            deviceList.append(device)
        for device in deviceList:
            if device.get("type") == "CPU":
                continue
            market_name = device.get("market_name")
            if not market_name:
                continue
            accelerator_type = _normalize_accelerator_type(market_name)
            accelerator_counts[accelerator_type] = accelerator_counts.get(accelerator_type, 0) + 1
    except subprocess.CalledProcessError as e:
        print("Error calling rocminfo:", e)
    except FileNotFoundError:
        print("rocminfo not found")
    return accelerator_counts


def _detect_via_rocm_smi():
    """Fallback: detect AMD GPUs using rocm-smi when rocminfo fails."""
    accelerator_counts = {}
    try:
        output = subprocess.check_output(
            ["rocm-smi", "--showproductname"],
            encoding="utf-8",
            stderr=subprocess.DEVNULL
        )
        for line in output.splitlines():
            if "Card series:" not in line:
                continue
            name = line.split("Card series:")[-1].strip()
            if not name:
                continue
            accelerator_type = _normalize_accelerator_type(name)
            accelerator_counts[accelerator_type] = accelerator_counts.get(accelerator_type, 0) + 1
    except subprocess.CalledProcessError as e:
        print("Error calling rocm-smi:", e)
    except FileNotFoundError:
        print("rocm-smi not found")
    return accelerator_counts

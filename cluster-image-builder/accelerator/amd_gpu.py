import subprocess

def get_accelerator_counts():
    """
    Get the number of AMD GPUs in the system.
    Returns:
        dict: The number of AMD GPUs.
    """
    accelerator_counts = {}
    try:
        output = subprocess.check_output(
            ["rocminfo"],
            encoding="utf-8"
        )
        lines = output.split("\n")
        deviceList = []
        device = None
        for line in lines:
            line = line.strip()
            if line.startswith("Agent "):
                # record last device info and start record new device
                if device is not None:
                    print(device)
                    deviceList.append(device)
                device = {}
                continue
            if 'Marketing Name:' in line and device is not None:
                device["market_name"] = line.split('Marketing Name:')[1].strip()

            if 'Device Type' in line and device is not None:
                device["type"] = line.split('Device Type:')[1].strip()
        # append last device
        deviceList.append(device)
        for device in deviceList:
            if device.get("type") is not None and  device.get("type") == "CPU":
                continue
            market_name = device.get("market_name")
            accelerator_type = market_name.replace(" ","_")
            if accelerator_counts.get(accelerator_type) is None:
                accelerator_counts[accelerator_type] = 1
            else:
                accelerator_counts[accelerator_type] += 1
    except subprocess.CalledProcessError as e:
        print("Error calling rocminfo:", e)
        return []
    except FileNotFoundError:
        print("rocminfo not found, please ensure that the AMD ROCM are installed")
        return []


    return accelerator_counts

import subprocess
import json

def get_accelerator_counts():
    """
    Get the number of Intel XPUs in the system.
    Returns:
        dict: The number of Intel XPUs.
    """
    accelerator_counts = {}
    try:
        output = subprocess.check_output(
            ["xpu-smi", "discovery", "-j"],
            encoding="utf-8"
        )
        
        # Parse JSON output
        data = json.loads(output)
        device_list = data.get("device_list", [])
        
        for device in device_list:
            device_name = device.get("device_name", "Unknown")
            device_type = device.get("device_type", "Unknown")
            
            # Only count GPU devices
            if device_type != "GPU":
                continue
                
            # Use device name as the accelerator type, replace spaces with underscores
            accelerator_type = device_name.replace(" ", "_").replace("(", "").replace(")", "").replace("[", "").replace("]", "")
            
            if accelerator_counts.get(accelerator_type) is None:
                accelerator_counts[accelerator_type] = 1
            else:
                accelerator_counts[accelerator_type] += 1
                
    except subprocess.CalledProcessError as e:
        print("Error calling xpu-smi:", e)
        return {}
    except FileNotFoundError:
        print("xpu-smi not found, please ensure that Intel XPU drivers are installed")
        return {}
    except json.JSONDecodeError as e:
        print("Error parsing xpu-smi output:", e)
        return {}

    return accelerator_counts

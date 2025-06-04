import socket
import os
import json
import fcntl
import errno
from pathlib import Path

def get_available_port(port_range_start: int = 30000, port_range_end: int = 32767) -> int:
    """
    Get available port
    Args:
        port_range_start: start port
        port_range_end: end port
    Returns:
        available port
    """
    port_dir = Path(os.path.expanduser("~/.neutree/ports"))
    port_dir.mkdir(parents=True, exist_ok=True)
    
    port_file = port_dir / "allocated_ports.json"
    lock_file = port_dir / "port_lock"
    
    if not port_file.exists():
        port_file.write_text("{}")
    if not lock_file.exists():
        lock_file.touch()
    
    with open(lock_file, "w") as f:
        try:
            fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
            
            try:
                with open(port_file, "r") as pf:
                    allocated_ports = json.load(pf)
            except (json.JSONDecodeError, FileNotFoundError):
                allocated_ports = {}
            
            for port, pid in list(allocated_ports.items()):
                try:
                    os.kill(pid, 0)
                except OSError as e:
                    if e.errno == errno.ESRCH:
                        del allocated_ports[port]
            
            current_pid = os.getpid()
            for port in range(port_range_start, port_range_end + 1):
                port_str = str(port)
                
                if port_str in allocated_ports and allocated_ports[port_str] == current_pid:
                    return port
                
                if port_str not in allocated_ports and is_port_available(port):
                    allocated_ports[port_str] = current_pid
                    with open(port_file, "w") as pf:
                        json.dump(allocated_ports, pf)
                    return port
            
            raise RuntimeError(f"No available ports in range {port_range_start}-{port_range_end}")
            
        finally:
            fcntl.flock(f, fcntl.LOCK_UN)

def is_port_available(port: int) -> bool:
    """
    Check if a port is available
    Args:
        port: port
    Returns:
        True if port is available, False otherwise
    """
    try:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
            s.settimeout(0.5)
            s.bind(("127.0.0.1", port))
            return True
    except (socket.error, OSError):
        return False
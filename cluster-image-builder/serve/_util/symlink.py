import os
from pathlib import Path

def create_symlinks(source_dir, target_dir):
    """
    create symlinks for all files and folders in the source directory to the target directory (using absolute paths)
    params:
    source_dir (str): source directory path
    target_dir (str): target directory path
    """
    # normalize path to absolute path
    source_dir = os.path.abspath(source_dir)
    target_dir = os.path.abspath(target_dir)
    
    # ensure source directory exists
    if not os.path.exists(source_dir):
        raise FileNotFoundError(f"source directory is not exist: {source_dir}")
    
    # ensure target directory exists
    if not os.path.exists(target_dir):
        os.makedirs(target_dir, exist_ok=True)
        print(f"create target directory: {target_dir}")
    
    # get all entries in source directory
    try:
        entries = os.listdir(source_dir)
    except PermissionError:
        raise PermissionError(f"Permission denied: {source_dir}")
    
    # create symlinks for all entries in source directory
    for entry in entries:
        source_entry = os.path.join(source_dir, entry)
        target_entry = os.path.join(target_dir, entry)
        
        # check target entry exists
        if os.path.exists(target_entry):
            if os.path.islink(target_entry):
                existing_link = os.readlink(target_entry)
                if os.path.abspath(existing_link) == os.path.abspath(source_entry):
                    print(f"skip exist symlink: {target_entry}")
                    continue
                else:
                    raise RuntimeError(f"target entry is exist and point to other: {target_entry}")
            else:
                raise RuntimeError(f"target entry is exist and not a symlink: {target_entry}")
        
        # create symlink
        try:
            os.symlink(source_entry, target_entry)
        except Exception as e:
            raise RuntimeError(f"failed to create symlink: {e}")

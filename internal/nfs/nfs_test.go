package nfs

import (
	"testing"

	"github.com/stretchr/testify/assert"
	kmount "k8s.io/utils/mount"
)

func TestGetNFSVersion(t *testing.T) {
	tests := []struct {
		name        string
		mountPoints []kmount.MountPoint
		device      string
		mountPoint  string
		wantVersion string
		wantErr     bool
	}{
		{
			name: "nfs4 type with vers=4.1 option returns 4.1",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs4",
					Opts:   []string{"rw", "relatime", "vers=4.1", "rsize=1048576"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "4.1",
		},
		{
			name: "nfs4 type with vers=4.2 option returns 4.2",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs4",
					Opts:   []string{"rw", "vers=4.2"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "4.2",
		},
		{
			name: "nfs4 type without vers option returns 4",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs4",
					Opts:   []string{"rw", "relatime"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "4",
		},
		{
			name: "nfs type with vers=4.1 option returns 4.1",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs",
					Opts:   []string{"rw", "relatime", "vers=4.1", "rsize=1048576"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "4.1",
		},
		{
			name: "nfs type with nfsvers option returns version",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs",
					Opts:   []string{"rw", "nfsvers=4.2"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "4.2",
		},
		{
			name: "nfs type without version option defaults to 3",
			mountPoints: []kmount.MountPoint{
				{
					Device: "server:/data",
					Path:   "/mnt/data",
					Type:   "nfs",
					Opts:   []string{"rw", "relatime", "rsize=1048576"},
				},
			},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantVersion: "3",
		},
		{
			name:        "mount not found returns error",
			mountPoints: []kmount.MountPoint{},
			device:      "server:/data",
			mountPoint:  "/mnt/data",
			wantErr:     true,
		},
		{
			name: "different device does not match",
			mountPoints: []kmount.MountPoint{
				{
					Device: "other-server:/data",
					Path:   "/mnt/data",
					Type:   "nfs",
					Opts:   []string{"rw", "vers=4.1"},
				},
			},
			device:     "server:/data",
			mountPoint: "/mnt/data",
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			origMountInterface := mountInterface
			mountInterface = kmount.NewFakeMounter(tt.mountPoints)
			defer func() { mountInterface = origMountInterface }()

			version, err := GetNFSVersion(tt.device, tt.mountPoint)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.wantVersion, version)
		})
	}
}

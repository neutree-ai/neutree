package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValidatePVCSpec(t *testing.T) {
	filesystemMode := corev1.PersistentVolumeFilesystem
	blockMode := corev1.PersistentVolumeBlock
	tests := []struct {
		name        string
		pvcSpec     *corev1.PersistentVolumeClaimSpec
		expectError bool
	}{
		{
			name: "Valid PVC Spec",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  &filesystemMode,
			},
			expectError: false,
		},
		{
			name: "Invalid PVC Spec - Missing Access Modes",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				VolumeMode: &filesystemMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Unsupported Access Mode",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
				VolumeMode:  &filesystemMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Multiple Access Modes",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany, corev1.ReadOnlyMany},
				VolumeMode:  &filesystemMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Missing Resource Requests",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:       &filesystemMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Missing Storage Request",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						// Missing corev1.ResourceStorage
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  &filesystemMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Volume Mode Block",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				VolumeMode:  &blockMode,
			},
			expectError: true,
		},
		{
			name: "Invalid PVC Spec - Missing Volume Mode",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				StorageClassName: pointer.String("fast-storage"),
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
			},
			expectError: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePVCSpec(*tt.pvcSpec)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestApplyDefault(t *testing.T) {
	filesystemMode := corev1.PersistentVolumeFilesystem
	tests := []struct {
		name            string
		pvcSpec         *corev1.PersistentVolumeClaimSpec
		expectedPVCSpec corev1.PersistentVolumeClaimSpec
	}{
		{
			name:    "Apply defaults to empty PVC Spec",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{},
			expectedPVCSpec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
				},
				VolumeMode: &filesystemMode,
			},
		},
		{
			name: "Do not override specified fields",
			pvcSpec: &corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					},
				},
				VolumeMode: &filesystemMode,
			},
			expectedPVCSpec: corev1.PersistentVolumeClaimSpec{
				AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteMany},
				Resources: corev1.VolumeResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("20Gi"),
					},
				},
				VolumeMode: &filesystemMode,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := applyDefault(*tt.pvcSpec)
			assert.Equal(t, tt.expectedPVCSpec, result)
		})
	}
}

func TestPvcStatus(t *testing.T) {
	tests := []struct {
		name          string
		inputPVC      *corev1.PersistentVolumeClaim
		inputObjects  []runtime.Object
		expectedReady bool
		expectError   bool
	}{
		{
			name: "PVC is bound and PV has sufficient capacity",
			inputPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
			},
			inputObjects: []runtime.Object{
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
					},
					Status: corev1.PersistentVolumeClaimStatus{
						Phase: corev1.ClaimBound,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("10Gi"),
							},
						},
						VolumeName: "test-pv",
					},
				},
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("20Gi"),
						},
					},
				},
			},
			expectedReady: true,
			expectError:   false,
		},
		{
			name: "PVC is not bound",
			inputPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
			},
			inputObjects: []runtime.Object{
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
					},
					Status: corev1.PersistentVolumeClaimStatus{
						Phase: corev1.ClaimPending,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("10Gi"),
							},
						},
						VolumeName: "test-pv",
					},
				},
			},
			expectedReady: false,
			expectError:   false,
		},
		{
			name: "PV has insufficient capacity",
			inputPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
			},
			inputObjects: []runtime.Object{
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
					},
					Status: corev1.PersistentVolumeClaimStatus{
						Phase: corev1.ClaimBound,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("10Gi"),
							},
						},
						VolumeName: "test-pv",
					},
				},
				&corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
					Spec: corev1.PersistentVolumeSpec{
						Capacity: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("5Gi"),
						},
					},
				},
			},
			expectedReady: false,
			expectError:   false,
		},
		{
			name: "Error retrieving PV",
			inputPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
				},
			},
			inputObjects: []runtime.Object{
				&corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "default",
					},
					Status: corev1.PersistentVolumeClaimStatus{
						Phase: corev1.ClaimBound,
					},
					Spec: corev1.PersistentVolumeClaimSpec{
						Resources: corev1.VolumeResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourceStorage: resource.MustParse("10Gi"),
							},
						},
						VolumeName: "nonexistent-pv",
					},
				},
			},
			expectedReady: false,
			expectError:   true,
		},
		{
			name: "Error retrieving PVC",
			inputPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "nonexistent-pvc",
					Namespace: "default",
				},
			},
			expectedReady: false,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := fake.NewFakeClient(tt.inputObjects...)
			ready, err := pvcStatus(context.TODO(), c, tt.inputPVC)
			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedReady, ready)
			}
		})
	}
}

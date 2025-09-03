package topdown

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	cloudv1beta1 "github.com/TKEColocation/tapestry/api/v1beta1"
)

func TestVirtualPVReconciler_Reconcile(t *testing.T) {
	// Helper function to add clusterID label to virtual PV
	addClusterIDLabel := func(pv *corev1.PersistentVolume) {
		if pv != nil && pv.Labels != nil {
			pv.Labels["tapestry.io/synced-by-test-cluster-id"] = cloudv1beta1.LabelValueTrue
		}
	}

	tests := []struct {
		name           string
		virtualPV      *corev1.PersistentVolume
		physicalPV     *corev1.PersistentVolume
		clusterBinding *cloudv1beta1.ClusterBinding
		expectedResult ctrl.Result
		expectedError  bool
	}{
		{
			name: "Virtual PV not found",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "PV not managed by Tapestry",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						"other-label": "value",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "PV not managed by this cluster",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy:              cloudv1beta1.LabelManagedByValue,
						"tapestry.io/synced-by-other-cluster-id": "true",
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pv-physical",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "PV has no physical name annotation",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "Physical PV doesn't exist - do nothing",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pv-physical",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "Physical PV exists and is valid",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pv-physical",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pv",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "Virtual PV is being deleted - physical PV exists",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pv-physical",
					},
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{cloudv1beta1.SyncedResourceFinalizer},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pv",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "Virtual PV is being deleted - physical PV doesn't exist",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pv-physical",
					},
					DeletionTimestamp: &metav1.Time{},
					Finalizers:        []string{cloudv1beta1.SyncedResourceFinalizer},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Add clusterID label to virtual PV if it exists and has managed-by label
			if tt.virtualPV != nil && tt.virtualPV.Labels != nil &&
				tt.virtualPV.Labels[cloudv1beta1.LabelManagedBy] == cloudv1beta1.LabelManagedByValue {
				addClusterIDLabel(tt.virtualPV)
			}

			// Setup scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = cloudv1beta1.AddToScheme(scheme)

			// Setup virtual client
			virtualObjs := []client.Object{}
			if tt.virtualPV != nil {
				virtualObjs = append(virtualObjs, tt.virtualPV)
			}
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Setup physical client
			physicalObjs := []client.Object{}
			if tt.physicalPV != nil {
				physicalObjs = append(physicalObjs, tt.physicalPV)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			// Setup physical k8s client
			physicalK8sClient := fake.NewSimpleClientset()
			if tt.physicalPV != nil {
				_, err := physicalK8sClient.CoreV1().PersistentVolumes().Create(context.TODO(), tt.physicalPV, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup reconciler
			reconciler := &VirtualPVReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    tt.clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}
			// Set clusterID manually for testing
			reconciler.clusterID = tt.clusterBinding.Spec.ClusterID

			// Create request
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name: "test-pv", // Use a default name for nil virtualPV case
				},
			}
			if tt.virtualPV != nil {
				req.Name = tt.virtualPV.Name
			}

			// Execute reconcile
			result, err := reconciler.Reconcile(context.Background(), req)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

func TestVirtualPVReconciler_checkPhysicalPVExists(t *testing.T) {
	tests := []struct {
		name           string
		physicalName   string
		physicalPV     *corev1.PersistentVolume
		clusterBinding *cloudv1beta1.ClusterBinding
		expectedExists bool
		expectedError  bool
	}{
		{
			name:         "Physical PV exists",
			physicalName: "test-pv-physical",
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:         "Physical PV doesn't exist",
			physicalName: "test-pv-physical",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedExists: false,
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = cloudv1beta1.AddToScheme(scheme)

			// Setup physical client
			physicalObjs := []client.Object{}
			if tt.physicalPV != nil {
				physicalObjs = append(physicalObjs, tt.physicalPV)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			// Setup physical k8s client
			physicalK8sClient := fake.NewSimpleClientset()
			if tt.physicalPV != nil {
				_, err := physicalK8sClient.CoreV1().PersistentVolumes().Create(context.TODO(), tt.physicalPV, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup reconciler
			reconciler := &VirtualPVReconciler{
				VirtualClient:     nil, // Not needed for this test
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    tt.clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			// Execute checkPhysicalPVExists
			exists, pv, err := reconciler.checkPhysicalPVExists(context.Background(), tt.physicalName)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedExists, exists)
			if tt.expectedExists {
				assert.NotNil(t, pv)
				assert.Equal(t, tt.physicalName, pv.Name)
			} else {
				assert.Nil(t, pv)
			}
		})
	}
}

func TestVirtualPVReconciler_validatePhysicalPV(t *testing.T) {
	tests := []struct {
		name          string
		virtualPV     *corev1.PersistentVolume
		physicalPV    *corev1.PersistentVolume
		expectedError bool
		errorContains string
	}{
		{
			name: "Valid physical PV",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
			},
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pv",
					},
				},
			},
			expectedError: false,
		},
		{
			name: "Physical PV not managed by Tapestry",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
			},
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						"other-label": "value",
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pv",
					},
				},
			},
			expectedError: true,
			errorContains: "is not managed by Tapestry",
		},
		{
			name: "Physical PV virtual name annotation doesn't match",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
				},
			},
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "different-pv",
					},
				},
			},
			expectedError: true,
			errorContains: "virtual name annotation does not point to current virtual PV",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup reconciler
			reconciler := &VirtualPVReconciler{
				Log: ctrl.Log.WithName("test"),
			}

			// Execute validatePhysicalPV
			err := reconciler.validatePhysicalPV(tt.virtualPV, tt.physicalPV)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVirtualPVReconciler_handleVirtualPVDeletion(t *testing.T) {
	tests := []struct {
		name           string
		virtualPV      *corev1.PersistentVolume
		physicalName   string
		physicalPV     *corev1.PersistentVolume
		clusterBinding *cloudv1beta1.ClusterBinding
		expectedResult ctrl.Result
		expectedError  bool
	}{
		{
			name: "Physical PV doesn't exist",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv",
					Finalizers: []string{cloudv1beta1.SyncedResourceFinalizer},
				},
			},
			physicalName: "test-pv-physical",
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
		{
			name: "Physical PV exists - should be deleted",
			virtualPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "test-pv",
					Finalizers: []string{cloudv1beta1.SyncedResourceFinalizer},
				},
			},
			physicalName: "test-pv-physical",
			physicalPV: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv-physical",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pv",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("1Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/tmp/test",
						},
					},
				},
			},
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedResult: ctrl.Result{},
			expectedError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = cloudv1beta1.AddToScheme(scheme)

			// Setup virtual client
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(tt.virtualPV).Build()

			// Setup physical client
			physicalObjs := []client.Object{}
			if tt.physicalPV != nil {
				physicalObjs = append(physicalObjs, tt.physicalPV)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			// Setup physical k8s client
			physicalK8sClient := fake.NewSimpleClientset()
			if tt.physicalPV != nil {
				_, err := physicalK8sClient.CoreV1().PersistentVolumes().Create(context.TODO(), tt.physicalPV, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup reconciler
			reconciler := &VirtualPVReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    tt.clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			// Execute handleVirtualPVDeletion
			physicalPVExists := tt.physicalPV != nil
			result, err := reconciler.handleVirtualPVDeletion(context.Background(), tt.virtualPV, tt.physicalName, physicalPVExists, tt.physicalPV)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

// TestVirtualPVReconciler_ClusterIDFunctionality tests clusterID related functionality
func TestVirtualPVReconciler_ClusterIDFunctionality(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	clusterBinding := &cloudv1beta1.ClusterBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-cluster",
		},
		Spec: cloudv1beta1.ClusterBindingSpec{
			ClusterID:      "test-cluster-id",
			MountNamespace: "test-cluster",
		},
	}

	t.Run("clusterID caching", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualPVReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
		}

		// Set clusterID directly for testing
		reconciler.clusterID = clusterBinding.Spec.ClusterID

		// Verify clusterID is cached
		assert.Equal(t, "test-cluster-id", reconciler.clusterID)
	})

	t.Run("removeSyncedResourceFinalizer with clusterID", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()

		reconciler := &VirtualPVReconciler{
			VirtualClient:  virtualClient,
			PhysicalClient: physicalClient,
			ClusterBinding: clusterBinding,
			Log:            ctrl.Log.WithName("test"),
			clusterID:      "test-cluster-id",
		}

		virtualPV := &corev1.PersistentVolume{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-pv",
				Finalizers: []string{
					"tapestry.io/finalizer-test-cluster-id",
					"other-finalizer",
				},
			},
		}

		// Add the PV to the client
		err := virtualClient.Create(context.Background(), virtualPV)
		require.NoError(t, err)

		// Test removing the clusterID finalizer
		result, err := reconciler.removeSyncedResourceFinalizer(context.Background(), virtualPV)
		require.NoError(t, err)
		assert.Equal(t, ctrl.Result{}, result)

		// Verify the clusterID finalizer is removed but other finalizer remains
		updatedPV := &corev1.PersistentVolume{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-pv"}, updatedPV)
		require.NoError(t, err)

		assert.NotContains(t, updatedPV.Finalizers, "tapestry.io/finalizer-test-cluster-id")
		assert.Contains(t, updatedPV.Finalizers, "other-finalizer")
	})
}

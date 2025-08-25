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

func TestVirtualPVCReconciler_Reconcile(t *testing.T) {
	tests := []struct {
		name           string
		virtualPVC     *corev1.PersistentVolumeClaim
		physicalPVC    *corev1.PersistentVolumeClaim
		clusterBinding *cloudv1beta1.ClusterBinding
		expectedResult ctrl.Result
		expectedError  bool
	}{
		{
			name: "Virtual PVC not found",
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
			name: "PVC not managed by Tapestry",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Labels: map[string]string{
						"other-label": "value",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
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
			name: "Create physical PVC when it doesn't exist",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pvc-physical",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
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
			name: "Physical PVC exists and is valid",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationPhysicalName: "test-pvc-physical",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
						},
					},
				},
			},
			physicalPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc-physical",
					Namespace: "physical-namespace",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pvc",
					},
				},
				Spec: corev1.PersistentVolumeClaimSpec{
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					Resources: corev1.VolumeResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceStorage: resource.MustParse("1Gi"),
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
			virtualObjs := []client.Object{}
			if tt.virtualPVC != nil {
				virtualObjs = append(virtualObjs, tt.virtualPVC)
			}
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Setup physical client
			physicalObjs := []client.Object{}
			if tt.physicalPVC != nil {
				physicalObjs = append(physicalObjs, tt.physicalPVC)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			// Setup physical k8s client
			physicalK8sClient := fake.NewSimpleClientset()
			if tt.physicalPVC != nil {
				_, err := physicalK8sClient.CoreV1().PersistentVolumeClaims(tt.physicalPVC.Namespace).Create(context.TODO(), tt.physicalPVC, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup reconciler
			reconciler := &VirtualPVCReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    tt.clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			// Create request
			req := ctrl.Request{
				NamespacedName: types.NamespacedName{
					Name:      "test-pvc",
					Namespace: "default",
				},
			}

			// Execute reconcile
			result, err := reconciler.Reconcile(context.TODO(), req)

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

func TestVirtualPVCReconciler_validatePhysicalPVC(t *testing.T) {
	tests := []struct {
		name           string
		virtualPVC     *corev1.PersistentVolumeClaim
		physicalPVC    *corev1.PersistentVolumeClaim
		expectedError  bool
		expectedErrMsg string
	}{
		{
			name: "Valid physical PVC",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pvc",
				},
			},
			physicalPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pvc",
					},
				},
			},
			expectedError: false,
		},
		{
			name: "Physical PVC not managed by Tapestry",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pvc",
				},
			},
			physicalPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc-physical",
					Namespace: "physical-namespace",
					Labels: map[string]string{
						"other-label": "value",
					},
					Annotations: map[string]string{
						cloudv1beta1.AnnotationVirtualName: "test-pvc",
					},
				},
			},
			expectedError:  true,
			expectedErrMsg: "physical PVC physical-namespace/test-pvc-physical is not managed by Tapestry",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reconciler := &VirtualPVCReconciler{
				Log: ctrl.Log.WithName("test"),
			}

			err := reconciler.validatePhysicalPVC(tt.virtualPVC, tt.physicalPVC)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.expectedErrMsg)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestVirtualPVCReconciler_handleVirtualPVCDeletion(t *testing.T) {
	tests := []struct {
		name              string
		virtualPVC        *corev1.PersistentVolumeClaim
		physicalPVC       *corev1.PersistentVolumeClaim
		physicalPVCExists bool
		clusterBinding    *cloudv1beta1.ClusterBinding
		expectedError     bool
	}{
		{
			name: "Physical PVC doesn't exist",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Finalizers: []string{
						cloudv1beta1.SyncedResourceFinalizer,
					},
				},
			},
			physicalPVCExists: false,
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedError: false,
		},
		{
			name: "Physical PVC exists and should be deleted",
			virtualPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "default",
					Finalizers: []string{
						cloudv1beta1.SyncedResourceFinalizer,
					},
				},
			},
			physicalPVC: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc-physical",
					Namespace: "physical-namespace",
					UID:       "test-uid",
				},
			},
			physicalPVCExists: true,
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:      "test-cluster-id",
					MountNamespace: "physical-namespace",
				},
			},
			expectedError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = cloudv1beta1.AddToScheme(scheme)

			// Setup virtual client
			virtualObjs := []client.Object{}
			if tt.virtualPVC != nil {
				virtualObjs = append(virtualObjs, tt.virtualPVC)
			}
			virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(virtualObjs...).Build()

			// Setup physical client
			physicalObjs := []client.Object{}
			if tt.physicalPVC != nil {
				physicalObjs = append(physicalObjs, tt.physicalPVC)
			}
			physicalClient := fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(physicalObjs...).Build()

			// Setup physical k8s client
			physicalK8sClient := fake.NewSimpleClientset()
			if tt.physicalPVC != nil {
				_, err := physicalK8sClient.CoreV1().PersistentVolumeClaims(tt.physicalPVC.Namespace).Create(context.TODO(), tt.physicalPVC, metav1.CreateOptions{})
				require.NoError(t, err)
			}

			// Setup reconciler
			reconciler := &VirtualPVCReconciler{
				VirtualClient:     virtualClient,
				PhysicalClient:    physicalClient,
				PhysicalK8sClient: physicalK8sClient,
				Scheme:            scheme,
				ClusterBinding:    tt.clusterBinding,
				Log:               ctrl.Log.WithName("test"),
			}

			// Execute handleVirtualPVCDeletion
			physicalName := "test-pvc-physical"
			result, err := reconciler.handleVirtualPVCDeletion(context.TODO(), tt.virtualPVC, physicalName, tt.physicalPVCExists, tt.physicalPVC)

			// Assert results
			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, ctrl.Result{}, result)
			}
		})
	}
}

func TestVirtualPVCReconciler_SetupWithManager(t *testing.T) {
	// Setup scheme
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = cloudv1beta1.AddToScheme(scheme)

	// Create virtual and physical managers
	virtualManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	physicalManager, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme: scheme,
	})
	require.NoError(t, err)

	// Create reconciler
	reconciler := &VirtualPVCReconciler{
		VirtualClient:  virtualManager.GetClient(),
		PhysicalClient: physicalManager.GetClient(),
		Scheme:         scheme,
		ClusterBinding: &cloudv1beta1.ClusterBinding{
			ObjectMeta: metav1.ObjectMeta{
				Name: "test-cluster",
			},
			Spec: cloudv1beta1.ClusterBindingSpec{
				ClusterID:      "test-cluster-id",
				MountNamespace: "physical-namespace",
			},
		},
		Log: ctrl.Log.WithName("test"),
	}

	// Test SetupWithManager
	err = reconciler.SetupWithManager(virtualManager, physicalManager)
	assert.NoError(t, err)
}

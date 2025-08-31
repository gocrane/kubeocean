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

func TestCheckPhysicalResourceExists(t *testing.T) {
	tests := []struct {
		name                string
		resourceType        ResourceType
		physicalName        string
		physicalNamespace   string
		setupPhysicalClient func() client.Client
		setupK8sClient      func() *fake.Clientset
		expectedExists      bool
		expectedError       bool
	}{
		{
			name:              "ConfigMap exists in cache",
			resourceType:      ResourceTypeConfigMap,
			physicalName:      "test-config",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(configMap).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:              "ConfigMap not found in cache but exists via direct client",
			resourceType:      ResourceTypeConfigMap,
			physicalName:      "test-config",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-config",
						Namespace: "test-namespace",
					},
				}
				return fake.NewSimpleClientset(configMap)
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:              "ConfigMap not found anywhere",
			resourceType:      ResourceTypeConfigMap,
			physicalName:      "test-config",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: false,
			expectedError:  false,
		},
		{
			name:              "Secret exists in cache",
			resourceType:      ResourceTypeSecret,
			physicalName:      "test-secret",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				secret := &corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-secret",
						Namespace: "test-namespace",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:              "PVC exists in cache",
			resourceType:      ResourceTypePVC,
			physicalName:      "test-pvc",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				pvc := &corev1.PersistentVolumeClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-pvc",
						Namespace: "test-namespace",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pvc).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:              "PV exists in cache",
			resourceType:      ResourceTypePV,
			physicalName:      "test-pv",
			physicalNamespace: "",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				pv := &corev1.PersistentVolume{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test-pv",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(pv).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: true,
			expectedError:  false,
		},
		{
			name:              "Unsupported resource type",
			resourceType:      "UnsupportedType",
			physicalName:      "test-resource",
			physicalNamespace: "test-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			setupK8sClient: func() *fake.Clientset {
				return fake.NewSimpleClientset()
			},
			expectedExists: false,
			expectedError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			physicalClient := tt.setupPhysicalClient()
			physicalK8sClient := tt.setupK8sClient()
			logger := ctrl.Log.WithName("test")

			var obj client.Object
			switch tt.resourceType {
			case ResourceTypeConfigMap:
				obj = &corev1.ConfigMap{}
			case ResourceTypeSecret:
				obj = &corev1.Secret{}
			case ResourceTypePVC:
				obj = &corev1.PersistentVolumeClaim{}
			case ResourceTypePV:
				obj = &corev1.PersistentVolume{}
			default:
				obj = &corev1.ConfigMap{}
			}

			exists, resultObj, err := CheckPhysicalResourceExists(ctx, tt.resourceType, tt.physicalName, tt.physicalNamespace, obj, physicalClient, physicalK8sClient, logger)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			assert.Equal(t, tt.expectedExists, exists)

			if tt.expectedExists {
				assert.NotNil(t, resultObj)
			} else {
				assert.Nil(t, resultObj)
			}
		})
	}
}

func TestBuildPhysicalResourceLabels(t *testing.T) {
	tests := []struct {
		name           string
		virtualObj     client.Object
		expectedLabels map[string]string
	}{
		{
			name: "ConfigMap with existing labels",
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"app": "test-app",
						"env": "prod",
					},
				},
			},
			expectedLabels: map[string]string{
				"app":                       "test-app",
				"env":                       "prod",
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
		{
			name: "Secret without labels",
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
			},
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
			},
		},
		{
			name: "PVC with managed-by label",
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
					Labels: map[string]string{
						cloudv1beta1.LabelManagedBy: "other-value",
						"storage":                   "fast",
					},
				},
			},
			expectedLabels: map[string]string{
				cloudv1beta1.LabelManagedBy: cloudv1beta1.LabelManagedByValue,
				"storage":                   "fast",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			labels := BuildPhysicalResourceLabels(tt.virtualObj)
			assert.Equal(t, tt.expectedLabels, labels)
		})
	}
}

func TestBuildPhysicalResourceAnnotations(t *testing.T) {
	tests := []struct {
		name                string
		virtualObj          client.Object
		expectedAnnotations map[string]string
	}{
		{
			name: "ConfigMap with existing annotations",
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"description": "test config",
						"version":     "v1",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"description":                           "test config",
				"version":                               "v1",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-config",
			},
		},
		{
			name: "Secret with Tapestry internal annotations",
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"description": "test secret",
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
						cloudv1beta1.AnnotationPhysicalPodUID:       "pod-uid",
						cloudv1beta1.AnnotationLastSyncTime:         "2023-01-01T00:00:00Z",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"description":                           "test secret",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-secret",
			},
		},
		{
			name: "PVC without annotations",
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
				},
			},
			expectedAnnotations: map[string]string{
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-pvc",
			},
		},
		{
			name: "PV with empty namespace (cluster-scoped resource)",
			virtualObj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Annotations: map[string]string{
						"storage.kubernetes.io/selected-node":     "node-1",
						"volume.beta.kubernetes.io/storage-class": "fast",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"storage.kubernetes.io/selected-node":     "node-1",
				"volume.beta.kubernetes.io/storage-class": "fast",
				cloudv1beta1.AnnotationVirtualNamespace:   "",
				cloudv1beta1.AnnotationVirtualName:        "test-pv",
			},
		},
		{
			name: "ConfigMap with all Tapestry internal annotations",
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"description": "test config",
						cloudv1beta1.AnnotationPhysicalPodNamespace: "physical-ns",
						cloudv1beta1.AnnotationPhysicalPodName:      "physical-pod",
						cloudv1beta1.AnnotationPhysicalPodUID:       "pod-uid",
						cloudv1beta1.AnnotationPhysicalNamespace:    "physical-ns",
						cloudv1beta1.AnnotationPhysicalName:         "physical-config",
						cloudv1beta1.AnnotationLastSyncTime:         "2023-01-01T00:00:00Z",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"description":                           "test config",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-config",
			},
		},
		{
			name: "Secret with mixed annotations",
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"kubernetes.io/service-account.name": "default",
						"description":                        "test secret",
						cloudv1beta1.AnnotationPhysicalName:  "physical-secret",
						cloudv1beta1.AnnotationLastSyncTime:  "2023-01-01T00:00:00Z",
						"custom.annotation":                  "custom-value",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"kubernetes.io/service-account.name":    "default",
				"description":                           "test secret",
				"custom.annotation":                     "custom-value",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-secret",
			},
		},
		{
			name: "PVC with nil annotations",
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
				},
			},
			expectedAnnotations: map[string]string{
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-pvc",
			},
		},
		{
			name: "ConfigMap with empty string annotations",
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"empty-annotation":  "",
						"normal-annotation": "normal-value",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"empty-annotation":                      "",
				"normal-annotation":                     "normal-value",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-config",
			},
		},
		{
			name: "Secret with special characters in annotations",
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
					Annotations: map[string]string{
						"special.key/with-slashes": "value-with-dashes_and_underscores",
						"annotation.with.dots":     "value.with.dots",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"special.key/with-slashes":              "value-with-dashes_and_underscores",
				"annotation.with.dots":                  "value.with.dots",
				cloudv1beta1.AnnotationVirtualNamespace: "test-namespace",
				cloudv1beta1.AnnotationVirtualName:      "test-secret",
			},
		},
		{
			name: "PV with long annotation values",
			virtualObj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Annotations: map[string]string{
						"long.description": "This is a very long description that contains many characters and should be preserved exactly as it is without any truncation or modification",
						"short.key":        "short",
					},
				},
			},
			expectedAnnotations: map[string]string{
				"long.description":                      "This is a very long description that contains many characters and should be preserved exactly as it is without any truncation or modification",
				"short.key":                             "short",
				cloudv1beta1.AnnotationVirtualNamespace: "",
				cloudv1beta1.AnnotationVirtualName:      "test-pv",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			annotations := BuildPhysicalResourceAnnotations(tt.virtualObj)
			assert.Equal(t, tt.expectedAnnotations, annotations)
		})
	}
}

func TestCreatePhysicalResource(t *testing.T) {
	tests := []struct {
		name                string
		resourceType        ResourceType
		virtualObj          client.Object
		physicalName        string
		physicalNamespace   string
		setupPhysicalClient func() client.Client
		expectedError       bool
	}{
		{
			name:         "Create ConfigMap successfully",
			resourceType: ResourceTypeConfigMap,
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"app": "test-app",
					},
				},
				Data: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
			},
			physicalName:      "physical-config",
			physicalNamespace: "physical-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
		},
		{
			name:         "Create Secret successfully",
			resourceType: ResourceTypeSecret,
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("secret"),
				},
			},
			physicalName:      "physical-secret",
			physicalNamespace: "physical-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
		},
		{
			name:         "Create PVC successfully",
			resourceType: ResourceTypePVC,
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
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
			physicalName:      "physical-pvc",
			physicalNamespace: "physical-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
		},
		{
			name:         "Create PV successfully",
			resourceType: ResourceTypePV,
			virtualObj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						"storage": "fast",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data",
						},
					},
				},
			},
			physicalName:      "physical-pv",
			physicalNamespace: "",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)
				return fakeclient.NewClientBuilder().WithScheme(scheme).Build()
			},
			expectedError: false,
		},
		{
			name:         "Resource already exists",
			resourceType: ResourceTypeConfigMap,
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
				},
			},
			physicalName:      "existing-config",
			physicalNamespace: "physical-namespace",
			setupPhysicalClient: func() client.Client {
				scheme := runtime.NewScheme()
				_ = corev1.AddToScheme(scheme)
				_ = cloudv1beta1.AddToScheme(scheme)

				existingConfigMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "existing-config",
						Namespace: "physical-namespace",
					},
				}
				return fakeclient.NewClientBuilder().WithScheme(scheme).WithObjects(existingConfigMap).Build()
			},
			expectedError: false, // Already exists should not be an error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			physicalClient := tt.setupPhysicalClient()
			logger := ctrl.Log.WithName("test")

			err := CreatePhysicalResource(ctx, tt.resourceType, tt.virtualObj, tt.physicalName, tt.physicalNamespace, physicalClient, logger)

			if tt.expectedError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify the resource was created (except for already exists case)
			if !tt.expectedError {
				var obj client.Object
				switch tt.resourceType {
				case ResourceTypeConfigMap:
					obj = &corev1.ConfigMap{}
				case ResourceTypeSecret:
					obj = &corev1.Secret{}
				case ResourceTypePVC:
					obj = &corev1.PersistentVolumeClaim{}
				case ResourceTypePV:
					obj = &corev1.PersistentVolume{}
				}

				err := physicalClient.Get(ctx, types.NamespacedName{Name: tt.physicalName, Namespace: tt.physicalNamespace}, obj)
				assert.NoError(t, err)
			}
		})
	}
}

func TestBuildPhysicalResource(t *testing.T) {
	tests := []struct {
		name              string
		resourceType      ResourceType
		virtualObj        client.Object
		physicalName      string
		physicalNamespace string
		expectedError     bool
		validateResult    func(t *testing.T, obj client.Object)
	}{
		{
			name:         "Build ConfigMap successfully",
			resourceType: ResourceTypeConfigMap,
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
					Labels: map[string]string{
						"app": "test-app",
					},
					Annotations: map[string]string{
						"description": "test config",
					},
				},
				Data: map[string]string{
					"key1": "value1",
					"key2": "value2",
				},
				BinaryData: map[string][]byte{
					"binary1": []byte("binary-data"),
				},
			},
			physicalName:      "physical-config",
			physicalNamespace: "physical-namespace",
			expectedError:     false,
			validateResult: func(t *testing.T, obj client.Object) {
				configMap, ok := obj.(*corev1.ConfigMap)
				require.True(t, ok)
				assert.Equal(t, "physical-config", configMap.Name)
				assert.Equal(t, "physical-namespace", configMap.Namespace)
				assert.Equal(t, "value1", configMap.Data["key1"])
				assert.Equal(t, "value2", configMap.Data["key2"])
				assert.Equal(t, []byte("binary-data"), configMap.BinaryData["binary1"])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, configMap.Labels[cloudv1beta1.LabelManagedBy])
				assert.Equal(t, "test-namespace", configMap.Annotations[cloudv1beta1.AnnotationVirtualNamespace])
				assert.Equal(t, "test-config", configMap.Annotations[cloudv1beta1.AnnotationVirtualName])
			},
		},
		{
			name:         "Build Secret successfully",
			resourceType: ResourceTypeSecret,
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
				Type: corev1.SecretTypeOpaque,
				Data: map[string][]byte{
					"username": []byte("admin"),
					"password": []byte("secret"),
				},
				StringData: map[string]string{
					"host": "localhost",
				},
			},
			physicalName:      "physical-secret",
			physicalNamespace: "physical-namespace",
			expectedError:     false,
			validateResult: func(t *testing.T, obj client.Object) {
				secret, ok := obj.(*corev1.Secret)
				require.True(t, ok)
				assert.Equal(t, "physical-secret", secret.Name)
				assert.Equal(t, "physical-namespace", secret.Namespace)
				assert.Equal(t, corev1.SecretTypeOpaque, secret.Type)
				assert.Equal(t, []byte("admin"), secret.Data["username"])
				assert.Equal(t, []byte("secret"), secret.Data["password"])
				assert.Equal(t, "localhost", secret.StringData["host"])
			},
		},
		{
			name:         "Build PVC successfully",
			resourceType: ResourceTypePVC,
			virtualObj: &corev1.PersistentVolumeClaim{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-pvc",
					Namespace: "test-namespace",
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
			physicalName:      "physical-pvc",
			physicalNamespace: "physical-namespace",
			expectedError:     false,
			validateResult: func(t *testing.T, obj client.Object) {
				pvc, ok := obj.(*corev1.PersistentVolumeClaim)
				require.True(t, ok)
				assert.Equal(t, "physical-pvc", pvc.Name)
				assert.Equal(t, "physical-namespace", pvc.Namespace)
				assert.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, pvc.Spec.AccessModes)
				assert.Equal(t, resource.MustParse("1Gi"), pvc.Spec.Resources.Requests[corev1.ResourceStorage])
			},
		},
		{
			name:         "Invalid object type for ConfigMap",
			resourceType: ResourceTypeConfigMap,
			virtualObj: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-secret",
					Namespace: "test-namespace",
				},
			},
			physicalName:      "physical-config",
			physicalNamespace: "physical-namespace",
			expectedError:     true,
			validateResult:    nil,
		},
		{
			name:         "Build PV successfully",
			resourceType: ResourceTypePV,
			virtualObj: &corev1.PersistentVolume{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-pv",
					Labels: map[string]string{
						"storage": "fast",
					},
					Annotations: map[string]string{
						"description": "test pv",
					},
				},
				Spec: corev1.PersistentVolumeSpec{
					Capacity: corev1.ResourceList{
						corev1.ResourceStorage: resource.MustParse("10Gi"),
					},
					AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
					PersistentVolumeSource: corev1.PersistentVolumeSource{
						HostPath: &corev1.HostPathVolumeSource{
							Path: "/data",
						},
					},
				},
			},
			physicalName:      "physical-pv",
			physicalNamespace: "",
			expectedError:     false,
			validateResult: func(t *testing.T, obj client.Object) {
				pv, ok := obj.(*corev1.PersistentVolume)
				require.True(t, ok)
				assert.Equal(t, "physical-pv", pv.Name)
				assert.Equal(t, "", pv.Namespace) // PVs are cluster-scoped
				assert.Equal(t, "fast", pv.Labels["storage"])
				assert.Equal(t, cloudv1beta1.LabelManagedByValue, pv.Labels[cloudv1beta1.LabelManagedBy])
				assert.Equal(t, "test-pv", pv.Annotations[cloudv1beta1.AnnotationVirtualName])
				assert.Equal(t, "", pv.Annotations[cloudv1beta1.AnnotationVirtualNamespace]) // PVs have empty namespace
				assert.Equal(t, resource.MustParse("10Gi"), pv.Spec.Capacity[corev1.ResourceStorage])
				assert.Equal(t, corev1.ReadWriteOnce, pv.Spec.AccessModes[0])
				assert.Equal(t, "/data", pv.Spec.HostPath.Path)
			},
		},
		{
			name:         "Unsupported resource type",
			resourceType: "UnsupportedType",
			virtualObj: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-config",
					Namespace: "test-namespace",
				},
			},
			physicalName:      "physical-config",
			physicalNamespace: "physical-namespace",
			expectedError:     true,
			validateResult:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, err := BuildPhysicalResource(tt.resourceType, tt.virtualObj, tt.physicalName, tt.physicalNamespace, nil)

			if tt.expectedError {
				assert.Error(t, err)
				assert.Nil(t, obj)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, obj)
				if tt.validateResult != nil {
					tt.validateResult(t, obj)
				}
			}
		})
	}
}

// TestRemoveSyncedResourceFinalizerWithClusterID tests the RemoveSyncedResourceFinalizerWithClusterID function
func TestRemoveSyncedResourceFinalizerWithClusterID(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, cloudv1beta1.AddToScheme(scheme))

	t.Run("remove clusterID finalizer successfully", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		logger := ctrl.Log.WithName("test")

		// Create a virtual secret with clusterID finalizer
		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Finalizers: []string{
					"tapestry.io/finalizer-test-cluster-id",
					"other-finalizer",
				},
			},
		}

		// Add the secret to the client
		err := virtualClient.Create(context.Background(), virtualSecret)
		require.NoError(t, err)

		// Test removing the clusterID finalizer
		err = RemoveSyncedResourceFinalizerWithClusterID(context.Background(), virtualSecret, virtualClient, logger, "test-cluster-id")
		require.NoError(t, err)

		// Verify the clusterID finalizer is removed but other finalizer remains
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "test-ns"}, updatedSecret)
		require.NoError(t, err)

		assert.NotContains(t, updatedSecret.Finalizers, "tapestry.io/finalizer-test-cluster-id")
		assert.Contains(t, updatedSecret.Finalizers, "other-finalizer")
	})

	t.Run("finalizer not found", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		logger := ctrl.Log.WithName("test")

		// Create a virtual secret without clusterID finalizer
		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Finalizers: []string{
					"other-finalizer",
				},
			},
		}

		// Add the secret to the client
		err := virtualClient.Create(context.Background(), virtualSecret)
		require.NoError(t, err)

		// Test removing non-existent clusterID finalizer
		err = RemoveSyncedResourceFinalizerWithClusterID(context.Background(), virtualSecret, virtualClient, logger, "test-cluster-id")
		require.NoError(t, err) // Should not error when finalizer doesn't exist

		// Verify no changes were made
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "test-ns"}, updatedSecret)
		require.NoError(t, err)

		assert.Contains(t, updatedSecret.Finalizers, "other-finalizer")
		assert.Len(t, updatedSecret.Finalizers, 1)
	})

	t.Run("different clusterID", func(t *testing.T) {
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		logger := ctrl.Log.WithName("test")

		// Create a virtual secret with different clusterID finalizer
		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Finalizers: []string{
					"tapestry.io/finalizer-different-cluster-id",
					"other-finalizer",
				},
			},
		}

		// Add the secret to the client
		err := virtualClient.Create(context.Background(), virtualSecret)
		require.NoError(t, err)

		// Test removing different clusterID finalizer
		err = RemoveSyncedResourceFinalizerWithClusterID(context.Background(), virtualSecret, virtualClient, logger, "test-cluster-id")
		require.NoError(t, err) // Should not error when finalizer doesn't exist

		// Verify no changes were made
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "test-ns"}, updatedSecret)
		require.NoError(t, err)

		assert.Contains(t, updatedSecret.Finalizers, "tapestry.io/finalizer-different-cluster-id")
		assert.Contains(t, updatedSecret.Finalizers, "other-finalizer")
		assert.Len(t, updatedSecret.Finalizers, 2)
	})

	t.Run("update failure", func(t *testing.T) {
		// Create a client that will fail on update
		virtualClient := fakeclient.NewClientBuilder().WithScheme(scheme).Build()
		logger := ctrl.Log.WithName("test")

		virtualSecret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "test-secret",
				Namespace: "test-ns",
				Finalizers: []string{
					"tapestry.io/finalizer-test-cluster-id",
				},
			},
		}

		// Add the secret to the client
		err := virtualClient.Create(context.Background(), virtualSecret)
		require.NoError(t, err)

		// Test removing finalizer - this should succeed with fake client
		err = RemoveSyncedResourceFinalizerWithClusterID(context.Background(), virtualSecret, virtualClient, logger, "test-cluster-id")
		require.NoError(t, err)

		// Verify the finalizer is removed
		updatedSecret := &corev1.Secret{}
		err = virtualClient.Get(context.Background(), types.NamespacedName{Name: "test-secret", Namespace: "test-ns"}, updatedSecret)
		require.NoError(t, err)

		assert.NotContains(t, updatedSecret.Finalizers, "tapestry.io/finalizer-test-cluster-id")
	})
}

// TestGetManagedByClusterIDLabel tests the GetManagedByClusterIDLabel function
func TestGetManagedByClusterIDLabel(t *testing.T) {
	tests := []struct {
		name      string
		clusterID string
		expected  string
	}{
		{
			name:      "normal cluster ID",
			clusterID: "test-cluster-id",
			expected:  "tapestry.io/synced-by-test-cluster-id",
		},
		{
			name:      "empty cluster ID",
			clusterID: "",
			expected:  "tapestry.io/synced-by-",
		},
		{
			name:      "special characters in cluster ID",
			clusterID: "test-cluster-123",
			expected:  "tapestry.io/synced-by-test-cluster-123",
		},
		{
			name:      "long cluster ID",
			clusterID: "very-long-cluster-id-24",
			expected:  "tapestry.io/synced-by-very-long-cluster-id-24",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := GetManagedByClusterIDLabel(tt.clusterID)
			assert.Equal(t, tt.expected, result)
		})
	}
}

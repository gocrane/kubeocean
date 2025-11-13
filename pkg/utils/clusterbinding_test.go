/*
Copyright 2025 The Kubeocean Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"context"
	"testing"

	cloudv1beta1 "github.com/gocrane/kubeocean/api/v1beta1"
	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestIsRunningDaemonsetByDefault(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = cloudv1beta1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name               string
		client             client.Client
		clusterBindingName string
		clusterBinding     *cloudv1beta1.ClusterBinding
		expectedResult     bool
		expectedError      bool
		errorContains      string
	}{
		{
			name:               "nil client should return error",
			client:             nil,
			clusterBindingName: "test-cluster-binding",
			expectedResult:     false,
			expectedError:      true,
			errorContains:      "client is nil",
		},
		{
			name:               "empty clusterBindingName should return error",
			client:             fake.NewClientBuilder().WithScheme(scheme).Build(),
			clusterBindingName: "",
			expectedResult:     false,
			expectedError:      true,
			errorContains:      "clusterBindingName is empty",
		},
		{
			name:               "clusterBinding not found should return error",
			client:             fake.NewClientBuilder().WithScheme(scheme).Build(),
			clusterBindingName: "non-existent",
			expectedResult:     false,
			expectedError:      true,
		},
		{
			name:   "clusterBinding with RunningDaemonsetByDefault=true should return true",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:                 "test-cluster-id",
					RunningDaemonsetByDefault: true,
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "default",
					},
				},
			},
			clusterBindingName: "test-cluster-binding",
			expectedResult:     true,
			expectedError:      false,
		},
		{
			name:   "clusterBinding with RunningDaemonsetByDefault=false should return false",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID:                 "test-cluster-id",
					RunningDaemonsetByDefault: false,
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "default",
					},
				},
			},
			clusterBindingName: "test-cluster-binding",
			expectedResult:     false,
			expectedError:      false,
		},
		{
			name:   "clusterBinding with RunningDaemonsetByDefault not set (default false) should return false",
			client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			clusterBinding: &cloudv1beta1.ClusterBinding{
				ObjectMeta: metav1.ObjectMeta{
					Name: "test-cluster-binding",
				},
				Spec: cloudv1beta1.ClusterBindingSpec{
					ClusterID: "test-cluster-id",
					SecretRef: corev1.SecretReference{
						Name:      "test-secret",
						Namespace: "default",
					},
				},
			},
			clusterBindingName: "test-cluster-binding",
			expectedResult:     false,
			expectedError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			client := tt.client

			// Create clusterBinding in fake client if provided
			if tt.clusterBinding != nil {
				client = fake.NewClientBuilder().WithScheme(scheme).WithObjects(tt.clusterBinding).Build()
			}

			result, err := IsRunningDaemonsetByDefault(ctx, client, tt.clusterBindingName)

			if tt.expectedError {
				assert.Error(t, err)
				if tt.errorContains != "" {
					assert.Contains(t, err.Error(), tt.errorContains)
				}
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedResult, result)
		})
	}
}

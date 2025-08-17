package utils

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMatchesSelector(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-node",
			Labels: map[string]string{
				"kubernetes.io/os": "linux",
				"node-type":        "worker",
			},
		},
	}

	tests := []struct {
		name         string
		nodeSelector *corev1.NodeSelector
		expected     bool
	}{
		{
			name:         "nil selector matches all",
			nodeSelector: nil,
			expected:     true,
		},
		{
			name:         "empty selector matches all",
			nodeSelector: &corev1.NodeSelector{},
			expected:     true,
		},
		{
			name: "empty term matches none",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{}},
			},
			expected: false,
		},
		{
			name: "matching label",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "kubernetes.io/os",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"linux"},
					}},
				}},
			},
			expected: true,
		},
		{
			name: "non-matching label",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "kubernetes.io/os",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"windows"},
					}},
				}},
			},
			expected: false,
		},
		{
			name: "exists operator",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{{
						Key:      "node-type",
						Operator: corev1.NodeSelectorOpExists,
					}},
				}},
			},
			expected: true,
		},
		{
			name: "AND logic fails",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"linux"}},
						{Key: "node-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"master"}},
					},
				}},
			},
			expected: false,
		},
		{
			name: "OR logic succeeds",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "kubernetes.io/os", Operator: corev1.NodeSelectorOpIn, Values: []string{"windows"}},
						},
					},
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{Key: "node-type", Operator: corev1.NodeSelectorOpIn, Values: []string{"worker"}},
						},
					},
				},
			},
			expected: true,
		},
		{
			name: "field selector metadata.name",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.name",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"test-node"},
					}},
				}},
			},
			expected: true,
		},
		{
			name: "unsupported field",
			nodeSelector: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{{
					MatchFields: []corev1.NodeSelectorRequirement{{
						Key:      "metadata.namespace",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"default"},
					}},
				}},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MatchesSelector(node, tt.nodeSelector)
			if result != tt.expected {
				t.Errorf("MatchesSelector() = %v, expected %v", result, tt.expected)
			}
		})
	}
}

func TestFieldSelectorValidation(t *testing.T) {
	tests := []struct {
		name        string
		fieldKey    string
		expectError bool
	}{
		{"metadata.name supported", "metadata.name", false},
		{"metadata.namespace unsupported", "metadata.namespace", true},
		{"custom field unsupported", "custom.field", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requirements := []corev1.NodeSelectorRequirement{{
				Key:      tt.fieldKey,
				Operator: corev1.NodeSelectorOpIn,
				Values:   []string{"test-value"},
			}}

			_, err := nodeSelectorRequirementsAsFieldSelector(requirements)

			if tt.expectError && err == nil {
				t.Errorf("Expected error but got none")
			} else if !tt.expectError && err != nil {
				t.Errorf("Expected no error but got: %v", err)
			} else if tt.expectError && err != nil && !strings.Contains(err.Error(), "field selector only supports 'metadata.name'") {
				t.Errorf("Unexpected error message: %v", err)
			}
		})
	}
}

package utils

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/selection"
)

// MatchesSelector checks if node matches the given node selector using Kubernetes official libraries
func MatchesSelector(node *corev1.Node, nodeSelector *corev1.NodeSelector) bool {
	if nodeSelector == nil || len(nodeSelector.NodeSelectorTerms) == 0 {
		return true // No selector means all nodes match (Kubernetes standard behavior)
	}

	nodeLabels := labels.Set(node.Labels)
	nodeFields := fields.Set{
		"metadata.name": node.Name,
	}

	// Node must match at least one NodeSelectorTerm (OR logic between terms)
	for _, term := range nodeSelector.NodeSelectorTerms {
		if matchesTerm(term, nodeLabels, nodeFields) {
			return true
		}
	}
	return false
}

// matchesTerm checks if a node matches a single NodeSelectorTerm using K8s libraries
func matchesTerm(term corev1.NodeSelectorTerm, nodeLabels labels.Set, nodeFields fields.Set) bool {
	// If both MatchExpressions and MatchFields are empty, no nodes can match
	if len(term.MatchExpressions) == 0 && len(term.MatchFields) == 0 {
		return false
	}

	// All expressions in a term must match (AND logic within term)

	// Check MatchExpressions using labels.Selector
	if len(term.MatchExpressions) > 0 {
		labelSelector, err := nodeSelectorRequirementsAsLabelSelector(term.MatchExpressions)
		if err != nil || !labelSelector.Matches(nodeLabels) {
			return false
		}
	}

	// Check MatchFields using fields.Selector
	if len(term.MatchFields) > 0 {
		fieldSelector, err := nodeSelectorRequirementsAsFieldSelector(term.MatchFields)
		if err != nil || !fieldSelector.Matches(nodeFields) {
			return false
		}
	}

	return true
}

// nodeSelectorRequirementsAsLabelSelector converts NodeSelectorRequirements to labels.Selector
func nodeSelectorRequirementsAsLabelSelector(requirements []corev1.NodeSelectorRequirement) (labels.Selector, error) {
	selector := labels.NewSelector()

	for _, req := range requirements {
		var op selection.Operator
		switch req.Operator {
		case corev1.NodeSelectorOpIn:
			op = selection.In
		case corev1.NodeSelectorOpNotIn:
			op = selection.NotIn
		case corev1.NodeSelectorOpExists:
			op = selection.Exists
		case corev1.NodeSelectorOpDoesNotExist:
			op = selection.DoesNotExist
		case corev1.NodeSelectorOpGt:
			op = selection.GreaterThan
		case corev1.NodeSelectorOpLt:
			op = selection.LessThan
		default:
			return nil, fmt.Errorf("unsupported node selector operator: %v", req.Operator)
		}

		requirement, err := labels.NewRequirement(req.Key, op, req.Values)
		if err != nil {
			return nil, err
		}
		selector = selector.Add(*requirement)
	}

	return selector, nil
}

// nodeSelectorRequirementsAsFieldSelector converts NodeSelectorRequirements to fields.Selector
// Only supports metadata.name field matching
func nodeSelectorRequirementsAsFieldSelector(requirements []corev1.NodeSelectorRequirement) (fields.Selector, error) {
	fieldSet := fields.Set{}

	for _, req := range requirements {
		// Only support metadata.name field
		if req.Key != "metadata.name" {
			return nil, fmt.Errorf("field selector only supports 'metadata.name', got '%s'", req.Key)
		}

		switch req.Operator {
		case corev1.NodeSelectorOpIn:
			// For In operator, we expect exactly one value for field selectors
			if len(req.Values) == 1 {
				fieldSet[req.Key] = req.Values[0]
			} else {
				return nil, fmt.Errorf("field selector In operator requires exactly one value, got %d", len(req.Values))
			}
		case corev1.NodeSelectorOpExists, corev1.NodeSelectorOpDoesNotExist, corev1.NodeSelectorOpNotIn, corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			// These operations are more complex for fields and not commonly used in NodeSelector context
			// For now, we'll return an error for unsupported operations
			return nil, fmt.Errorf("field selector operator %v is not supported in this implementation", req.Operator)
		default:
			return nil, fmt.Errorf("unsupported node selector operator: %v", req.Operator)
		}
	}

	return fields.SelectorFromSet(fieldSet), nil
}

// /*
// Copyright 2025 The Grove Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
// */

package utils

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	defaultPollInterval = 2 * time.Second
)

// IsNodeReady checks if a node is in Ready state
func IsNodeReady(node *v1.Node) bool {
	for _, condition := range node.Status.Conditions {
		if condition.Type == v1.NodeReady {
			return condition.Status == v1.ConditionTrue
		}
	}
	return false // If no Ready condition is found, consider the node not ready
}

// WaitAndGetReadyNode waits for a specific node to become ready after container restart and returns the ready node
func WaitAndGetReadyNode(ctx context.Context, clientset *kubernetes.Clientset, nodeName string, timeout time.Duration, logger *Logger) (*v1.Node, error) {
	var readyNode *v1.Node
	err := PollForCondition(ctx, timeout, defaultPollInterval, func() (bool, error) {
		node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			// Node might not have rejoined yet (k3d agent still starting up)
			logger.Debugf("  Node %s not found yet (k3d agent still connecting), waiting...", nodeName)
			return false, nil
		}

		if IsNodeReady(node) {
			logger.Debugf("✅ Node %s has rejoined and is ready", nodeName)
			readyNode = node
			return true, nil
		}

		logger.Debugf("  Node %s found but not ready yet, waiting...", nodeName)
		return false, nil
	})

	if err != nil {
		return nil, err
	}
	return readyNode, nil
}

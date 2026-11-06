package csi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var ErrAttachmentNotFound = errors.New("csi: volume attachment not found")

// AttachmentLookup resolves the Kubernetes VolumeAttachment UID for a CSI
// volume handle. The UID is the attachment generation: a retry of the same
// attach reuses it, while a new VolumeAttachment gets a new credential.
type AttachmentLookup interface {
	AttachmentID(ctx context.Context, volumeHandle, nodeID string) (string, error)
}

type PolicyLookup interface {
	NodeFailurePolicy(ctx context.Context, volumeHandle string) (string, error)
	NodeUnavailable(ctx context.Context, nodeID string) (bool, error)
	NodeCallerID(ctx context.Context, nodeID string) (string, error)
	VolumeProtocol(ctx context.Context, volumeHandle string) (string, error)
}

type KubernetesAttachmentLookup struct {
	client kubernetes.Interface
}

func NewKubernetesAttachmentLookup(kubeconfig string) (*KubernetesAttachmentLookup, error) {
	var (
		config *rest.Config
		err    error
	)
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("csi: Kubernetes client configuration: %w", err)
	}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("csi: create Kubernetes client: %w", err)
	}
	return &KubernetesAttachmentLookup{client: client}, nil
}

func (lookup *KubernetesAttachmentLookup) AttachmentID(ctx context.Context, volumeHandle, nodeID string) (string, error) {
	persistentVolume, err := lookup.persistentVolume(ctx, volumeHandle)
	if err != nil {
		return "", err
	}
	attachments, err := lookup.client.StorageV1().VolumeAttachments().List(ctx, metav1.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("csi: list volume attachments: %w", err)
	}
	for index := range attachments.Items {
		attachment := &attachments.Items[index]
		if attachment.Spec.Source.PersistentVolumeName != nil && *attachment.Spec.Source.PersistentVolumeName == persistentVolume.Name && attachment.Spec.NodeName == nodeID {
			return string(attachment.UID), nil
		}
	}
	return "", fmt.Errorf("%w: %s on %s", ErrAttachmentNotFound, persistentVolume.Name, nodeID)
}

func (lookup *KubernetesAttachmentLookup) NodeFailurePolicy(ctx context.Context, volumeHandle string) (string, error) {
	persistentVolume, err := lookup.persistentVolume(ctx, volumeHandle)
	if err != nil {
		return "", err
	}
	if persistentVolume.Spec.StorageClassName == "" {
		return "wait", nil
	}
	storageClass, err := lookup.client.StorageV1().StorageClasses().Get(ctx, persistentVolume.Spec.StorageClassName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("csi: get storage class %s: %w", persistentVolume.Spec.StorageClassName, err)
	}
	policy := strings.ToLower(strings.TrimSpace(storageClass.Parameters["nodeFailurePolicy"]))
	switch policy {
	case "", "wait":
		return "wait", nil
	case "force", "failover":
		return "force", nil
	default:
		return "", fmt.Errorf("csi: unsupported nodeFailurePolicy %q", policy)
	}
}

func (lookup *KubernetesAttachmentLookup) VolumeProtocol(ctx context.Context, volumeHandle string) (string, error) {
	persistentVolume, err := lookup.persistentVolume(ctx, volumeHandle)
	if err != nil {
		return "", err
	}
	if persistentVolume.Spec.CSI == nil {
		return "", fmt.Errorf("%w: persistent volume %s has no CSI source", ErrAttachmentNotFound, persistentVolume.Name)
	}
	protocol := strings.ToLower(strings.TrimSpace(persistentVolume.Spec.CSI.VolumeAttributes["protocol"]))
	if protocol == "" {
		return "iscsi", nil
	}
	return protocol, nil
}

func (lookup *KubernetesAttachmentLookup) NodeCallerID(ctx context.Context, nodeID string) (string, error) {
	if nodeID == "" {
		return "", fmt.Errorf("csi: node_id is required")
	}
	csiNode, err := lookup.client.StorageV1().CSINodes().Get(ctx, nodeID, metav1.GetOptions{})
	if err == nil {
		if caller := strings.TrimSpace(csiNode.Annotations["kouja.csi/node-caller-id"]); caller != "" {
			return caller, nil
		}
	} else if !apierrors.IsNotFound(err) {
		return "", fmt.Errorf("csi: get CSINode %s: %w", nodeID, err)
	}
	// The node certificate convention is part of the deployment contract.
	// Operators that use a different node identity must set the CSINode
	// annotation.
	return "spiffe://kouja/node/" + nodeID, nil
}

func (lookup *KubernetesAttachmentLookup) NodeUnavailable(ctx context.Context, nodeID string) (bool, error) {
	if nodeID == "" {
		return false, nil
	}
	node, err := lookup.client.CoreV1().Nodes().Get(ctx, nodeID, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("csi: get node %s: %w", nodeID, err)
	}
	for _, taint := range node.Spec.Taints {
		if taint.Key == "node.kubernetes.io/out-of-service" {
			return true, nil
		}
	}
	for _, condition := range node.Status.Conditions {
		if condition.Type == corev1.NodeReady {
			return condition.Status != corev1.ConditionTrue, nil
		}
	}
	return true, nil
}

func (lookup *KubernetesAttachmentLookup) persistentVolume(ctx context.Context, volumeHandle string) (*corev1.PersistentVolume, error) {
	persistentVolumes, err := lookup.client.CoreV1().PersistentVolumes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("csi: list persistent volumes: %w", err)
	}
	for index := range persistentVolumes.Items {
		volume := &persistentVolumes.Items[index]
		if volume.Spec.CSI != nil && volume.Spec.CSI.VolumeHandle == volumeHandle {
			return volume, nil
		}
	}
	return nil, fmt.Errorf("%w: persistent volume for %s", ErrAttachmentNotFound, volumeHandle)
}

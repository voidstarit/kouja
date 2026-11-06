package csi

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var placementResource = schema.GroupVersionResource{
	Group: "kouja.io", Version: "v1alpha1", Resource: "koujavolumeplacements",
}

var ErrPlacementNotFound = errors.New("csi: volume placement not found")

// KubernetesPlacement stores the durable host assignment for a CSI volume in a
// cluster-scoped KoujaVolumePlacement custom resource.
type KubernetesPlacement struct {
	client dynamic.Interface
	now    func() time.Time
}

func NewKubernetesPlacement(kubeconfig string) (*KubernetesPlacement, error) {
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
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("csi: create Kubernetes discovery client: %w", err)
	}
	if _, err := discoveryClient.ServerResourcesForGroupVersion(placementResource.GroupVersion().String()); err != nil {
		return nil, fmt.Errorf("csi: KoujaVolumePlacement CRD is unavailable: %w", err)
	}
	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, fmt.Errorf("csi: create dynamic Kubernetes client: %w", err)
	}
	return &KubernetesPlacement{client: client, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (placement *KubernetesPlacement) Get(ctx context.Context, key string) (PlacementRecord, error) {
	object, err := placement.client.Resource(placementResource).Get(ctx, key, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return PlacementRecord{}, ErrPlacementNotFound
	}
	if err != nil {
		return PlacementRecord{}, fmt.Errorf("csi: get placement %s: %w", key, err)
	}
	return placementRecord(object), nil
}

func (placement *KubernetesPlacement) List(ctx context.Context) ([]PlacementRecord, error) {
	list, err := placement.client.Resource(placementResource).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("csi: list placements: %w", err)
	}
	records := make([]PlacementRecord, 0, len(list.Items))
	for index := range list.Items {
		records = append(records, placementRecord(&list.Items[index]))
	}
	return records, nil
}

func (placement *KubernetesPlacement) Pending(ctx context.Context, key string, host Host, volumeID string) error {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kouja.io/v1alpha1",
		"kind":       "KoujaVolumePlacement",
		"metadata":   map[string]any{"name": key},
		"spec": map[string]any{
			"placementKey":   key,
			"csiVolumeName":  key,
			"storageClass":   "",
			"hostUuid":       host.UUID,
			"volumeId":       volumeID,
			"idempotencyKey": volumeID,
		},
		"status": map[string]any{
			"phase":     "Pending",
			"updatedAt": placement.now().Format(time.RFC3339),
		},
	}}
	if _, err := placement.client.Resource(placementResource).Create(ctx, object, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("csi: create placement %s: %w", key, err)
	}
	if err := placement.updateStatus(ctx, key, "Pending", ""); err != nil {
		_ = placement.Delete(ctx, key)
		return err
	}
	return nil
}

func (placement *KubernetesPlacement) Bind(ctx context.Context, key, resourceUUID string) error {
	object, err := placement.client.Resource(placementResource).Get(ctx, key, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("csi: get placement %s: %w", key, err)
	}
	if err := unstructured.SetNestedField(object.Object, resourceUUID, "spec", "resourceUuid"); err != nil {
		return fmt.Errorf("csi: set placement resource UUID: %w", err)
	}
	if _, err := placement.client.Resource(placementResource).Update(ctx, object, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("csi: update placement %s: %w", key, err)
	}
	return placement.updateStatus(ctx, key, "Bound", "")
}

func (placement *KubernetesPlacement) Fail(ctx context.Context, key string, cause error) error {
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return placement.updateStatus(ctx, key, "Failed", message)
}

func (placement *KubernetesPlacement) Delete(ctx context.Context, key string) error {
	err := placement.client.Resource(placementResource).Delete(ctx, key, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("csi: delete placement %s: %w", key, err)
	}
	return nil
}

func (placement *KubernetesPlacement) updateStatus(ctx context.Context, key, phase, message string) error {
	object, err := placement.client.Resource(placementResource).Get(ctx, key, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("csi: get placement %s for status: %w", key, err)
	}
	status := map[string]any{
		"phase":     phase,
		"updatedAt": placement.now().Format(time.RFC3339),
	}
	if message != "" {
		status["error"] = message
	}
	if err := unstructured.SetNestedMap(object.Object, status, "status"); err != nil {
		return fmt.Errorf("csi: set placement status: %w", err)
	}
	if _, err := placement.client.Resource(placementResource).UpdateStatus(ctx, object, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("csi: update placement %s status: %w", key, err)
	}
	return nil
}

func placementRecord(object *unstructured.Unstructured) PlacementRecord {
	record := PlacementRecord{Key: object.GetName()}
	record.CSIVolumeName, _, _ = unstructured.NestedString(object.Object, "spec", "csiVolumeName")
	record.StorageClass, _, _ = unstructured.NestedString(object.Object, "spec", "storageClass")
	record.HostUUID, _, _ = unstructured.NestedString(object.Object, "spec", "hostUuid")
	record.VolumeID, _, _ = unstructured.NestedString(object.Object, "spec", "volumeId")
	record.ResourceUUID, _, _ = unstructured.NestedString(object.Object, "spec", "resourceUuid")
	record.IdempotencyKey, _, _ = unstructured.NestedString(object.Object, "spec", "idempotencyKey")
	record.Phase, _, _ = unstructured.NestedString(object.Object, "status", "phase")
	record.Error, _, _ = unstructured.NestedString(object.Object, "status", "error")
	if value, _, _ := unstructured.NestedString(object.Object, "status", "updatedAt"); value != "" {
		record.UpdatedAt, _ = time.Parse(time.RFC3339, value)
	}
	return record
}

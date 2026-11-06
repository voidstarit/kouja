package csi

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func bddTestKubernetesAttachmentLookup(t testing.TB) {
	const (
		handle = "v1:host-1:resource-1"
		nodeID = "node-1"
	)
	pvName := "pv-1"
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: pvName},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "class",
			PersistentVolumeSource: corev1.PersistentVolumeSource{CSI: &corev1.CSIPersistentVolumeSource{
				VolumeHandle:     handle,
				VolumeAttributes: map[string]string{"protocol": "nvme-tcp"},
			}},
		},
	}
	va := &storagev1.VolumeAttachment{
		ObjectMeta: metav1.ObjectMeta{Name: "va-1", UID: "uid-1"},
		Spec: storagev1.VolumeAttachmentSpec{
			Attacher: "kouja.csi.voidstarit.com",
			NodeName: nodeID,
			Source:   storagev1.VolumeAttachmentSource{PersistentVolumeName: &pvName},
		},
	}
	class := &storagev1.StorageClass{ObjectMeta: metav1.ObjectMeta{Name: "class"}, Parameters: map[string]string{"nodeFailurePolicy": "wait"}}
	csiNode := &storagev1.CSINode{ObjectMeta: metav1.ObjectMeta{
		Name: nodeID, Annotations: map[string]string{"kouja.csi/node-caller-id": "spiffe://kouja/node/custom"},
	}}
	lookup := &KubernetesAttachmentLookup{client: fake.NewSimpleClientset(pv, va, class, csiNode)}
	if id, err := lookup.AttachmentID(context.Background(), handle, nodeID); err != nil || id != "uid-1" {
		t.Fatalf("attachment = %q, %v", id, err)
	}
	if id, err := lookup.AttachmentID(context.Background(), handle, "other"); err == nil || id != "" {
		t.Fatalf("missing attachment = %q, %v", id, err)
	}
	if policy, err := lookup.NodeFailurePolicy(context.Background(), handle); err != nil || policy != "wait" {
		t.Fatalf("policy = %q, %v", policy, err)
	}
	if caller, err := lookup.NodeCallerID(context.Background(), nodeID); err != nil || caller != "spiffe://kouja/node/custom" {
		t.Fatalf("caller = %q, %v", caller, err)
	}
	if _, err := lookup.NodeFailurePolicy(context.Background(), "missing"); err == nil {
		t.Fatal("missing PV policy did not fail")
	}
}

func bddTestNodeFailurePolicyAndNodeUnavailable(t testing.TB) {
	const (
		volumeHandle = "v1:host-1:resource-1"
		className    = "force-class"
		nodeName     = "node-1"
	)
	pv := &corev1.PersistentVolume{
		ObjectMeta: metav1.ObjectMeta{Name: "pv-1"},
		Spec: corev1.PersistentVolumeSpec{
			StorageClassName: className,
			PersistentVolumeSource: corev1.PersistentVolumeSource{
				CSI: &corev1.CSIPersistentVolumeSource{
					VolumeHandle: volumeHandle,
					VolumeAttributes: map[string]string{
						"protocol": "nvme-tcp",
					},
				},
			},
		},
	}
	class := &storagev1.StorageClass{
		ObjectMeta: metav1.ObjectMeta{Name: className},
		Parameters: map[string]string{"nodeFailurePolicy": "force"},
	}
	notReady := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: nodeName},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionFalse,
		}}},
	}
	lookup := &KubernetesAttachmentLookup{client: fake.NewSimpleClientset(pv, class, notReady)}
	policy, err := lookup.NodeFailurePolicy(context.Background(), volumeHandle)
	if err != nil || policy != "force" {
		t.Fatalf("policy = %q, %v", policy, err)
	}
	protocol, err := lookup.VolumeProtocol(context.Background(), volumeHandle)
	if err != nil || protocol != "nvme-tcp" {
		t.Fatalf("protocol = %q, %v", protocol, err)
	}
	callerID, err := lookup.NodeCallerID(context.Background(), nodeName)
	if err != nil || callerID != "spiffe://kouja/node/node-1" {
		t.Fatalf("node caller = %q, %v", callerID, err)
	}
	unavailable, err := lookup.NodeUnavailable(context.Background(), nodeName)
	if err != nil || !unavailable {
		t.Fatalf("unavailable = %v, %v", unavailable, err)
	}

	ready := notReady.DeepCopy()
	ready.Name = "node-ready"
	ready.Status.Conditions[0].Status = corev1.ConditionTrue
	outOfService := notReady.DeepCopy()
	outOfService.Name = "node-out-of-service"
	outOfService.Spec.Taints = []corev1.Taint{{Key: "node.kubernetes.io/out-of-service", Effect: corev1.TaintEffectNoExecute}}
	if _, err := lookup.client.CoreV1().Nodes().Create(context.Background(), ready, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := lookup.client.CoreV1().Nodes().Create(context.Background(), outOfService, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if unavailable, err := lookup.NodeUnavailable(context.Background(), ready.Name); err != nil || unavailable {
		t.Fatalf("ready node unavailable = %v, %v", unavailable, err)
	}
	if unavailable, err := lookup.NodeUnavailable(context.Background(), outOfService.Name); err != nil || !unavailable {
		t.Fatalf("out-of-service node unavailable = %v, %v", unavailable, err)
	}
	if unavailable, err := lookup.NodeUnavailable(context.Background(), "missing"); err != nil || !unavailable {
		t.Fatalf("missing node unavailable = %v, %v", unavailable, err)
	}
}

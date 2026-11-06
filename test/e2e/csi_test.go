//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestCSIIscsiProvisionAndIO assumes the CSI controller, node plugin, and a
// StorageClass named by KOUJA_E2E_STORAGECLASS are already installed in the
// current kubectl context. It exercises dynamic provisioning, attach, login,
// format, write, read, unmount, and delete.
func TestCSIIscsiProvisionAndIO(t *testing.T) {
	if os.Getenv("KOUJA_E2E") != "1" {
		t.Skip("set KOUJA_E2E=1 with a kubeconfig and installed kouja CSI driver")
	}
	namespace := envOrDefault("KOUJA_E2E_NAMESPACE", "kouja-csi")
	storageClass := envOrDefault("KOUJA_E2E_STORAGECLASS", "kouja-e2e")
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	pvcName := "kouja-e2e-pvc-" + suffix
	podName := "kouja-e2e-pod-" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	manifest := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 64Mi
  storageClassName: %s
---
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  containers:
    - name: writer
      image: busybox:1.36
      command:
        - sh
        - -c
        - |
          set -e
          dd if=/dev/urandom of=/data/test.bin bs=1M count=8
          sync
          dd if=/data/test.bin of=/dev/null bs=1M
          echo kouja-csi-write-read-ok
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: %s
`, pvcName, namespace, storageClass, podName, namespace, pvcName)

	run(t, ctx, manifest, "apply", "-f", "-")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = kubectl(t, cleanupCtx, nil, "delete", "pod", podName, "-n", namespace, "--ignore-not-found", "--wait=true")
		_ = kubectl(t, cleanupCtx, nil, "delete", "pvc", pvcName, "-n", namespace, "--ignore-not-found", "--wait=true")
	})

	waitForPhase(t, ctx, namespace, podName, "Succeeded")
	logs := kubectl(t, ctx, nil, "logs", "-n", namespace, podName)
	if !strings.Contains(logs, "kouja-csi-write-read-ok") {
		t.Fatalf("pod did not complete the read/write check:\n%s", logs)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func run(t *testing.T, ctx context.Context, stdin string, args ...string) string {
	t.Helper()
	var input *bytes.Reader
	if stdin != "" {
		input = bytes.NewReader([]byte(stdin))
	}
	return kubectlReader(t, ctx, input, args...)
}

func kubectl(t *testing.T, ctx context.Context, stdin *bytes.Reader, args ...string) string {
	t.Helper()
	return kubectlReader(t, ctx, stdin, args...)
}

func kubectlReader(t *testing.T, ctx context.Context, stdin *bytes.Reader, args ...string) string {
	t.Helper()
	command := exec.CommandContext(ctx, "kubectl", args...) //nolint:gosec // test harness invokes the operator's kubectl
	if stdin != nil {
		command.Stdin = stdin
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("kubectl %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func waitForPhase(t *testing.T, ctx context.Context, namespace, podName, phase string) {
	t.Helper()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for pod %s/%s to reach %s", namespace, podName, phase)
		case <-ticker.C:
			output, err := exec.CommandContext(ctx, "kubectl", "get", "pod", podName, "-n", namespace, "-o", "jsonpath={.status.phase}").Output() //nolint:gosec // test harness
			if err != nil {
				continue
			}
			if strings.TrimSpace(string(output)) == phase {
				return
			}
		}
	}
}

// TestCSICredentialRotationOnReattach verifies that a new VolumeAttachment
// generation receives a different CHAP secret. The CSI controller must pass
// the VolumeAttachment UID to the agent; repeated retries of one attachment
// remain idempotent.
func TestCSICredentialRotationOnReattach(t *testing.T) {
	if os.Getenv("KOUJA_E2E") != "1" {
		t.Skip("set KOUJA_E2E=1 with a kubeconfig and installed kouja CSI driver")
	}
	namespace := envOrDefault("KOUJA_E2E_NAMESPACE", "kouja-csi")
	storageClass := envOrDefault("KOUJA_E2E_STORAGECLASS", "kouja-e2e")
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	pvcName := "kouja-rot-pvc-" + suffix
	podName := "kouja-rot-pod-" + suffix
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	manifest := fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %s
  namespace: %s
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: 64Mi
  storageClassName: %s
---
apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
spec:
  restartPolicy: Never
  containers:
    - name: sleeper
      image: busybox:1.36
      command: ["sh", "-c", "echo ready > /data/ready; sleep 300"]
      volumeMounts:
        - name: data
          mountPath: /data
  volumes:
    - name: data
      persistentVolumeClaim:
        claimName: %s
`, pvcName, namespace, storageClass, podName, namespace, pvcName)
	run(t, ctx, manifest, "apply", "-f", "-")
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cleanupCancel()
		_ = kubectl(t, cleanupCtx, nil, "delete", "pod", podName, "-n", namespace, "--ignore-not-found", "--wait=true")
		_ = kubectl(t, cleanupCtx, nil, "delete", "pvc", pvcName, "-n", namespace, "--ignore-not-found", "--wait=true")
	})
	waitForPhase(t, ctx, namespace, podName, "Running")

	pvName := strings.TrimSpace(kubectl(t, ctx, nil, "get", "pvc", pvcName, "-n", namespace, "-o", "jsonpath={.spec.volumeName}"))
	if pvName == "" {
		t.Fatal("PVC has no bound volume")
	}
	first := attachmentForPV(t, ctx, pvName)
	if first.credential == "" {
		t.Fatal("first VolumeAttachment has no credential")
	}

	// Force a new attachment generation: stop the pod, remove the old
	// VolumeAttachment, then create a replacement pod on the same node.
	run(t, ctx, "", "delete", "pod", podName, "-n", namespace, "--wait=true", "--timeout=60s")
	_ = kubectl(t, ctx, nil, "delete", "volumeattachment", first.name, "--ignore-not-found", "--wait=true", "--timeout=60s")
	run(t, ctx, manifest, "apply", "-f", "-")
	waitForPhase(t, ctx, namespace, podName, "Running")

	second := attachmentForPV(t, ctx, pvName)
	if second.credential == "" {
		t.Fatal("second VolumeAttachment has no credential")
	}
	if second.uid == first.uid {
		t.Fatalf("VolumeAttachment UID was not recreated: %s", first.uid)
	}
	if first.credential == second.credential {
		t.Fatalf("credential was reused across attachments: %q", first.credential)
	}
}

type volumeAttachmentList struct {
	Items []struct {
		Metadata struct {
			Name string `json:"name"`
			UID  string `json:"uid"`
		} `json:"metadata"`
		Spec struct {
			Source struct {
				PersistentVolumeName string `json:"persistentVolumeName"`
			} `json:"source"`
		} `json:"spec"`
		Status struct {
			AttachmentMetadata map[string]string `json:"attachmentMetadata"`
		} `json:"status"`
	} `json:"items"`
}

type attachmentRef struct {
	credential string
	name       string
	uid        string
}

func attachmentForPV(t *testing.T, ctx context.Context, pvName string) attachmentRef {
	t.Helper()
	raw := kubectl(t, ctx, nil, "get", "volumeattachment", "-o", "json")
	var list volumeAttachmentList
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("decode VolumeAttachments: %v", err)
	}
	for _, item := range list.Items {
		if item.Spec.Source.PersistentVolumeName == pvName {
			return attachmentRef{
				credential: item.Status.AttachmentMetadata["credential"],
				name:       item.Metadata.Name,
				uid:        item.Metadata.UID,
			}
		}
	}
	return attachmentRef{}
}

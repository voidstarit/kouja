package agent

import (
	"context"
	"encoding/json"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/voidstarit/kouja/gen/kouja/v1"
	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var _ = Describe("storage class selection", func() {
	classes := map[string]string{"nvme": "vg-nvme", "ssd": "vg-ssd", "hdd": "vg-hdd"}

	It("resolves a class to its configured VG", func() {
		class, vg, err := resolveStorageClass(classes, "ssd", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(class).To(Equal("ssd"))
		Expect(vg).To(Equal("vg-ssd"))
	})

	It("accepts a raw VG when no class mapping exists", func() {
		class, vg, err := resolveStorageClass(nil, "vg-raw", "")
		Expect(err).NotTo(HaveOccurred())
		Expect(class).To(Equal("vg-raw"))
		Expect(vg).To(Equal("vg-raw"))
	})

	It("rejects a class and VG that disagree", func() {
		_, _, err := resolveStorageClass(classes, "nvme", "vg-hdd")
		Expect(err).To(MatchError(ContainSubstring("maps to vg")))
	})

	It("reports the class for capacity belonging to a mapped VG", func() {
		store := newAgentStore(GinkgoTB())
		now := time.Now().UTC()
		Expect(store.Transact(context.Background(), func(tx state.Tx) error {
			return tx.PutVG(state.VG{Name: "vg-nvme", UUID: "vg-uuid", State: "active", RegisteredAt: now, UpdatedAt: now})
		})).To(Succeed())
		service := NewVolumeService(store, &fakeLVM{capacity: lvm.Capacity{
			VGName: "vg-nvme", VGUUID: "vg-uuid", VGState: "active",
			TotalBytes: 10 << 30, FreeBytes: 8 << 30, AllocatableBytes: 7 << 30,
		}}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{StorageClasses: classes})

		response, err := service.GetCapacity(context.Background(), &koujav1.GetCapacityRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.GetVgs()).To(HaveLen(1))
		Expect(response.GetVgs()[0].GetStorageClass()).To(Equal("nvme"))
		Expect(response.GetVgs()[0].GetVgName()).To(Equal("vg-nvme"))

		classes, err := service.ListStorageClasses(context.Background(), &koujav1.ListStorageClassesRequest{})
		Expect(err).NotTo(HaveOccurred())
		Expect(classes.GetClasses()).To(HaveLen(1))
		Expect(classes.GetClasses()[0].GetName()).To(Equal("nvme"))
		Expect(classes.GetClasses()[0].GetVgName()).To(Equal("vg-nvme"))
		Expect(classes.GetClasses()[0].GetAllocatableBytes()).To(Equal(int64(7 << 30)))
	})

	It("stores the selected VG and returns the selected class on create", func() {
		store := newAgentStore(GinkgoTB())
		now := time.Now().UTC()
		runner := &fakeRunner{result: operations.Result{Volume: state.Volume{
			Owner: "spiffe://kouja/caller/one", VolumeID: "vol1", ResourceUUID: "resource-1",
			VGName: "vg-nvme", RequestedSizeBytes: 1 << 20, ActualSizeBytes: 1 << 20,
			State: "available", CreatedAt: now, UpdatedAt: now,
		}}}
		service := NewVolumeService(store, &fakeLVM{}, runner, &fakeCredentialManager{}, ServiceOptions{
			StorageClasses: classes,
		})
		export := false
		response, err := service.CreateVolume(callerContext("spiffe://kouja/caller/one"), &koujav1.CreateVolumeRequest{
			VolumeId: "vol1", SizeBytes: 1 << 20, StorageClass: "nvme", Export: &export,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.GetVolume().GetStorageClass()).To(Equal("nvme"))
		var intent operations.CreateIntent
		Expect(json.Unmarshal(runner.request.Intent, &intent)).To(Succeed())
		Expect(intent.VG).To(Equal("vg-nvme"))
	})

	It("returns InvalidArgument for an invalid class mapping", func() {
		service := NewVolumeService(newAgentStore(GinkgoTB()), &fakeLVM{}, &fakeRunner{}, &fakeCredentialManager{}, ServiceOptions{
			StorageClasses: classes,
		})
		export := false
		_, err := service.CreateVolume(callerContext("spiffe://kouja/caller/one"), &koujav1.CreateVolumeRequest{
			VolumeId: "vol1", SizeBytes: 1 << 20, StorageClass: "nvme", Vg: "vg-hdd", Export: &export,
		})
		Expect(status.Code(err)).To(Equal(codes.InvalidArgument))
	})
})

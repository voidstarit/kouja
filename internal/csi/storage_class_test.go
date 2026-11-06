package csi

import (
	"context"

	csipb "github.com/container-storage-interface/spec/lib/go/csi"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	koujav1 "github.com/voidstarit/kouja/gen/kouja/v1"
)

var _ = Describe("CSI storage classes", func() {
	newCapacity := func() *koujav1.GetCapacityResponse {
		return &koujav1.GetCapacityResponse{Vgs: []*koujav1.Capacity{
			{VgName: "vg-nvme", StorageClass: "nvme", VgState: "active", AllocatableBytes: 2 << 30, MaxVolumeBytes: 2 << 30},
			{VgName: "vg-hdd", StorageClass: "hdd", VgState: "active", AllocatableBytes: 3 << 30, MaxVolumeBytes: 3 << 30},
		}}
	}

	It("selects a host by class without requiring a VG parameter", func() {
		controller, volume, _, _, _ := newTestController()
		volume.capacity = newCapacity()
		volume.createFn = func(request *koujav1.CreateVolumeRequest) (*koujav1.CreateVolumeResponse, error) {
			return &koujav1.CreateVolumeResponse{Volume: &koujav1.Volume{
				VolumeId: request.GetVolumeId(), ResourceUuid: "resource-1", VgName: "vg-nvme",
				StorageClass: request.GetStorageClass(), ActualSizeBytes: request.GetSizeBytes(),
			}}, nil
		}
		response, err := controller.CreateVolume(context.Background(), &csipb.CreateVolumeRequest{
			Name:          "vol-class",
			CapacityRange: &csipb.CapacityRange{RequiredBytes: 1 << 30},
			Parameters:    map[string]string{"class": "nvme", "protocol": "iscsi"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(volume.lastCreate.GetStorageClass()).To(Equal("nvme"))
		Expect(volume.lastCreate.GetVg()).To(BeEmpty())
		Expect(response.GetVolume().GetVolumeContext()["storage_class"]).To(Equal("nvme"))
		Expect(response.GetVolume().GetVolumeContext()["vg"]).To(Equal("vg-nvme"))
	})

	It("filters capacity by class", func() {
		controller, volume, _, _, _ := newTestController()
		volume.capacity = newCapacity()
		response, err := controller.GetCapacity(context.Background(), &csipb.GetCapacityRequest{
			Parameters: map[string]string{"class": "hdd"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.GetAvailableCapacity()).To(Equal(int64(3 << 30)))
		Expect(response.GetMaximumVolumeSize().GetValue()).To(Equal(int64(3 << 30)))
	})

	It("still filters capacity by a raw VG", func() {
		controller, volume, _, _, _ := newTestController()
		volume.capacity = newCapacity()
		response, err := controller.GetCapacity(context.Background(), &csipb.GetCapacityRequest{
			Parameters: map[string]string{"vg": "vg-nvme"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(response.GetAvailableCapacity()).To(Equal(int64(2 << 30)))
	})
})

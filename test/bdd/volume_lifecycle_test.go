package bdd_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/voidstarit/kouja/internal/lvm"
	"github.com/voidstarit/kouja/internal/operations"
	"github.com/voidstarit/kouja/internal/state"
	"github.com/voidstarit/kouja/internal/target"
)

var _ = Describe("Volume lifecycle", func() {
	const owner = "spiffe://kouja/caller/bdd"

	var (
		store       *state.Store
		manager     *operations.Manager
		backend     *fakeLVM
		exporter    *fakeExporter
		credentials *fakeCredentials
		create      operations.CreateIntent
	)

	BeforeEach(func() {
		var err error
		store, err = state.Open(context.Background(), filepath.Join(GinkgoT().TempDir(), "state.db"))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			Expect(store.Close()).To(Succeed())
		})
		Expect(store.Transact(context.Background(), func(tx state.Tx) error {
			return tx.PutVG(state.VG{
				Name: "vg1", UUID: "vg-uuid", State: "active",
				RegisteredAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
			})
		})).To(Succeed())

		backend = &fakeLVM{
			volumes: map[string]lvm.LV{},
			capacity: lvm.Capacity{
				VGName: "vg1", VGUUID: "vg-uuid", VGState: "active",
				TotalBytes: 1 << 30, AllocatableBytes: 1 << 30, MaxVolumeBytes: 1 << 30,
			},
		}
		exporter = &fakeExporter{}
		credentials = &fakeCredentials{}
		manager = operations.New(store, backend, exporter, nil, credentials, operations.Options{
			MaxAttempts: 1, TotalTimeout: time.Minute,
		})
		create = operations.CreateIntent{
			ResourceUUID: "resource-1", SizeBytes: 64 << 20, VG: "vg1", Export: true,
			Protocol: "iscsi", Initiator: "iqn.initiator", TargetName: "iqn.target",
			Portal: "127.0.0.1:3260",
		}
	})

	createVolume := func() operations.Result {
		raw, err := json.Marshal(create)
		Expect(err).NotTo(HaveOccurred())
		result, err := manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionCreate, Intent: raw,
		})
		Expect(err).NotTo(HaveOccurred())
		return result
	}

	It("creates a thick LV and exports it with a generated credential", func() {
		result := createVolume()

		Expect(result.Volume.State).To(Equal("exported"))
		Expect(result.Volume.ActualSizeBytes).To(Equal(int64(64 << 20)))
		Expect(result.Export.TargetName).To(Equal("iqn.target"))
		Expect(result.Export.Protocol).To(Equal("iscsi"))
		Expect(credentials.generated).To(Equal(1))
		Expect(exporter.exported).To(BeTrue())
		Expect(backend.volumes).To(HaveKey("vg1/kouja-resource-1"))
	})

	It("refuses to unexport while the initiator session is active", func() {
		createVolume()
		exporter.sessions = target.SessionActive
		raw, err := json.Marshal(operations.UnexportIntent{Force: false})
		Expect(err).NotTo(HaveOccurred())

		_, err = manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionUnexport, Intent: raw,
		})
		Expect(errors.Is(err, operations.ErrSessionActive)).To(BeTrue())
		Expect(exporter.exported).To(BeTrue())

		raw, err = json.Marshal(operations.UnexportIntent{Force: true})
		Expect(err).NotTo(HaveOccurred())
		_, err = manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionUnexport, Intent: raw,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(exporter.exported).To(BeFalse())
	})

	It("rotates the credential when the volume is attached again", func() {
		createVolume()
		raw, err := json.Marshal(operations.UnexportIntent{Force: true})
		Expect(err).NotTo(HaveOccurred())
		_, err = manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionUnexport, Intent: raw,
		})
		Expect(err).NotTo(HaveOccurred())

		exportIntent, err := json.Marshal(operations.ExportIntent{
			Protocol: "iscsi", Initiator: "iqn.initiator", TargetName: "iqn.target",
			Portal: "127.0.0.1:3260", RotateCredential: true,
		})
		Expect(err).NotTo(HaveOccurred())
		result, err := manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionExport, Intent: exportIntent,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(result.Volume.State).To(Equal("exported"))
		Expect(credentials.generated).To(Equal(2))
		Expect(exporter.exported).To(BeTrue())
	})

	It("deletes the LV and export after the session is gone", func() {
		createVolume()
		raw, err := json.Marshal(operations.UnexportIntent{Force: true})
		Expect(err).NotTo(HaveOccurred())
		_, err = manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionUnexport, Intent: raw,
		})
		Expect(err).NotTo(HaveOccurred())

		deleteIntent, err := json.Marshal(operations.DeleteIntent{Force: false})
		Expect(err).NotTo(HaveOccurred())
		_, err = manager.Run(context.Background(), operations.Request{
			Owner: owner, VolumeID: "vol-1", Action: operations.ActionDelete, Intent: deleteIntent,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(exporter.exported).To(BeFalse())
		Expect(backend.volumes).NotTo(HaveKey("vg1/kouja-resource-1"))
		Expect(store.Transact(context.Background(), func(tx state.Tx) error {
			_, err := tx.GetVolume(owner, "vol-1")
			return err
		})).To(MatchError(state.ErrNotFound))
	})
})

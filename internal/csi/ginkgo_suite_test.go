package csi

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "csi Suite")
}

var _ = Describe("unit tests", func() {
	It("TestKubernetesAttachmentLookup", func() {
		bddTestKubernetesAttachmentLookup(GinkgoTB())
	})
	It("TestNodeFailurePolicyAndNodeUnavailable", func() {
		bddTestNodeFailurePolicyAndNodeUnavailable(GinkgoTB())
	})
	It("TestNewKubernetesAttachmentLookup", func() {
		bddTestNewKubernetesAttachmentLookup(GinkgoTB())
	})
	It("TestLoadTLSConfigAndControllerLifecycle", func() {
		bddTestLoadTLSConfigAndControllerLifecycle(GinkgoTB())
	})
	It("TestControllerCreateDeleteAndPlacement", func() {
		bddTestControllerCreateDeleteAndPlacement(GinkgoTB())
	})
	It("TestControllerPublishGrantsAndUnpublishRevokes", func() {
		bddTestControllerPublishGrantsAndUnpublishRevokes(GinkgoTB())
	})
	It("TestControllerExpandStatusCapabilitiesAndIdentity", func() {
		bddTestControllerExpandStatusCapabilitiesAndIdentity(GinkgoTB())
	})
	It("TestControllerErrorsAndDuplicatePlacement", func() {
		bddTestControllerErrorsAndDuplicatePlacement(GinkgoTB())
	})
	It("TestControllerErrorPaths", func() {
		bddTestControllerErrorPaths(GinkgoTB())
	})
	It("TestVolumeHandleRoundTrip", func() {
		bddTestVolumeHandleRoundTrip(GinkgoTB())
	})
	It("TestVolumeHandleRejectsInvalidValues", func() {
		bddTestVolumeHandleRejectsInvalidValues(GinkgoTB())
	})
	It("TestProtocolValue", func() {
		bddTestProtocolValue(GinkgoTB())
	})
	It("TestResolveSRV", func() {
		bddTestResolveSRV(GinkgoTB())
	})
	It("TestNodeLifecycleAndExpand", func() {
		bddTestNodeLifecycleAndExpand(GinkgoTB())
	})
	It("TestNodeISCSIPublishAndFailures", func() {
		bddTestNodeISCSIPublishAndFailures(GinkgoTB())
	})
	It("TestNodeMiscellaneousPaths", func() {
		bddTestNodeMiscellaneousPaths(GinkgoTB())
	})
	It("TestNodeCredentialAndHelpers", func() {
		bddTestNodeCredentialAndHelpers(GinkgoTB())
	})
	It("TestNodeFilesystemAndWaitEdges", func() {
		bddTestNodeFilesystemAndWaitEdges(GinkgoTB())
	})
	It("TestNodeSetAgentAndNoop", func() {
		bddTestNodeSetAgentAndNoop(GinkgoTB())
	})
	It("TestKubernetesPlacementLifecycle", func() {
		bddTestKubernetesPlacementLifecycle(GinkgoTB())
	})
})

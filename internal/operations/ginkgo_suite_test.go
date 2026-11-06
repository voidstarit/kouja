package operations

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "operations Suite")
}

var _ = Describe("unit tests", func() {
	It("TestCreateUnexported", func() {
		bddTestCreateUnexported(GinkgoTB())
	})
	It("TestCreateExportAndIdempotentReplay", func() {
		bddTestCreateExportAndIdempotentReplay(GinkgoTB())
	})
	It("TestCreateDifferentParametersRejected", func() {
		bddTestCreateDifferentParametersRejected(GinkgoTB())
	})
	It("TestUnexportRequiresForceAndSupersedesCredential", func() {
		bddTestUnexportRequiresForceAndSupersedesCredential(GinkgoTB())
	})
	It("TestDeleteForceAndSessionGuard", func() {
		bddTestDeleteForceAndSessionGuard(GinkgoTB())
	})
	It("TestResizeExtendsVolumeAndRefreshesExport", func() {
		bddTestResizeExtendsVolumeAndRefreshesExport(GinkgoTB())
	})
	It("TestExecutionFailureIsJournaledAndNotReplayed", func() {
		bddTestExecutionFailureIsJournaledAndNotReplayed(GinkgoTB())
	})
	It("TestTombstoneRetiresStaleDeleteOperation", func() {
		bddTestTombstoneRetiresStaleDeleteOperation(GinkgoTB())
	})
	It("TestRetryRearmsFailedOperation", func() {
		bddTestRetryRearmsFailedOperation(GinkgoTB())
	})
	It("TestExportDifferentInitiatorRejected", func() {
		bddTestExportDifferentInitiatorRejected(GinkgoTB())
	})
	It("TestExportRotationAfterUnexportUsesNewCredential", func() {
		bddTestExportRotationAfterUnexportUsesNewCredential(GinkgoTB())
	})
	It("TestExportRotationDifferentInitiator", func() {
		bddTestExportRotationDifferentInitiator(GinkgoTB())
	})
	It("TestExportRotationRequiresForceForActiveSession", func() {
		bddTestExportRotationRequiresForceForActiveSession(GinkgoTB())
	})
	It("TestExportRotationIsIdempotentForSameAttachment", func() {
		bddTestExportRotationIsIdempotentForSameAttachment(GinkgoTB())
	})
	It("TestIntentParsersAndInternalHelpers", func() {
		bddTestIntentParsersAndInternalHelpers(GinkgoTB())
	})
	It("TestOperationManagerErrorPaths", func() {
		bddTestOperationManagerErrorPaths(GinkgoTB())
	})
	It("TestResizeAndDeleteGuards", func() {
		bddTestResizeAndDeleteGuards(GinkgoTB())
	})
	It("TestOperationErrorAndDefaults", func() {
		bddTestOperationErrorAndDefaults(GinkgoTB())
	})
	It("TestCreateCapacityGuard", func() {
		bddTestCreateCapacityGuard(GinkgoTB())
	})
	It("TestResizeExportFailure", func() {
		bddTestResizeExportFailure(GinkgoTB())
	})
})

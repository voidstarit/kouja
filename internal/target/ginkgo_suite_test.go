package target

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "target Suite")
}

var _ = Describe("unit tests", func() {
	It("TestISCSIExportObserveUnexportDelete", func() {
		bddTestISCSIExportObserveUnexportDelete(GinkgoTB())
	})
	It("TestISCSIUnexportUnknownSessionRequiresForce", func() {
		bddTestISCSIUnexportUnknownSessionRequiresForce(GinkgoTB())
	})
	It("TestISCSIListSessions", func() {
		bddTestISCSIListSessions(GinkgoTB())
	})
	It("TestISCSIValidationAndBackstoreConflict", func() {
		bddTestISCSIValidationAndBackstoreConflict(GinkgoTB())
	})
	It("TestConfigFSPathValidationAndSymlink", func() {
		bddTestConfigFSPathValidationAndSymlink(GinkgoTB())
	})
	It("TestISCSIResizeAndRestore", func() {
		bddTestISCSIResizeAndRestore(GinkgoTB())
	})
	It("TestNVMeExportObserveUnexport", func() {
		bddTestNVMeExportObserveUnexport(GinkgoTB())
	})
	It("TestNVMeValidation", func() {
		bddTestNVMeValidation(GinkgoTB())
	})
	It("TestNVMeResizeDeleteRestoreAndSessions", func() {
		bddTestNVMeResizeDeleteRestoreAndSessions(GinkgoTB())
	})
	It("TestConfigFSPathAndRemove", func() {
		bddTestConfigFSPathAndRemove(GinkgoTB())
	})
	It("TestConfigFSErrorBranches", func() {
		bddTestConfigFSErrorBranches(GinkgoTB())
	})
	It("TestNVMeExportAndResizeErrorBranches", func() {
		bddTestNVMeExportAndResizeErrorBranches(GinkgoTB())
	})
	It("TestISCSIExportErrorBranches", func() {
		bddTestISCSIExportErrorBranches(GinkgoTB())
	})
	It("TestTargetConstructorsAndValidation", func() {
		bddTestTargetConstructorsAndValidation(GinkgoTB())
	})
	It("TestTargetNoopTeardown", func() {
		bddTestTargetNoopTeardown(GinkgoTB())
	})
	It("TestIdempotentExportBranches", func() {
		bddTestIdempotentExportBranches(GinkgoTB())
	})
	It("TestPortalAndComponentValidation", func() {
		bddTestPortalAndComponentValidation(GinkgoTB())
	})
	It("TestDefaultConfigFSRoot", func() {
		bddTestDefaultConfigFSRoot(GinkgoTB())
	})
})

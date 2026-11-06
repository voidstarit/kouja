package hostid

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "hostid Suite")
}

var _ = Describe("unit tests", func() {
	It("TestEnsureCreatesPersistsAndRepairsFile", func() {
		bddTestEnsureCreatesPersistsAndRepairsFile(GinkgoTB())
	})
	It("TestMismatchAndMissingFileWithVolumesFailClosed", func() {
		bddTestMismatchAndMissingFileWithVolumesFailClosed(GinkgoTB())
	})
	It("TestMismatchWithoutVolumesRepairsSetting", func() {
		bddTestMismatchWithoutVolumesRepairsSetting(GinkgoTB())
	})
	It("TestInvalidFileAndBootID", func() {
		bddTestInvalidFileAndBootID(GinkgoTB())
	})
	It("TestHostIDErrorPaths", func() {
		bddTestHostIDErrorPaths(GinkgoTB())
	})
	It("TestHostIDReadBootAndStoreErrors", func() {
		bddTestHostIDReadBootAndStoreErrors(GinkgoTB())
	})
	It("TestHostIDEnsureReadErrors", func() {
		bddTestHostIDEnsureReadErrors(GinkgoTB())
	})
	It("TestHostIDWriteAndReadFailureBranches", func() {
		bddTestHostIDWriteAndReadFailureBranches(GinkgoTB())
	})
	It("TestHostIDInvalidStoredSetting", func() {
		bddTestHostIDInvalidStoredSetting(GinkgoTB())
	})
})

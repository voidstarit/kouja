package state

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "state Suite")
}

var _ = Describe("unit tests", func() {
	It("TestOpenConfiguresDatabaseAndPersistsSettings", func() {
		bddTestOpenConfiguresDatabaseAndPersistsSettings(GinkgoTB())
	})
	It("TestVolumeExportObservationAndCascades", func() {
		bddTestVolumeExportObservationAndCascades(GinkgoTB())
	})
	It("TestPaginationAndListing", func() {
		bddTestPaginationAndListing(GinkgoTB())
	})
	It("TestOperationsAndGrants", func() {
		bddTestOperationsAndGrants(GinkgoTB())
	})
	It("TestValidationForeignKeysAndAudit", func() {
		bddTestValidationForeignKeysAndAudit(GinkgoTB())
	})
	It("TestSchemaNewerRejected", func() {
		bddTestSchemaNewerRejected(GinkgoTB())
	})
	It("TestBackupCreatesRestorableCopy", func() {
		bddTestBackupCreatesRestorableCopy(GinkgoTB())
	})
	It("TestTransactionRollback", func() {
		bddTestTransactionRollback(GinkgoTB())
	})
	It("TestStateMiscellaneousCoverage", func() {
		bddTestStateMiscellaneousCoverage(GinkgoTB())
	})
	It("TestStateValidationBranches", func() {
		bddTestStateValidationBranches(GinkgoTB())
	})
	It("TestNormalizePageAndObservationValidation", func() {
		bddTestNormalizePageAndObservationValidation(GinkgoTB())
	})
})

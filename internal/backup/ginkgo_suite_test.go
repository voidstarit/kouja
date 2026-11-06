package backup

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "backup Suite")
}

var _ = Describe("unit tests", func() {
	It("TestRunCreatesCompleteBundle", func() {
		bddTestRunCreatesCompleteBundle(GinkgoTB())
	})
	It("TestRunWithoutKeyAndMissingHostID", func() {
		bddTestRunWithoutKeyAndMissingHostID(GinkgoTB())
	})
	It("TestRunRejectsInvalidTarget", func() {
		bddTestRunRejectsInvalidTarget(GinkgoTB())
	})
	It("TestRotateRetainsShortDailyAndWeekly", func() {
		bddTestRotateRetainsShortDailyAndWeekly(GinkgoTB())
	})
	It("TestBackupValidationAndCopyErrors", func() {
		bddTestBackupValidationAndCopyErrors(GinkgoTB())
	})
	It("TestBackupDefaultsAndRunFailures", func() {
		bddTestBackupDefaultsAndRunFailures(GinkgoTB())
	})
})

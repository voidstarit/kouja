package lvm

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "lvm Suite")
}

func bddSubtest(t testing.TB, name string, fn func(testing.TB)) {
	By(name)
	fn(t)
}

var _ = Describe("unit tests", func() {
	It("TestCreateBuildsCommandAndObservesAllocatedSize", func() {
		bddTestCreateBuildsCommandAndObservesAllocatedSize(GinkgoTB())
	})
	It("TestCreateMapsResourceExhausted", func() {
		bddTestCreateMapsResourceExhausted(GinkgoTB())
	})
	It("TestObserveParsesTagsAndNotFound", func() {
		bddTestObserveParsesTagsAndNotFound(GinkgoTB())
	})
	It("TestCapacityAppliesHeadroom", func() {
		bddTestCapacityAppliesHeadroom(GinkgoTB())
	})
	It("TestEligibilityRejectsThinClusteredMissingAndRootBacked", func() {
		bddTestEligibilityRejectsThinClusteredMissingAndRootBacked(GinkgoTB())
	})
	It("TestSetOwnerTagReplacesExistingOwnerTag", func() {
		bddTestSetOwnerTagReplacesExistingOwnerTag(GinkgoTB())
	})
	It("TestListManagedAndCandidates", func() {
		bddTestListManagedAndCandidates(GinkgoTB())
	})
	It("TestResizeActivateRemoveCommands", func() {
		bddTestResizeActivateRemoveCommands(GinkgoTB())
	})
	It("TestValidation", func() {
		bddTestValidation(GinkgoTB())
	})
	It("TestOptionsCommandErrorAndSettle", func() {
		bddTestOptionsCommandErrorAndSettle(GinkgoTB())
	})
	It("TestJSONAndProbeEdgeCases", func() {
		bddTestJSONAndProbeEdgeCases(GinkgoTB())
	})
})

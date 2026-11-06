package config

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "config Suite")
}

func bddSubtest(t testing.TB, name string, fn func(testing.TB)) {
	By(name)
	fn(t)
}

var _ = Describe("unit tests", func() {
	It("TestParseAppliesDefaultsAndValidates", func() {
		bddTestParseAppliesDefaultsAndValidates(GinkgoTB())
	})
	It("TestLoadFromFile", func() {
		bddTestLoadFromFile(GinkgoTB())
	})
	It("TestUnknownFieldRejected", func() {
		bddTestUnknownFieldRejected(GinkgoTB())
	})
	It("TestValidationFailures", func() {
		bddTestValidationFailures(GinkgoTB())
	})
	It("TestConfigValidationBranches", func() {
		bddTestConfigValidationBranches(GinkgoTB())
	})
})

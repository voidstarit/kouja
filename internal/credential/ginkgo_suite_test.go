package credential

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "credential Suite")
}

var _ = Describe("unit tests", func() {
	It("TestGenerateRevealAndKeyFile", func() {
		bddTestGenerateRevealAndKeyFile(GinkgoTB())
	})
	It("TestGetAuthorizationAndGrants", func() {
		bddTestGetAuthorizationAndGrants(GinkgoTB())
	})
	It("TestRotateReplacesActiveCredential", func() {
		bddTestRotateReplacesActiveCredential(GinkgoTB())
	})
	It("TestInstallRevokeAndNVMeGeneration", func() {
		bddTestInstallRevokeAndNVMeGeneration(GinkgoTB())
	})
	It("TestCredentialErrorPaths", func() {
		bddTestCredentialErrorPaths(GinkgoTB())
	})
	It("TestCredentialFileOpenErrors", func() {
		bddTestCredentialFileOpenErrors(GinkgoTB())
	})
	It("TestCredentialKeyDirectoryErrors", func() {
		bddTestCredentialKeyDirectoryErrors(GinkgoTB())
	})
	It("TestRotateWithoutCredential", func() {
		bddTestRotateWithoutCredential(GinkgoTB())
	})
})

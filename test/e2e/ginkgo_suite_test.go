//go:build e2e

package e2e

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "e2e Suite")
}

var _ = Describe("unit tests", func() {
	It("TestCSIIscsiProvisionAndIO", func() {
		bddTestCSIIscsiProvisionAndIO(GinkgoTB())
	})
	It("TestCSICredentialRotationOnReattach", func() {
		bddTestCSICredentialRotationOnReattach(GinkgoTB())
	})
})

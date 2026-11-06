package auth

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "auth Suite")
}

var _ = Describe("unit tests", func() {
	It("TestParseRolesAndErrors", func() {
		bddTestParseRolesAndErrors(GinkgoTB())
	})
})

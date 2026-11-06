//go:build csi_sanity

package csi_sanity

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "csi_sanity Suite")
}

var _ = Describe("unit tests", func() {
	It("TestSanity", func() {
		bddTestSanity(GinkgoTB())
	})
})

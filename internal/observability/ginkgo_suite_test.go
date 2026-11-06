package observability

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "observability Suite")
}

var _ = Describe("unit tests", func() {
	It("TestMetricsHandler", func() {
		bddTestMetricsHandler(GinkgoTB())
	})
	It("TestMetricsRenderAndNilReceiver", func() {
		bddTestMetricsRenderAndNilReceiver(GinkgoTB())
	})
})

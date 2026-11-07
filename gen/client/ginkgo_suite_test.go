package client

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "client Suite")
}

var _ = Describe("unit tests", func() {
	It("TestLoadTLSConfig", func() {
		bddTestLoadTLSConfig(GinkgoTB())
	})
	It("TestGRPCClient", func() {
		bddTestGRPCClient(GinkgoTB())
	})
	It("TestGRPCRequestIDInterceptor", func() {
		bddTestGRPCRequestIDInterceptor(GinkgoTB())
	})
	It("TestHTTPClientCoverage", func() {
		bddTestHTTPClientCoverage(GinkgoTB())
	})
	It("TestHTTPClientGatewayCompatibility", func() {
		bddTestHTTPClientGatewayCompatibility(GinkgoTB())
	})
	It("TestHTTPClientBasePathAndRequestID", func() {
		bddTestHTTPClientBasePathAndRequestID(GinkgoTB())
	})
	It("TestHTTPClientResponseDecoding", func() {
		bddTestHTTPClientResponseDecoding(GinkgoTB())
	})
	It("TestHTTPClientErrors", func() {
		bddTestHTTPClientErrors(GinkgoTB())
	})
	It("TestHTTPClientValidation", func() {
		bddTestHTTPClientValidation(GinkgoTB())
	})
	It("TestOpenAPISpec", func() {
		bddTestOpenAPISpec(GinkgoTB())
	})
})

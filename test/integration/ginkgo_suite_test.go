//go:build integration

package integration

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "integration Suite")
}

var _ = Describe("unit tests", func() {
	It("TestISCSIAdapterForceUnexportActiveSession", func() {
		bddTestISCSIAdapterForceUnexportActiveSession(GinkgoTB())
	})
	It("TestISCSIAdapterLifecycle", func() {
		bddTestISCSIAdapterLifecycle(GinkgoTB())
	})
	It("TestCommandBackendLifecycle", func() {
		bddTestCommandBackendLifecycle(GinkgoTB())
	})
	It("TestNVMeTLSAdapterLifecycle", func() {
		bddTestNVMeTLSAdapterLifecycle(GinkgoTB())
	})
})

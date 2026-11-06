package agent

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	gomega "github.com/onsi/gomega"
)

func TestGinkgo(t *testing.T) {
	gomega.RegisterFailHandler(Fail)
	RunSpecs(t, "agent Suite")
}

func bddSubtest(t testing.TB, name string, fn func(testing.TB)) {
	By(name)
	fn(t)
}

var _ = Describe("unit tests", func() {
	It("TestResolveDriftMarkActions", func() {
		bddTestResolveDriftMarkActions(GinkgoTB())
	})
	It("TestResolveDriftRecreateExport", func() {
		bddTestResolveDriftRecreateExport(GinkgoTB())
	})
	It("TestAdminRegisterAndDeregisterVG", func() {
		bddTestAdminRegisterAndDeregisterVG(GinkgoTB())
	})
	It("TestAdminGrantsAndRevokesNodeCredential", func() {
		bddTestAdminGrantsAndRevokesNodeCredential(GinkgoTB())
	})
	It("TestAdminRejectsNonAdminAndDeregisterWithVolume", func() {
		bddTestAdminRejectsNonAdminAndDeregisterWithVolume(GinkgoTB())
	})
	It("TestAdminValidationBranches", func() {
		bddTestAdminValidationBranches(GinkgoTB())
	})
	It("TestAdmissionConcurrencyAndPerCaller", func() {
		bddTestAdmissionConcurrencyAndPerCaller(GinkgoTB())
	})
	It("TestAdmissionCapacityRate", func() {
		bddTestAdmissionCapacityRate(GinkgoTB())
	})
	It("TestAgentProtoMappings", func() {
		bddTestAgentProtoMappings(GinkgoTB())
	})
	It("TestCheckDependenciesWithFakeTools", func() {
		bddTestCheckDependenciesWithFakeTools(GinkgoTB())
	})
	It("TestAgentAdmissionAndIdentityErrors", func() {
		bddTestAgentAdmissionAndIdentityErrors(GinkgoTB())
	})
	It("TestInfoAndListBranches", func() {
		bddTestInfoAndListBranches(GinkgoTB())
	})
	It("TestCapacityAdmissionAndReadyBranches", func() {
		bddTestCapacityAdmissionAndReadyBranches(GinkgoTB())
	})
	It("TestVolumeServiceIdentityFailures", func() {
		bddTestVolumeServiceIdentityFailures(GinkgoTB())
	})
	It("TestCapacityUnavailableBranch", func() {
		bddTestCapacityUnavailableBranch(GinkgoTB())
	})
	It("TestRequestIDAndNilErrors", func() {
		bddTestRequestIDAndNilErrors(GinkgoTB())
	})
	It("TestReconcileWritesObservationsAndDrift", func() {
		bddTestReconcileWritesObservationsAndDrift(GinkgoTB())
	})
	It("TestServerServesGRPCAndHTTPOnOneTLSListener", func() {
		bddTestServerServesGRPCAndHTTPOnOneTLSListener(GinkgoTB())
	})
	It("TestNewServerLifecycleAndErrors", func() {
		bddTestNewServerLifecycleAndErrors(GinkgoTB())
	})
	It("TestDependencyChecksAndReconcileLoop", func() {
		bddTestDependencyChecksAndReconcileLoop(GinkgoTB())
	})
	It("TestUnaryInterceptorPropagatesIdentityAndRequestID", func() {
		bddTestUnaryInterceptorPropagatesIdentityAndRequestID(GinkgoTB())
	})
	It("TestUnaryInterceptorRejectsMissingOrInvalidCertificate", func() {
		bddTestUnaryInterceptorRejectsMissingOrInvalidCertificate(GinkgoTB())
	})
	It("TestInitializeVGsSeedsOnlyOnce", func() {
		bddTestInitializeVGsSeedsOnlyOnce(GinkgoTB())
	})
	It("TestVerifyVGsMarksUnavailableAndVolumesDrift", func() {
		bddTestVerifyVGsMarksUnavailableAndVolumesDrift(GinkgoTB())
	})
	It("TestVerifyVGsRejectsUUIDMismatch", func() {
		bddTestVerifyVGsRejectsUUIDMismatch(GinkgoTB())
	})
	It("TestCreateVolumeBuildsCanonicalIntent", func() {
		bddTestCreateVolumeBuildsCanonicalIntent(GinkgoTB())
	})
	It("TestCreateVolumeUnexportedDoesNotRequireProtocol", func() {
		bddTestCreateVolumeUnexportedDoesNotRequireProtocol(GinkgoTB())
	})
	It("TestGetVolumeOwnerScopingAndMapping", func() {
		bddTestGetVolumeOwnerScopingAndMapping(GinkgoTB())
	})
	It("TestListVolumesPaginationAndCapacity", func() {
		bddTestListVolumesPaginationAndCapacity(GinkgoTB())
	})
	It("TestOperationErrorMapsToStatus", func() {
		bddTestOperationErrorMapsToStatus(GinkgoTB())
	})
	It("TestGetCredentialRevealsSecret", func() {
		bddTestGetCredentialRevealsSecret(GinkgoTB())
	})
	It("TestVolumeServiceMutationRPCsAndCredentialValue", func() {
		bddTestVolumeServiceMutationRPCsAndCredentialValue(GinkgoTB())
	})
	It("TestVolumeServiceHelpersAndStatusErrors", func() {
		bddTestVolumeServiceHelpersAndStatusErrors(GinkgoTB())
	})
	It("TestVolumeServiceRPCErrorBranches", func() {
		bddTestVolumeServiceRPCErrorBranches(GinkgoTB())
	})
})

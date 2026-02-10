package e2e_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/neutree-ai/neutree/test/e2e/framework"
)

var (
	cfg    *framework.Config
	client *framework.Client
)

func TestE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	cfg = framework.NewConfigFromEnv()
	client = framework.NewClient(cfg.APIEndpoint)

	// Authenticate
	GinkgoWriter.Printf("Authenticating with %s as %s\n", cfg.APIEndpoint, cfg.AdminEmail)
	token, err := client.Login(cfg.AdminEmail, cfg.AdminPassword)
	Expect(err).NotTo(HaveOccurred(), "Failed to authenticate")
	client.SetToken(token)
	GinkgoWriter.Println("Authentication successful")
})

var _ = AfterSuite(func() {
	// Global cleanup if needed
	GinkgoWriter.Println("E2E test suite completed")
})

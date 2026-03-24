package e2e

import (
	"fmt"
	"os"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
)

func TestE2E(t *testing.T) {
	if Cfg.ServerURL == "" || Cfg.APIKey == "" {
		t.Skip("Skipping E2E tests: NEUTREE_SERVER_URL and NEUTREE_API_KEY must be set")
	}
	RegisterFailHandler(Fail)
	RunSpecs(t, "E2E Suite")
}

var _ = BeforeSuite(func() {
	By("Loading profile from E2E_PROFILE_PATH (if set)")
	err := LoadProfile()
	Expect(err).NotTo(HaveOccurred())

	By("Building neutree-cli binary")
	BuildCLI()
})

var _ = AfterSuite(func() {
	CleanupCLI()
})

var _ = ReportAfterSuite("TestRail Reporter", func(report Report) {
	trRunID := profileTestrailRunID()
	if trRunID == "" {
		return
	}

	var results []CaseResult
	for _, spec := range report.SpecReports {
		// Only report tests that actually ran (passed or failed)
		if !spec.State.Is(types.SpecStatePassed) && !spec.State.Is(types.SpecStateFailed) {
			continue
		}
		for _, label := range spec.Labels() {
			if len(label) > 1 && label[0] == 'C' && label[1] >= '0' && label[1] <= '9' {
				results = append(results, CaseResult{
					CaseID:  label,
					Passed:  spec.State.Is(types.SpecStatePassed),
					Comment: spec.FullText(),
				})
			}
		}
	}

	if len(results) > 0 {
		if err := ReportToTestRail(trRunID, results); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to report to TestRail: %v\n", err)
		}
	}
})

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// TestRail status IDs
const (
	trStatusPassed = 1
	trStatusFailed = 5
)

// CaseResult represents the result of a single TestRail test case.
type CaseResult struct {
	CaseID  string
	Passed  bool
	Comment string
}

// ReportToTestRail sends test results to a TestRail run.
// It reads connection info from environment variables:
//   - TESTRAIL_URL
//   - TESTRAIL_USER
//   - TESTRAIL_PASSWORD
//
// If any are missing, it silently skips reporting.
func ReportToTestRail(runID string, caseResults []CaseResult) error {
	url := os.Getenv("TESTRAIL_URL")
	user := os.Getenv("TESTRAIL_USER")
	password := os.Getenv("TESTRAIL_PASSWORD")

	if url == "" || user == "" || password == "" {
		fmt.Println("TestRail credentials not fully configured, skipping report")
		return nil
	}

	type trResult struct {
		CaseID   string `json:"case_id"`
		StatusID int    `json:"status_id"`
		Comment  string `json:"comment,omitempty"`
	}

	payload := struct {
		Results []trResult `json:"results"`
	}{}

	for _, r := range caseResults {
		statusID := trStatusPassed
		if !r.Passed {
			statusID = trStatusFailed
		}
		// Strip the "C" prefix if present (TestRail API expects numeric ID)
		caseID := r.CaseID
		if len(caseID) > 0 && caseID[0] == 'C' {
			caseID = caseID[1:]
		}

		payload.Results = append(payload.Results, trResult{
			CaseID:   caseID,
			StatusID: statusID,
			Comment:  r.Comment,
		})
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal TestRail payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/index.php?/api/v2/add_results_for_cases/%s", url, runID)

	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create TestRail request: %w", err)
	}

	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send TestRail results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("TestRail API returned status %d", resp.StatusCode)
	}

	fmt.Printf("Successfully reported %d results to TestRail run %s\n", len(caseResults), runID)

	return nil
}

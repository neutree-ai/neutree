package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
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
// It reads connection info (URL, user, password) from the profile struct.
// runID is resolved by the caller via profileTestrailRunID(), which checks
// the TESTRAIL_RUN_ID env var first, then falls back to the profile value.
// If any credentials are missing, it silently skips reporting.
func ReportToTestRail(runID string, caseResults []CaseResult) error {
	url := profile.Testrail.URL
	user := profile.Testrail.User
	password := profile.Testrail.Password

	if url == "" || user == "" || password == "" {
		fmt.Println("TestRail credentials not fully configured, skipping report")
		return nil
	}

	type trResult struct {
		CaseID   int    `json:"case_id"`
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

		caseIDInt, err := strconv.Atoi(caseID)
		if err != nil {
			fmt.Printf("Skipping invalid case ID %q: %v\n", r.CaseID, err)
			continue
		}

		payload.Results = append(payload.Results, trResult{
			CaseID:   caseIDInt,
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

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send TestRail results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TestRail API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Printf("Successfully reported %d results to TestRail run %s\n", len(caseResults), runID)

	return nil
}

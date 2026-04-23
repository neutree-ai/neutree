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
// Cases not present in the run are filtered out to avoid API errors.
// If any credentials are missing, it silently skips reporting.
// Cases not present in the run are filtered out to avoid API errors.
func ReportToTestRail(runID string, caseResults []CaseResult) error {
	url := profile.Testrail.URL
	user := profile.Testrail.User
	password := profile.Testrail.Password

	if url == "" || user == "" || password == "" {
		fmt.Println("TestRail credentials not fully configured, skipping report")
		return nil
	}

	client := &http.Client{Timeout: 30 * time.Second}

	// Fetch case IDs that exist in this run.
	validCases, err := fetchRunCaseIDs(url, user, password, runID, client)
	if err != nil {
		fmt.Printf("Warning: failed to fetch run cases, reporting all results: %v\n", err)

		validCases = nil // nil means skip filtering
	}

	type trResult struct {
		CaseID   int    `json:"case_id"`
		StatusID int    `json:"status_id"`
		Comment  string `json:"comment,omitempty"`
	}

	payload := struct {
		Results []trResult `json:"results"`
	}{}

	var skippedCount int

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

		// Skip cases not in the run (if we successfully fetched the list).
		if validCases != nil {
			if _, ok := validCases[caseIDInt]; !ok {
				skippedCount++

				continue
			}
		}

		payload.Results = append(payload.Results, trResult{
			CaseID:   caseIDInt,
			StatusID: statusID,
			Comment:  r.Comment,
		})
	}

	if skippedCount > 0 {
		fmt.Printf("Skipped %d case(s) not present in TestRail run %s\n", skippedCount, runID)
	}

	if len(payload.Results) == 0 {
		fmt.Println("No matching cases to report to TestRail")

		return nil
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

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send TestRail results: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("TestRail API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	fmt.Printf("Successfully reported %d results to TestRail run %s\n", len(payload.Results), runID)

	return nil
}

// fetchRunCaseIDs fetches all case IDs present in a TestRail run.
func fetchRunCaseIDs(baseURL, user, password, runID string, client *http.Client) (map[int]struct{}, error) {
	endpoint := fmt.Sprintf("%s/index.php?/api/v2/get_tests/%s", baseURL, runID)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, err
	}

	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get_tests returned status %d", resp.StatusCode)
	}

	var tests []struct {
		CaseID int `json:"case_id"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&tests); err != nil {
		return nil, fmt.Errorf("failed to decode get_tests response: %w", err)
	}

	result := make(map[int]struct{}, len(tests))
	for _, t := range tests {
		result[t.CaseID] = struct{}{}
	}

	return result, nil
}

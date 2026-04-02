package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
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

// parseCaseID strips the optional "C" prefix and converts a case ID string to int.
// Returns (0, false) if the string is not a valid case ID.
func parseCaseID(raw string) (int, bool) {
	s := raw
	if len(s) > 0 && s[0] == 'C' {
		s = s[1:]
	}

	id, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}

	return id, true
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
		caseIDInt, ok := parseCaseID(r.CaseID)
		if !ok {
			fmt.Printf("Skipping invalid case ID %q\n", r.CaseID)
			continue
		}

		statusID := trStatusPassed
		if !r.Passed {
			statusID = trStatusFailed
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

// --- Plan-based reporting ---

type trPlanRun struct {
	ID int `json:"id"`
}

type trPlanEntry struct {
	IncludeAll bool        `json:"include_all"`
	CaseIDs    []int       `json:"case_ids"`
	Runs       []trPlanRun `json:"runs"`
}

type trPlan struct {
	Entries []trPlanEntry `json:"entries"`
}

type trTestItem struct {
	CaseID int `json:"case_id"`
}

type trTestsPage struct {
	Tests []trTestItem `json:"tests"`
	Size  int          `json:"size"` // total count across all pages, not current page length
}

// trGet makes an authenticated GET request to the TestRail API.
func trGet(apiPath string) ([]byte, error) {
	endpoint := fmt.Sprintf("%s/index.php?/api/v2/%s", profile.Testrail.URL, apiPath)

	req, err := http.NewRequest("GET", endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.SetBasicAuth(profile.Testrail.User, profile.Testrail.Password)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(body))
	}

	return body, nil
}

// fetchRunCaseIDs retrieves all case IDs for a given run, handling pagination.
func fetchRunCaseIDs(runID int) ([]int, error) {
	var allCaseIDs []int

	offset := 0
	const limit = 250

	for {
		path := fmt.Sprintf("get_tests/%d&offset=%d&limit=%d", runID, offset, limit)

		body, err := trGet(path)
		if err != nil {
			return nil, fmt.Errorf("failed to get tests for run %d: %w", runID, err)
		}

		var page trTestsPage
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, fmt.Errorf("failed to parse tests response: %w", err)
		}

		for _, t := range page.Tests {
			allCaseIDs = append(allCaseIDs, t.CaseID)
		}

		offset += len(page.Tests)
		if offset >= page.Size {
			break
		}
	}

	return allCaseIDs, nil
}

// fetchPlanRunCases retrieves the plan structure and returns a map of runID to the set of caseIDs it covers.
func fetchPlanRunCases(planID string) (map[int]map[int]bool, error) {
	body, err := trGet("get_plan/" + planID)
	if err != nil {
		return nil, fmt.Errorf("failed to get plan %s: %w", planID, err)
	}

	var plan trPlan
	if err := json.Unmarshal(body, &plan); err != nil {
		return nil, fmt.Errorf("failed to parse plan response: %w", err)
	}

	result := make(map[int]map[int]bool)

	for _, entry := range plan.Entries {
		var caseIDs []int

		if !entry.IncludeAll {
			caseIDs = entry.CaseIDs
		} else if len(entry.Runs) > 0 {
			// When include_all is true, case_ids is empty; fetch from the first run.
			// All runs in the same entry share the same suite, so any run works.
			caseIDs, err = fetchRunCaseIDs(entry.Runs[0].ID)
			if err != nil {
				return nil, err
			}
		} else {
			fmt.Printf("Warning: plan entry has include_all=true but no runs, skipping\n")
			continue
		}

		caseIDSet := make(map[int]bool, len(caseIDs))
		for _, id := range caseIDs {
			caseIDSet[id] = true
		}

		for _, run := range entry.Runs {
			result[run.ID] = caseIDSet
		}
	}

	return result, nil
}

// ReportToTestRailPlan fetches the plan structure and distributes test results
// to the appropriate runs based on case ID membership.
func ReportToTestRailPlan(planID string, caseResults []CaseResult) error {
	if profile.Testrail.URL == "" || profile.Testrail.User == "" || profile.Testrail.Password == "" {
		fmt.Println("TestRail credentials not fully configured, skipping report")
		return nil
	}

	runCases, err := fetchPlanRunCases(planID)
	if err != nil {
		return fmt.Errorf("failed to fetch plan structure: %w", err)
	}

	// Build caseID -> []runID reverse index.
	caseToRuns := make(map[int][]int)
	for runID, cases := range runCases {
		for caseID := range cases {
			caseToRuns[caseID] = append(caseToRuns[caseID], runID)
		}
	}

	// Group results by run.
	runResults := make(map[int][]CaseResult)

	for _, r := range caseResults {
		caseIDInt, ok := parseCaseID(r.CaseID)
		if !ok {
			fmt.Printf("Skipping invalid case ID %q\n", r.CaseID)
			continue
		}

		runIDs, ok := caseToRuns[caseIDInt]
		if !ok {
			fmt.Printf("Warning: case %s not found in any run of plan %s\n", r.CaseID, planID)
			continue
		}

		for _, runID := range runIDs {
			runResults[runID] = append(runResults[runID], r)
		}
	}

	// Submit results to each run.
	var errs []string

	for runID, results := range runResults {
		if err := ReportToTestRail(strconv.Itoa(runID), results); err != nil {
			errs = append(errs, fmt.Sprintf("run %d: %v", runID, err))
		}
	}

	fmt.Printf("Plan %s: submitted results to %d run(s) (%d failed)\n", planID, len(runResults), len(errs))

	if len(errs) > 0 {
		return fmt.Errorf("failed to report to %d run(s): %s", len(errs), strings.Join(errs, "; "))
	}

	return nil
}

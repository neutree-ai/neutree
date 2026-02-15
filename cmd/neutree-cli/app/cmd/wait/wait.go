package wait

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tidwall/gjson"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
)

// --- options & command ---

type waitOptions struct {
	workspace string
	forCond   string
	timeout   time.Duration
	interval  time.Duration
}

// NewWaitCmd creates the wait cobra command.
func NewWaitCmd() *cobra.Command {
	opts := &waitOptions{}

	cmd := &cobra.Command{
		Use:   "wait <KIND> <NAME>",
		Short: "Wait for a resource to reach a specific condition",
		Long: `Wait for a resource to meet a condition, then exit.

Exits 0 when the condition is met, non-zero on timeout.

Supported --for conditions:
  delete                              Wait for the resource to be deleted
  jsonpath=.status.phase=Running      Wait for a JSON path to equal a value

Examples:
  # Wait for an endpoint to reach Running phase
  neutree-cli wait endpoint my-ep -w default --for jsonpath=.status.phase=Running

  # Wait for a resource to be deleted
  neutree-cli wait endpoint my-ep -w default --for delete

  # Wait with custom timeout and poll interval
  neutree-cli wait endpoint my-ep -w default --for jsonpath=.status.phase=Running --timeout 5m --interval 10s`,
		Args:          cobra.ExactArgs(2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWait(opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name (ignored for Workspace kind)")
	cmd.Flags().StringVar(&opts.forCond, "for", "", "Condition to wait for: \"delete\" or \"jsonpath=.path=value\" (required)")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 5*time.Minute, "Maximum time to wait")
	cmd.Flags().DurationVar(&opts.interval, "interval", 5*time.Second, "Poll interval")

	_ = cmd.MarkFlagRequired("for")

	return cmd
}

// --- run logic ---

func runWait(opts *waitOptions, args []string) error {
	cond, err := parseForCondition(opts.forCond)
	if err != nil {
		return err
	}

	c, err := global.NewClient()
	if err != nil {
		return err
	}

	kind, err := c.Generic.ResolveKind(args[0])
	if err != nil {
		return err
	}

	name := args[1]

	poll := func() (bool, error) {
		data, err := c.Generic.Get(kind, opts.workspace, name)
		if err != nil {
			// For delete condition, "not found" means success
			if cond.matchNotFound() && strings.Contains(err.Error(), "not found") {
				return true, nil
			}

			return false, err
		}

		return cond.match(data), nil
	}

	// Initial check
	if done, err := poll(); err != nil {
		return err
	} else if done {
		fmt.Printf("%s/%s condition met\n", kind, name)
		return nil
	}

	// Poll loop
	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	timer := time.NewTimer(opts.timeout)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			return fmt.Errorf("timeout waiting for %s/%s: %s", kind, name, cond)
		case <-ticker.C:
			done, err := poll()
			if err != nil {
				return err
			}

			if done {
				fmt.Printf("%s/%s condition met\n", kind, name)
				return nil
			}
		}
	}
}

// --- condition parsing & matching ---

// condition represents a parsed --for value.
type condition interface {
	match(data json.RawMessage) bool
	matchNotFound() bool
	String() string
}

// parseForCondition parses the --for flag value into a condition.
// Supported formats:
//   - delete
//   - jsonpath=.status.phase=Running
func parseForCondition(s string) (condition, error) {
	if s == "delete" {
		return deleteCondition{}, nil
	}

	if expr, ok := strings.CutPrefix(s, "jsonpath="); ok {
		// Strip optional leading dot: .status.phase → status.phase
		expr = strings.TrimPrefix(expr, ".")

		parts := strings.SplitN(expr, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("invalid --for value %q: jsonpath format is jsonpath=.path=value", s)
		}

		return jsonpathCondition{path: parts[0], value: parts[1]}, nil
	}

	return nil, fmt.Errorf("invalid --for value %q: must be \"delete\" or \"jsonpath=.path=value\"", s)
}

// deleteCondition waits for the resource to not exist.
type deleteCondition struct{}

func (d deleteCondition) match(json.RawMessage) bool { return false }
func (d deleteCondition) matchNotFound() bool        { return true }
func (d deleteCondition) String() string              { return "delete" }

// jsonpathCondition waits for a jsonpath expression to match a value.
type jsonpathCondition struct {
	path  string // gjson path, e.g. "status.phase"
	value string
}

func (j jsonpathCondition) match(data json.RawMessage) bool {
	return strings.EqualFold(gjson.GetBytes(data, j.path).String(), j.value)
}

func (j jsonpathCondition) matchNotFound() bool { return false }

func (j jsonpathCondition) String() string {
	return fmt.Sprintf("jsonpath=%s=%s", j.path, j.value)
}

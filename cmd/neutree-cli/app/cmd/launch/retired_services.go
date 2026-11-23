package launch

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/pkg/errors"

	"github.com/neutree-ai/neutree/pkg/command"
)

// retiredCoreServices were part of the core compose project in earlier
// releases. `compose up` leaves their containers running, so launch removes them.
var retiredCoreServices = []string{"postgres-meta"}

// removeRetiredCoreServices removes containers of retired services, matched by
// the compose project and service labels so a same-named unrelated container stays.
func removeRetiredCoreServices(exec command.Executor, out io.Writer) error {
	for _, svc := range retiredCoreServices {
		ids, err := exec.Execute(context.Background(), "docker", []string{
			"ps", "-aq",
			"--filter", "label=com.docker.compose.project=" + neutreeCoreComposeProject,
			"--filter", "label=com.docker.compose.service=" + svc,
		})
		if err != nil {
			return errors.Wrapf(err, "list containers of retired service %s: %s", svc, strings.TrimSpace(string(ids)))
		}

		for _, id := range strings.Fields(string(ids)) {
			if res, err := exec.Execute(context.Background(), "docker", []string{"rm", "-f", id}); err != nil {
				return errors.Wrapf(err, "remove container %s of retired service %s: %s", id, svc, strings.TrimSpace(string(res)))
			}

			fmt.Fprintf(out, "removed container %s of retired service %s\n", id, svc)
		}
	}

	return nil
}

package model

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/pkg/client"
)

func NewListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all models in the registry",
		Long:  `List all models in the registry with their basic information`,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientOptions := []client.ClientOption{
				client.WithAPIKey(apiKey),
			}

			if insecure {
				clientOptions = append(clientOptions, client.WithInsecureSkipVerify())
			}

			// Create client
			c := client.NewClient(serverURL, clientOptions...)
			_, err := c.ModelRegistries.Get(workspace, registry) // Ensure registry exists
			if err != nil {
				return fmt.Errorf("failed to get model registry %s: %w", registry, err)
			}

			// List models
			models, err := c.Models.List(workspace, registry, "")
			if err != nil {
				return fmt.Errorf("failed to list models: %w", err)
			}

			// Use tabwriter to format output
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tVERSIONS\tSIZE\tCREATION TIME")

			for _, model := range models {
				versions := ""
				size := ""
				creationTime := ""

				if len(model.Versions) > 0 {
					// Show the first version and a count if there are more
					versions = model.Versions[0].Name
					if len(model.Versions) > 1 {
						versions += fmt.Sprintf(" (+%d more)", len(model.Versions)-1)
					}

					// Use the first version's size and creation time
					size = model.Versions[0].Size
					creationTime = model.Versions[0].CreationTime
				}

				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
					model.Name,
					versions,
					size,
					creationTime)
			}
			w.Flush()

			return nil
		},
	}

	return cmd
}

package model

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/pkg/client"
)

func NewGetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get [model_name:version]",
		Short: "Get detailed information about a model",
		Long:  `Get detailed information about a specific model in the registry`,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			modelTag := args[0]

			// Parse model tag
			modelName, version, err := client.ParseModelTag(modelTag)
			if err != nil {
				return err
			}

			clientOptions := []client.ClientOption{
				client.WithAPIKey(apiKey),
			}

			if insecure {
				clientOptions = append(clientOptions, client.WithInsecureSkipVerify())
			}

			// Create client
			c := client.NewClient(serverURL, clientOptions...)
			_, err = c.ModelRegistries.Get(workspace, registry) // Ensure registry exists
			if err != nil {
				return fmt.Errorf("failed to get model registry %s: %w", registry, err)
			}

			// Get model details
			modelVersion, err := c.Models.Get(workspace, registry, modelName, version)
			if err != nil {
				return fmt.Errorf("failed to get model %s: %w", modelTag, err)
			}

			// Use tabwriter for formatted output
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Name:\t%s\n", modelName)
			fmt.Fprintf(w, "Version:\t%s\n", modelVersion.Name)
			fmt.Fprintf(w, "Size:\t%s\n", modelVersion.Size)
			fmt.Fprintf(w, "Creation Time:\t%s\n", modelVersion.CreationTime)
			if modelVersion.Module != "" {
				fmt.Fprintf(w, "Module:\t%s\n", modelVersion.Module)
			}

			if len(modelVersion.Labels) > 0 {
				fmt.Fprintln(w, "Labels:")
				for k, v := range modelVersion.Labels {
					fmt.Fprintf(w, "  %s:\t%s\n", k, v)
				}
			}

			if modelVersion.Description != "" {
				fmt.Fprintf(w, "Description:\t%s\n", modelVersion.Description)
			}

			w.Flush()
			return nil
		},
	}

	return cmd
}

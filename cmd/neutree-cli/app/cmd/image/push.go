package image

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	cliImage "github.com/neutree-ai/neutree/internal/cli/image"
	"github.com/neutree-ai/neutree/pkg/client"
)

func NewPushCmd() *cobra.Command {
	var target string
	var archivePath string
	var registryUsername string
	var registryPassword string

	cmd := &cobra.Command{
		Use:   "push [image]",
		Short: "Push a container image to a Neutree-managed image registry",
		Long: `Push a local container image to the image registry managed by Neutree.

The image is tagged into the registry the same way Neutree renders image
references, so the printed target reference can be used directly as an engine
image override (e.g. the flex engine's engine_args.image).

The image must exist in the local Docker daemon; use --archive to load it from
a "docker save" tar first. Registry credentials are read from the Neutree image
registry resource, and can be overridden with --registry-username/--registry-password.`,
		Example: `  # Push a locally built image to the default image registry
  neutree-cli image push my-workload:v1

  # Load from an offline archive and push under a different repository
  neutree-cli image push my-workload:v1 --archive my-workload.tar --target team-a/my-workload:v1`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := global.NewClient(client.WithTimeout(0))
			if err != nil {
				return err
			}

			pusher, err := cliImage.NewPusher(cmd.OutOrStdout())
			if err != nil {
				return err
			}

			target, err := cliImage.Push(cmd.Context(), c.ImageRegistries, pusher, cliImage.PushOptions{
				Workspace:   workspace,
				Registry:    registry,
				SourceImage: args[0],
				Target:      target,
				ArchivePath: archivePath,
				Username:    registryUsername,
				Password:    registryPassword,
			})
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Image pushed successfully: %s\n", target)

			return nil
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "Repository[:tag] inside the target registry (default: derived from the source image)")
	cmd.Flags().StringVar(&archivePath, "archive", "", `Load the image from a "docker save" tar archive before pushing`)
	cmd.Flags().StringVar(&registryUsername, "registry-username", "", "Username for the image registry (default: the registry's stored credentials)")
	cmd.Flags().StringVar(&registryPassword, "registry-password", "", "Password for the image registry (default: the registry's stored credentials)")

	return cmd
}

package global

import (
	"os"

	"github.com/spf13/cobra"
)

var (
	ServerURL string
	APIKey    string
	Insecure  bool
)

// AddFlags registers --server-url, --api-key and --insecure as persistent flags on the root command.
func AddFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&ServerURL, "server-url", "", "API server URL (env: NEUTREE_SERVER_URL)")
	cmd.PersistentFlags().StringVar(&APIKey, "api-key", "", "API key (env: NEUTREE_API_KEY)")
	cmd.PersistentFlags().BoolVar(&Insecure, "insecure", false, "Skip TLS verification")
}

// ResolveEnv fills in flag values from environment variables when not set via flags.
func ResolveEnv() {
	if ServerURL == "" {
		ServerURL = os.Getenv("NEUTREE_SERVER_URL")
	}

	if APIKey == "" {
		APIKey = os.Getenv("NEUTREE_API_KEY")
	}
}

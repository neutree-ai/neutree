package options

import (
	"os"

	"github.com/spf13/pflag"
)

type StorageOptions struct {
	AccessURL     string
	JwtSecret     string
	DBURI         string
	NotifyEnabled bool
	NotifyChannel string
}

func NewStorageOptions() *StorageOptions {
	dbURI := os.Getenv("PGRST_DB_URI")

	return &StorageOptions{
		AccessURL:     "http://postgrest:6432",
		JwtSecret:     "jwt_secret",
		DBURI:         dbURI,
		NotifyEnabled: false,
		NotifyChannel: "neutree_reconcile",
	}
}

func (o *StorageOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AccessURL, "storage-access-url", o.AccessURL, "postgrest url")
	fs.StringVar(&o.JwtSecret, "storage-jwt-secret", o.JwtSecret, "storage auth token")
	fs.StringVar(&o.DBURI, "storage-db-uri", o.DBURI, "postgres dsn for LISTEN/NOTIFY trigger listener")
	fs.BoolVar(&o.NotifyEnabled, "storage-notify-enabled", o.NotifyEnabled, "enable DB LISTEN/NOTIFY trigger for reconcile")
	fs.StringVar(&o.NotifyChannel, "storage-notify-channel", o.NotifyChannel, "postgres NOTIFY channel used for reconcile trigger")
}

func (o *StorageOptions) Validate() error {
	return nil
}

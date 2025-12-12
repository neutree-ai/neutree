package version

import (
	"fmt"
	"runtime"
)

var (
	// These variables are set via -ldflags during build
	gitCommit  = "unknown"
	appVersion = "dev"
	buildTime  = "unknown"
)

// Info contains version information
type Info struct {
	GitCommit  string `json:"git_commit"`
	AppVersion string `json:"app_version"`
	BuildTime  string `json:"build_time"`
	GoVersion  string `json:"go_version"`
	Platform   string `json:"platform"`
}

// Get returns version information
func Get() Info {
	return Info{
		GitCommit:  gitCommit,
		AppVersion: appVersion,
		BuildTime:  buildTime,
		GoVersion:  runtime.Version(),
		Platform:   fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
	}
}

// String returns version information as a formatted string
func (i Info) String() string {
	return fmt.Sprintf("Version: %s\nGit Commit: %s\nBuild Time: %s\nGo Version: %s\nPlatform: %s",
		i.AppVersion, i.GitCommit, i.BuildTime, i.GoVersion, i.Platform)
}

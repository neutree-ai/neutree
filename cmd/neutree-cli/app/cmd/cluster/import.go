package cluster

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	registryClient "github.com/neutree-ai/neutree/pkg/registry"
	"github.com/spf13/cobra"
)

// newRegistryClient is a hookable factory for tests to override
var newRegistryClient func() registryClient.RegistryClient = func() registryClient.RegistryClient { return registryClient.NewDefaultRegistryClient() }

// NewClusterImportCmd imports an SSH-type cluster image package into a registry
func NewClusterImportCmd() *cobra.Command {
	var offlineImage string
	var registry string
	var registryUser string
	var registryPassword string
	var pushRetry int

	cmd := &cobra.Command{
		Use:   "import",
		Short: "导入集群离线镜像包到指定仓库",
		RunE: func(cmd *cobra.Command, args []string) error {
			if offlineImage == "" || registry == "" {
				return fmt.Errorf("必须指定 --offline-image 和 --registry 参数")
			}

			// 解压镜像包到临时目录
			tmpDir, err := os.MkdirTemp("", "cluster-image-import-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmpDir)

			f, err := os.Open(offlineImage)
			if err != nil {
				return err
			}
			defer f.Close()

			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
			defer gz.Close()

			tarReader := tar.NewReader(gz)

			for {
				hdr, err := tarReader.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				outPath := filepath.Join(tmpDir, hdr.Name)
				outFile, err := os.Create(outPath)
				if err != nil {
					return err
				}
				if _, err := io.Copy(outFile, tarReader); err != nil {
					outFile.Close()
					return err
				}
				outFile.Close()
			}

			// 如果包含 package manifest.json，则优先基于 manifest 映射镜像文件名 -> tag 并上传
			manifestPath := filepath.Join(tmpDir, "manifest.json")
			if _, err := os.Stat(manifestPath); err == nil {
				pm, err := registryClient.ParsePackageManifest(manifestPath)
				if err != nil {
					return err
				}
				for _, img := range pm.Images {
					imgPath := filepath.Join(tmpDir, img.File)
					fmt.Printf("推送镜像: %s -> %s\n", imgPath, registry)
					// use go-containerregistry to push the tar directly to target registry
					client := newRegistryClient()
					if err := client.PushImageTarToRegistryWithAuth(context.Background(), imgPath, registry, registryUser, registryPassword, pushRetry); err != nil {
						return err
					}
				}
			} else {
				// 遍历临时目录，使用 go-containerregistry 将镜像上传到 registry
				entries, err := os.ReadDir(tmpDir)
				if err != nil {
					return err
				}
				for _, entry := range entries {
					imgPath := filepath.Join(tmpDir, entry.Name())
					fmt.Printf("推送镜像到仓库: %s -> %s\n", imgPath, registry)
					// use go-containerregistry to push the tar directly to target registry
					client := newRegistryClient()
					if err := client.PushImageTarToRegistryWithAuth(context.Background(), imgPath, registry, registryUser, registryPassword, pushRetry); err != nil {
						return err
					}
				}

			}

			fmt.Println("所有镜像导入并推送完成。")
			return nil
		},
	}

	cmd.Flags().StringVar(&offlineImage, "offline-image", "", "离线镜像包路径 (tar.gz)")
	cmd.Flags().StringVar(&registry, "registry", "", "目标镜像仓库地址")
	cmd.Flags().StringVar(&registryUser, "registry-username", "", "用于登录目标 registry 的用户名（如果需要）")
	cmd.Flags().StringVar(&registryPassword, "registry-password", "", "用于登录目标 registry 的密码（如果需要）")
	cmd.Flags().IntVar(&pushRetry, "push-retry", 3, "当使用 SDK 推送镜像时的重试次数")

	return cmd
}

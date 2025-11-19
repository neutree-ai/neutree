package cmd

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

func NewClusterImportCmd() *cobra.Command {
	var offlineImage string
	var registry string

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
			gz, err := gzip.NewReader(f)
			if err != nil {
				return err
			}
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

			// 遍历临时目录，docker load 并推送到 registry
			entries, err := os.ReadDir(tmpDir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				imgPath := filepath.Join(tmpDir, entry.Name())
				fmt.Printf("推送镜像到仓库: %s -> %s\n", imgPath, registry)
				client := registryClient.NewDefaultRegistryClient()
				if err := client.PushImageTarToRegistryWithAuth(context.Background(), imgPath, registry, "", "", 3); err != nil {
					return err
				}
			}

			fmt.Println("所有镜像导入并推送完成。")
			return nil
		},
	}

	cmd.Flags().StringVar(&offlineImage, "offline-image", "", "离线镜像包路径 (tar.gz)")
	cmd.Flags().StringVar(&registry, "registry", "", "目标镜像仓库地址")

	return cmd
}

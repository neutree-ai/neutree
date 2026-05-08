package e2e

import (
	"encoding/json"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var _ = Describe("CLI", Ordered, Label("cli"), func() {
	BeforeAll(func() {
		requireImageRegistryProfile()
	})

	// --- apply ---

	Describe("apply", Label("apply"), func() {

		It("should create resources from multi-doc YAML with dependency ordering", Label("C2642183"), func() {
			irName := "e2e-cli-ir-" + Cfg.RunID
			clusterName := "e2e-cli-cls-" + Cfg.RunID

			headIP, _, sshUser, sshPrivateKey := requireSSHProfile()

			DeferCleanup(func() {
				RunCLI("delete", "cluster", clusterName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
				RunCLI("delete", "imageregistry", irName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			// Multi-doc YAML in REVERSE dependency order: Cluster(2) before ImageRegistry(1).
			// CLI apply sorts by KindPriority: ImageRegistry first, then Cluster.
			clusterPath := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"image_registry":  irName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			irPath := renderImageRegistryYAML(map[string]any{
				"name": irName,
			})

			// Concatenate in reverse order: Cluster(2) → ImageRegistry(1)
			multiDocPath := writeMultiDocYAML(clusterPath, irPath)
			defer func() {
				os.Remove(multiDocPath)
				os.Remove(clusterPath)
				os.Remove(irPath)
			}()

			r := RunCLI("apply", "-f", multiDocPath)
			ExpectSuccess(r)

			// Verify dependency ordering: ImageRegistry(1) created before Cluster(2).
			lines := strings.Split(r.Stdout, "\n")
			irIdx, clsIdx := -1, -1

			for i, line := range lines {
				if strings.Contains(line, "ImageRegistry") && strings.Contains(line, "created") {
					irIdx = i
				}

				if strings.Contains(line, "Cluster") && strings.Contains(line, "created") {
					clsIdx = i
				}
			}

			Expect(irIdx).To(BeNumerically(">=", 0), "ImageRegistry should be created")
			Expect(clsIdx).To(BeNumerically(">=", 0), "Cluster should be created")
			Expect(irIdx).To(BeNumerically("<", clsIdx),
				"ImageRegistry (priority 1) should be created before Cluster (priority 2)")
		})

		It("should update existing resources with --force-update", Label("C2642184"), func() {
			name := "e2e-cli-upd-" + Cfg.RunID
			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			// Create resource first.
			irDefaults := map[string]any{
				"E2E_IMAGE_REGISTRY":      name,
				"E2E_IMAGE_REGISTRY_REPO": "original-repo",
			}

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", irDefaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("created"))

			// Update with --force-update and new repository.
			irDefaults["E2E_IMAGE_REGISTRY_REPO"] = "updated-repo"

			yamlPath2, err := renderTemplateToTempFile("testdata/image-registry.yaml", irDefaults)
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath2)

			r = RunCLI("apply", "-f", yamlPath2, "--force-update")
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("updated"))

			// Verify the update took effect via typed struct comparison.
			r = RunCLI("get", "imageregistry", name, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			var updated v1.ImageRegistry
			Expect(json.Unmarshal([]byte(r.Stdout), &updated)).To(Succeed())
			Expect(updated.Spec).NotTo(BeNil())
			Expect(updated.Spec.Repository).To(Equal("updated-repo"),
				"repository should be updated to 'updated-repo' after --force-update")

			// Without --force-update, should skip.
			r = RunCLI("apply", "-f", yamlPath2)
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("skipped"))
		})

		It("should fail on invalid kind or malformed YAML", Label("C2642185"), func() {
			// Invalid kind.
			yamlBadKind := `apiVersion: v1
kind: FooBar
metadata:
  name: test
  workspace: default
spec: {}
`
			tmpFile, err := os.CreateTemp("", "e2e-cli-badkind-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile.WriteString(yamlBadKind)
			Expect(err).NotTo(HaveOccurred())
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			r := RunCLI("apply", "-f", tmpFile.Name())
			ExpectFailed(r)

			// Malformed YAML.
			tmpFile2, err := os.CreateTemp("", "e2e-cli-badyaml-*.yaml")
			Expect(err).NotTo(HaveOccurred())
			_, err = tmpFile2.WriteString("not: valid: yaml: {{{}}")
			Expect(err).NotTo(HaveOccurred())
			tmpFile2.Close()
			defer os.Remove(tmpFile2.Name())

			r = RunCLI("apply", "-f", tmpFile2.Name())
			ExpectFailed(r)
		})
	})

	// --- get ---

	Describe("get", Ordered, Label("get"), func() {
		var irName1, irName2 string

		BeforeAll(func() {
			irName1 = "e2e-cli-get-1-" + Cfg.RunID
			irName2 = "e2e-cli-get-2-" + Cfg.RunID

			for _, name := range []string{irName1, irName2} {
				yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
					"E2E_IMAGE_REGISTRY": name,
				})
				Expect(err).NotTo(HaveOccurred())

				r := RunCLI("apply", "-f", yamlPath)
				ExpectSuccess(r)
				os.Remove(yamlPath)
			}
		})

		AfterAll(func() {
			RunCLI("delete", "imageregistry", irName1, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			RunCLI("delete", "imageregistry", irName2, "-w", profileWorkspace(), "--force", "--ignore-not-found")
		})

		It("should list resources in table format", Label("C2642186"), func() {
			r := RunCLI("get", "imageregistry", "-w", profileWorkspace())
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("NAME"))
			Expect(r.Stdout).To(ContainSubstring("WORKSPACE"))
			Expect(r.Stdout).To(ContainSubstring(irName1))
			Expect(r.Stdout).To(ContainSubstring(irName2))
		})

		It("should output single resource as JSON and YAML", Label("C2642187"), func() {
			By("JSON output")
			r := RunCLI("get", "imageregistry", irName1, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)

			var ir v1.ImageRegistry
			Expect(json.Unmarshal([]byte(r.Stdout), &ir)).To(Succeed())

			Expect(ir.Metadata).NotTo(BeNil())
			Expect(ir.Metadata.Name).To(Equal(irName1))
			Expect(ir.Metadata.Workspace).To(Equal(profileWorkspace()))
			Expect(ir.Spec).NotTo(BeNil())
			Expect(ir.Spec.URL).To(Equal(profile.ImageRegistry.URL))
			Expect(ir.Spec.Repository).To(Equal(profile.ImageRegistry.Repository))

			By("YAML output")
			r = RunCLI("get", "imageregistry", irName1, "-w", profileWorkspace(), "-o", "yaml")
			ExpectSuccess(r)

			var irYAML v1.ImageRegistry
			Expect(yaml.Unmarshal([]byte(r.Stdout), &irYAML)).To(Succeed())

			Expect(irYAML.Metadata).NotTo(BeNil())
			Expect(irYAML.Metadata.Name).To(Equal(irName1))
			Expect(irYAML.Metadata.Workspace).To(Equal(profileWorkspace()))
			Expect(irYAML.Spec).NotTo(BeNil())
			Expect(irYAML.Spec.URL).To(Equal(profile.ImageRegistry.URL))
			Expect(irYAML.Spec.Repository).To(Equal(profile.ImageRegistry.Repository))
		})

		It("should fail for non-existent resource", func() {
			r := RunCLI("get", "imageregistry", "nonexistent-"+Cfg.RunID, "-w", profileWorkspace())
			ExpectFailed(r)
		})
	})

	// --- wait ---

	Describe("wait", Label("wait"), func() {

		It("should exit 0 when jsonpath condition is already met", Label("C2642189"), func() {
			// Use ImageRegistry which reaches Connected within seconds — this tests the
			// CLI wait mechanism itself (jsonpath matching + polling + exit code), not the
			// duration of the wait. The wait command is resource-agnostic.
			name := "e2e-cli-wait-" + Cfg.RunID
			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
				"E2E_IMAGE_REGISTRY": name,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			// Wait for status.phase=Connected (image registry should connect quickly).
			r = RunCLI("wait", "imageregistry", name,
				"-w", profileWorkspace(),
				"--for", "jsonpath=.status.phase=Connected",
				"--timeout", "3m",
			)
			ExpectSuccess(r)
		})

		It("should exit 0 when waiting for delete and resource is deleted", Label("C2642190"), func() {
			name := "e2e-cli-waitdel-" + Cfg.RunID

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
				"E2E_IMAGE_REGISTRY": name,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			// Delete the resource.
			RunCLI("delete", "imageregistry", name, "-w", profileWorkspace(), "--force")

			// Wait for delete should succeed.
			r = RunCLI("wait", "imageregistry", name,
				"-w", profileWorkspace(),
				"--for", "delete",
				"--timeout", "2m",
			)
			ExpectSuccess(r)
		})

		It("should exit non-zero on timeout", Label("C2642191"), func() {
			// Wait for a condition that will never be met, with short timeout.
			r := RunCLI("wait", "workspace", profileWorkspace(),
				"--for", "jsonpath=.status.phase=NeverGonnaHappen",
				"--timeout", "5s",
			)
			ExpectFailed(r)
			Expect(r.Stdout).To(ContainSubstring("timeout"))
		})
	})

	// --- delete ---

	Describe("delete", Label("delete"), func() {

		It("should delete a single resource", Label("C2642192"), func() {
			name := "e2e-cli-del-" + Cfg.RunID

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]any{
				"E2E_IMAGE_REGISTRY": name,
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(yamlPath)

			r := RunCLI("apply", "-f", yamlPath)
			ExpectSuccess(r)

			// Delete by kind + name.
			r = RunCLI("delete", "imageregistry", name, "-w", profileWorkspace(), "--force")
			ExpectSuccess(r)

			// Verify deleted.
			r = RunCLI("get", "imageregistry", name, "-w", profileWorkspace())
			ExpectFailed(r)
		})

		It("should delete resources from YAML file in reverse dependency order", Label("C2642193"), func() {
			irName := "e2e-cli-delf-ir-" + Cfg.RunID
			clusterName := "e2e-cli-delf-cls-" + Cfg.RunID

			headIP, _, sshUser, sshPrivateKey := requireSSHProfile()

			// Multi-doc: ImageRegistry(priority 1) + Cluster(priority 2).
			irPath := renderImageRegistryYAML(map[string]any{"name": irName})
			clusterPath := renderSSHClusterYAML(map[string]any{
				"name":            clusterName,
				"image_registry":  irName,
				"head_ip":         headIP,
				"ssh_user":        sshUser,
				"ssh_private_key": sshPrivateKey,
			})

			multiDocPath := writeMultiDocYAML(irPath, clusterPath)
			defer func() {
				os.Remove(multiDocPath)
				os.Remove(irPath)
				os.Remove(clusterPath)
			}()

			// Create resources.
			r := RunCLI("apply", "-f", multiDocPath)
			ExpectSuccess(r)

			// Delete from file — reverse dependency: Cluster(2) before ImageRegistry(1).
			r = RunCLI("delete", "-f", multiDocPath, "--force")
			ExpectSuccess(r)

			// Verify deletion order: Cluster deleted before ImageRegistry.
			lines := strings.Split(r.Stdout, "\n")
			clsIdx, irIdx := -1, -1

			for i, line := range lines {
				if strings.Contains(line, "Cluster") && strings.Contains(line, "delet") {
					clsIdx = i
				}

				if strings.Contains(line, "ImageRegistry") && strings.Contains(line, "delet") {
					irIdx = i
				}
			}

			Expect(clsIdx).To(BeNumerically(">=", 0), "Cluster should be deleted")
			Expect(irIdx).To(BeNumerically(">=", 0), "ImageRegistry should be deleted")
			Expect(clsIdx).To(BeNumerically("<", irIdx),
				"Cluster (priority 2) should be deleted before ImageRegistry (priority 1)")

			// Verify both deleted.
			r = RunCLI("get", "cluster", clusterName, "-w", profileWorkspace())
			ExpectFailed(r)

			r = RunCLI("get", "imageregistry", irName, "-w", profileWorkspace())
			ExpectFailed(r)
		})

		It("should silently skip with --ignore-not-found", Label("C2642194"), func() {
			r := RunCLI("delete", "imageregistry", "nonexistent-"+Cfg.RunID,
				"-w", profileWorkspace(), "--force", "--ignore-not-found")
			ExpectSuccess(r)
		})
	})

})

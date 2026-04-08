package e2e

import (
	"encoding/json"
	"os"
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CLI", Label("cli"), func() {

	// --- apply ---

	Describe("apply", Label("apply"), func() {

		It("should create resources from multi-doc YAML with dependency ordering", Label("C2642183"), func() {
			irName := "e2e-cli-ir-" + Cfg.RunID
			clusterName := "e2e-cli-cls-" + Cfg.RunID
			epName := "e2e-cli-ep-" + Cfg.RunID

			headIP, _, sshUser, sshPrivateKey := requireSSHEnv()

			DeferCleanup(func() {
				RunCLI("delete", "endpoint", epName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
				RunCLI("delete", "cluster", clusterName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
				RunCLI("wait", "cluster", clusterName, "-w", profileWorkspace(), "--for", "delete", "--timeout", "5m")
				RunCLI("delete", "imageregistry", irName, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			// Multi-doc YAML in reverse dependency order: Endpoint → Cluster → ImageRegistry.
			// apply must reorder: ImageRegistry first, then Cluster, then Endpoint.
			multiYamlPath, err := renderMultiYamlTemplate([]struct {
				path     string
				defaults map[string]string
			}{
				{path: "testdata/endpoint.yaml", defaults: map[string]string{
					"E2E_ENDPOINT_NAME": epName,
					"E2E_CLUSTER_NAME":  clusterName,
				}},
				{path: "testdata/ssh-cluster.yaml", defaults: map[string]string{
					"CLUSTER_NAME":            clusterName,
					"CLUSTER_IMAGE_REGISTRY":  irName,
					"CLUSTER_SSH_HEAD_IP":     headIP,
					"CLUSTER_SSH_USER":        sshUser,
					"CLUSTER_SSH_PRIVATE_KEY": sshPrivateKey,
				}},
				{path: "testdata/image-registry.yaml", defaults: map[string]string{
					"E2E_IMAGE_REGISTRY": irName,
				}},
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(multiYamlPath)

			r := RunCLI("apply", "-f", multiYamlPath)
			ExpectSuccess(r)

			// Verify dependency ordering: ImageRegistry created before Cluster before Endpoint.
			lines := strings.Split(r.Stdout, "\n")
			irIdx, clsIdx, epIdx := -1, -1, -1

			for i, line := range lines {
				if strings.Contains(line, "ImageRegistry") && strings.Contains(line, "created") {
					irIdx = i
				}

				if strings.Contains(line, "Cluster") && strings.Contains(line, "created") {
					clsIdx = i
				}

				if strings.Contains(line, "Endpoint") && strings.Contains(line, "created") {
					epIdx = i
				}
			}

			Expect(irIdx).To(BeNumerically(">=", 0), "ImageRegistry should be created")
			Expect(clsIdx).To(BeNumerically(">=", 0), "Cluster should be created")
			Expect(epIdx).To(BeNumerically(">=", 0), "Endpoint should be created")
			Expect(irIdx).To(BeNumerically("<", clsIdx),
				"ImageRegistry (line %d) should be created before Cluster (line %d)", irIdx, clsIdx)
			Expect(clsIdx).To(BeNumerically("<", epIdx),
				"Cluster (line %d) should be created before Endpoint (line %d)", clsIdx, epIdx)
		})

		It("should update existing resources with --force-update", Label("C2642184"), func() {
			name := "e2e-cli-upd-" + Cfg.RunID
			DeferCleanup(func() {
				RunCLI("delete", "imageregistry", name, "-w", profileWorkspace(), "--force", "--ignore-not-found")
			})

			// Create resource first.
			irDefaults := map[string]string{
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

			// Verify the update took effect.
			r = RunCLI("get", "imageregistry", name, "-w", profileWorkspace(), "-o", "json")
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("updated-repo"),
				"repository should be updated to 'updated-repo' after --force-update")
			Expect(r.Stdout).NotTo(ContainSubstring("original-repo"),
				"old repository 'original-repo' should no longer appear")

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
				yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
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

			var obj map[string]any
			Expect(json.Unmarshal([]byte(r.Stdout), &obj)).To(Succeed(),
				"output should be valid JSON")

			metadata, ok := obj["metadata"].(map[string]any)
			Expect(ok).To(BeTrue(), "should have metadata")
			Expect(metadata["name"]).To(Equal(irName1))
			Expect(metadata["workspace"]).To(Equal(profileWorkspace()))

			spec, ok := obj["spec"].(map[string]any)
			Expect(ok).To(BeTrue(), "should have spec")
			Expect(spec["url"]).To(Equal(profile.ImageRegistry.URL))
			Expect(spec["repository"]).To(Equal(profile.ImageRegistry.Repository))

			By("YAML output")
			r = RunCLI("get", "imageregistry", irName1, "-w", profileWorkspace(), "-o", "yaml")
			ExpectSuccess(r)
			Expect(r.Stdout).To(ContainSubstring("name: " + irName1))
			Expect(r.Stdout).To(ContainSubstring("workspace: " + profileWorkspace()))
			Expect(r.Stdout).To(ContainSubstring("url: " + profile.ImageRegistry.URL))
			Expect(r.Stdout).To(ContainSubstring("repository: " + profile.ImageRegistry.Repository))
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

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
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

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
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

			yamlPath, err := renderTemplateToTempFile("testdata/image-registry.yaml", map[string]string{
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
			name := "e2e-cli-delf-" + Cfg.RunID

			tmpFile, err := renderMultiYamlTemplate([]struct {
				path     string
				defaults map[string]string
			}{
				{path: "testdata/image-registry.yaml", defaults: map[string]string{"E2E_IMAGE_REGISTRY": name}},
				{path: "testdata/model-registry.yaml", defaults: map[string]string{"E2E_MODEL_REGISTRY": name}},
			})
			Expect(err).NotTo(HaveOccurred())
			defer os.Remove(tmpFile)

			// Create resources.
			r := RunCLI("apply", "-f", tmpFile)
			ExpectSuccess(r)

			// Delete from file — should delete in reverse dependency order
			// (ModelRegistry before ImageRegistry).
			r = RunCLI("delete", "-f", tmpFile, "--force")
			ExpectSuccess(r)

			// Verify deletion order: ModelRegistry deleted before ImageRegistry.
			lines := strings.Split(r.Stdout, "\n")
			mrIdx, irIdx := -1, -1

			for i, line := range lines {
				if strings.Contains(line, "ModelRegistry") && strings.Contains(line, "delet") {
					mrIdx = i
				}

				if strings.Contains(line, "ImageRegistry") && strings.Contains(line, "delet") {
					irIdx = i
				}
			}

			if mrIdx >= 0 && irIdx >= 0 {
				Expect(mrIdx).To(BeNumerically("<", irIdx),
					"ModelRegistry (line %d) should be deleted before ImageRegistry (line %d)", mrIdx, irIdx)
			}

			// Verify both deleted.
			r = RunCLI("get", "imageregistry", name, "-w", profileWorkspace())
			ExpectFailed(r)

			r = RunCLI("get", "modelregistry", name, "-w", profileWorkspace())
			ExpectFailed(r)
		})

		It("should silently skip with --ignore-not-found", Label("C2642194"), func() {
			r := RunCLI("delete", "imageregistry", "nonexistent-"+Cfg.RunID,
				"-w", profileWorkspace(), "--force", "--ignore-not-found")
			ExpectSuccess(r)
		})
	})

})


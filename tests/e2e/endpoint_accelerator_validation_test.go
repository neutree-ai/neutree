package e2e

import (
	"encoding/json"
	"net/http"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// apiValidationError mirrors the middleware validationError JSON shape returned
// on HTTP 400 for invalid endpoint accelerator resources.
type apiValidationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

var _ = Describe("Endpoint Accelerator Resource Validation", Ordered, Label("endpoint", "k8s", "api-validation"), func() {
	var (
		clusterName string
		accType     string
		accProduct  string
	)

	BeforeAll(func() {
		clusterName = setupK8sCluster("e2e-ep-accval-")

		accType, accProduct = getClusterAccelerator(clusterName)
		if accType == "" {
			Skip("Cluster does not report an accelerator type; accelerator resource validation requires an accelerator-capable cluster")
		}
	})

	AfterAll(func() {
		teardownCluster(clusterName)
	})

	endpointPayload := func(name, gpu, product string) map[string]any {
		return map[string]any{
			"metadata": map[string]any{
				"name":      name,
				"workspace": profileWorkspace(),
			},
			"spec": map[string]any{
				"cluster": clusterName,
				"resources": map[string]any{
					"gpu": gpu,
					"accelerator": map[string]any{
						"type":    accType,
						"product": product,
					},
				},
			},
		}
	}

	It("rejects a fractional physical accelerator count on create", Label("C2745662"), func() {
		epName := "e2e-ep-accval-frac-" + Cfg.RunID
		DeferCleanup(func() { deleteEndpoint(epName) })

		body, code := callNeutreeAPIWithJSON(
			http.MethodPost,
			"/api/v1/endpoints",
			endpointPayload(epName, "1.5", accProduct),
		)

		var response apiValidationError
		Expect(code).To(Equal(http.StatusBadRequest), "fractional accelerator count should be rejected, body: %s", string(body))
		Expect(json.Unmarshal(body, &response)).To(Succeed())
		Expect(response.Code).To(Equal("10230"))
		Expect(response.Hint).To(ContainSubstring("positive integer"))
	})

	It("rejects an empty accelerator product on create", Label("C2745663"), func() {
		epName := "e2e-ep-accval-emptyprod-" + Cfg.RunID
		DeferCleanup(func() { deleteEndpoint(epName) })

		body, code := callNeutreeAPIWithJSON(
			http.MethodPost,
			"/api/v1/endpoints",
			endpointPayload(epName, "1", ""),
		)

		var response apiValidationError
		Expect(code).To(Equal(http.StatusBadRequest), "empty accelerator product should be rejected, body: %s", string(body))
		Expect(json.Unmarshal(body, &response)).To(Succeed())
		Expect(response.Code).To(Equal("10230"))
		Expect(response.Hint).To(ContainSubstring("product is required"))
	})

	It("rejects an unknown accelerator product when cluster metadata lists supported products", Label("C2745664"), func() {
		epName := "e2e-ep-accval-unknownprod-" + Cfg.RunID
		DeferCleanup(func() { deleteEndpoint(epName) })

		body, code := callNeutreeAPIWithJSON(
			http.MethodPost,
			"/api/v1/endpoints",
			endpointPayload(epName, "1", "unknown-product-model"),
		)

		var response apiValidationError
		Expect(code).To(Equal(http.StatusBadRequest), "unknown accelerator product should be rejected, body: %s", string(body))
		Expect(json.Unmarshal(body, &response)).To(Succeed())
		Expect(response.Code).To(Equal("10230"))
		Expect(response.Hint).To(ContainSubstring("unsupported accelerator product"))
	})

	It("does not create partial endpoints for rejected payloads", Label("C2745665"), func() {
		for _, suffix := range []string{"frac", "emptyprod", "unknownprod"} {
			_, code := callNeutreeAPIWithJSON(
				http.MethodPost,
				"/api/v1/endpoints",
				endpointPayload("e2e-ep-accval-partial-"+suffix+"-"+Cfg.RunID, "1.5", accProduct),
			)
			Expect(code).To(Equal(http.StatusBadRequest))
		}

		r := RunCLI("get", "endpoint", "-w", profileWorkspace(), "-o", "json")
		ExpectSuccess(r)
		Expect(r.Stdout).NotTo(ContainSubstring("e2e-ep-accval-partial-"))
	})
})

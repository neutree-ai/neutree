# E2E Automation vs TestRail Run 6527 Diff Analysis (78 Shared Cases)

> Generated: 2026-04-05
> Branch: feat/e2e-cluster-endpoint-p0
> TestRail Run: http://testrail.smartx.com/index.php?/runs/view/6527

---

## 1. Overview

The E2E code contains 164 Case IDs, and TestRail Run 6527 contains 300 Cases. The intersection is **78 cases**; this document analyzes only those 78.

| Status | Count |
|--------|-------|
| Passed | 56 |
| Untested | 18 |
| Failed | 4 |

---

## 2. Problematic Cases

### Issue Type A: Insufficient E2E Assertions (False-Pass Risk)

The following cases have assertion gaps or hollow logic — even if TestRail marks them Passed, they may be false passes.

#### 1. C2613388 / C2613397 / C2613402 — Completely Hollow Assertions

**File**: `endpoint_ssh_config_test.go:186-119`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2613388 | Verify CPU config parameter takes effect (ray_actor_options.number_cpus) | Passed |
| C2613397 | Verify memory config parameter takes effect (ray_actor_options.memory) | Passed |
| C2613402 | Verify replica count config parameter takes effect (num_replicas) | Passed |

**Issue**: All three cases share the same code structure:

```go
It("should have CPU config", Label("C2613388"), func() {
    apps, err := rayH.GetServeApplications()
    Expect(err).NotTo(HaveOccurred())
    for _, appStatus := range apps.Applications {
        if len(appStatus.Deployments) > 0 {
            return  // ← returns immediately if any deployment exists, no value verification
        }
    }
    Fail("should have deployments with CPU config")
})
```

**Root Cause**: Only checks `len(appStatus.Deployments) > 0` — never verifies that CPU/Memory/Replica **actual values** match the creation configuration.

**TestRail Expectation**: Should verify `ray_actor_options.number_cpus` / `memory` / `num_replicas` values equal the configured values at creation time.

**Severity**: **High** — All three cases are false passes; no configuration parameters are actually verified.

---

#### 2. C2642247 — Auto tensor_parallel_size Not Verified

**File**: `endpoint_ssh_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642247 | vLLM multi-GPU deploy without specifying tensor_parallel_size, system auto-sets to GPU count | Untested |

**TestRail Steps**:
1. Create inference instance on 4-GPU cluster without specifying tensor_parallel_size
2. Check Ray Application Config → **verify tensor_parallel_size=4**
3. Verify inference instance starts

**E2E Actual Code**:
1. Apply endpoint with gpu "2" (not 4 GPU)
2. Wait Running
3. **Only verifies phase="Running"** + inference HTTP 200

**Issue**: E2E only indirectly verifies through Running status — **does not check the actual tensor_parallel_size value**. If the system defaults to tp=1 but happens to reach Running anyway, this case cannot detect the bug.

**Severity**: **Medium** — Core logic point not directly asserted.

---

#### 3. C2642248 — User tensor_parallel_size Priority Not Verified

**File**: `endpoint_ssh_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642248 | vLLM multi-GPU deploy with manual tensor_parallel_size, user value takes priority | Passed |

**TestRail Steps**:
1. Create inference instance on multi-GPU cluster with engine_args tensor_parallel_size=2
2. **Check actual deployment config, confirm user-specified tp=2 is used and not overridden by auto value**

**E2E Actual Code**: apply → wait Running → only verifies phase="Running" + ServiceURL not empty

**Issue**: Same as C2642247 — **does not check the actual tensor_parallel_size value** in deployment config.

**Severity**: **Medium**

---

#### 4. C2644064 — env_vars Propagation Not Verified

**File**: `endpoint_ssh_config_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2644064 | SSH backend_container scenario: env_vars (HF_TOKEN etc.) correctly propagated to Backend container | Passed |

**TestRail Steps**:
1. Create inference instance with env_vars (HF_TOKEN)
2. Wait Running
3. **Check via Ray Dashboard Serve Tab: Backend Config → ray_actor_options.runtime_env.env_vars contains HF_TOKEN**
4. Verify inference

**E2E Actual Code**:
1. Apply endpoint with env var `E2E_TEST_KEY=e2e_test_value`
2. Wait Running
3. Get endpoint → verify phase Running
4. HTTP POST chat/completions → verify 200

**Issue**: **Does not check via Ray Dashboard API whether env_vars are actually propagated to the Backend container**. Running + successful inference alone cannot prove env_vars took effect (unless the model itself depends on that env var).

**Severity**: **Medium** — If env_vars propagation mechanism fails but doesn't affect inference, this case cannot detect it.

---

#### 5. C2644063 — Serving-Only Parameter Ignored Not Verified

**File**: `endpoint_ssh_config_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2644063 | SSH engine_args containing serving-only parameter (response_role) deploys successfully, parameter is ignored | Passed |

**TestRail Steps**:
1. Create inference instance with engine_args containing response_role (serving-only parameter)
2. Wait Running, **unknown parameter is ignored (warning in logs)**
3. Inference works normally

**E2E Actual Code**: apply → wait Running → verify phase Running

**Issue**: Does not check logs for warning, does not verify response_role is actually absent from engine startup arguments.

**Severity**: **Low** — Core behavior (successful deployment) is verified.

---

### Issue Type B: Intermediate State Not Captured

The following cases require verifying intermediate state transitions per TestRail, but E2E code skips the intermediate state.

#### 6. C2642277 — "Updating" Intermediate State Not Verified

**File**: `cluster_ssh_test.go:96-121`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642277 | Edit Running cluster spec → status immediately becomes Updating → Running | Passed |

**TestRail Steps**:
1. Running → edit spec
2. **Status immediately becomes Updating**
3. Returns to Running

**E2E Actual Code**:
1. Record ObservedSpecHash
2. Apply modified YAML
3. `WaitForSpecChange(oldHash, 60s)` — wait for hash change
4. `WaitForPhase("Running", "10m")`
5. Verify newHash != oldHash

**Issue**: Uses spec hash change to indirectly determine the controller accepted the change, but **never checks whether the phase went through "Updating"**. If the controller handles the change directly from Running→Running (hash changed but phase didn't), this case still passes.

**Severity**: **Medium** — TestRail title explicitly requires "Updating", but code doesn't verify it.

---

#### 7. C2642278 / C2612848 — "Deleting" Intermediate State Not Verified

**File**: `cluster_ssh_test.go:123-133`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642278 | Delete cluster → status immediately becomes Deleting → Deleted | Passed |
| C2612848 | Running cluster can be successfully deleted | Passed |

**E2E Actual Code**:
```go
r := ClusterH.DeleteGraceful(clusterName)
r = ClusterH.WaitForDelete(clusterName, "10m")
r = RunCLI("get", "cluster", "-w", ...)
Expect(r.Stdout).NotTo(ContainSubstring(clusterName))
```

**Issue**: Directly waits for deletion, **never captures the "Deleting" intermediate state**.

**Severity**: **Low** — Final result is correct (deletion complete), but if the Deleting state has a bug, it won't be detected.

---

#### 8. C2642231 / C2642232 — "Upgrading" Intermediate State Not Deterministically Captured

**File**: `cluster_upgrade_test.go:165-211, 297-327`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642231 | SSH cluster PATCH spec.version → Upgrading → Running | Passed |
| C2642232 | K8s cluster PATCH spec.version → Pod rolling update → Running | Passed |

**E2E Actual Code**: Uses 60s polling to attempt capturing "Upgrading", but allows going directly to Running+new version.

```go
// Polling logic: phase == "Upgrading" || (phase == "Running" && version != old)
```

**Issues**:
- If upgrade completes quickly (< 2s polling interval), "Upgrading" is never captured but the case still passes
- C2642232 TestRail requires "Pod rolling update", but E2E **does not check whether K8s Pods actually rolled**

**Severity**: **Medium** — K8s upgrade verification is weaker than TestRail requires.

---

### Issue Type C: E2E and TestRail Steps Inconsistent

#### 9. C2642183 — Resource Types Differ from TestRail Description

**File**: `cli_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642183 | CLI apply creates resources from multi-document YAML with dependency ordering | Passed |

**TestRail Steps**: Prepare multi-document YAML containing Workspace, Engine, Cluster, Endpoint → apply → verify dependency ordering

**E2E Actual Code**: Uses ImageRegistry + ModelRegistry (two lightweight resources), **does not involve Workspace/Engine/Cluster/Endpoint**

**Issue**: TestRail requires verifying **dependency ordering** of heavyweight resources (Workspace before Engine, Engine before Cluster), but the E2E uses two resources with simple dependency relationships — **does not verify the core multi-layer dependency ordering logic**.

**Severity**: **Medium** — Passes but with insufficient test strength.

---

#### 10. C2642189 — Uses Lightweight Resource Substitute

**File**: `cli_test.go`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642189 | CLI wait --for jsonpath=... exits 0 after resource reaches specified state | Passed |

**TestRail Steps**: Create Endpoint → wait jsonpath=.status.phase=Running

**E2E Actual Code**: Create ImageRegistry → wait jsonpath=.status.phase=Connected

**Issue**: Resource type and target state differ. ImageRegistry Connected is typically reached in seconds — **does not test long-wait scenarios**. However, the CLI wait mechanism is generic, so lightweight resources are sufficient to verify command functionality.

**Severity**: **Low**

---

#### 11. C2644058 — Different Verification Method

**File**: `external_endpoint_test.go:332-355`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2644058 | YAML Export - ExternalEndpoint resource export, credential masked | Untested |

**TestRail Steps**:
1. Create ExternalEndpoint
2. **YAML export dialog** selects ExternalEndpoint type
3. Export YAML file
4. Check credential masking

**E2E Actual Code**: `CLI get ExternalEndpoint -o yaml` → check no plaintext → check contains `***`

**Issue**: TestRail describes the **UI YAML export** feature, while E2E verifies **CLI get -o yaml**. Both may share the same underlying API, but the UI export path may have independent masking logic.

**Severity**: **Medium** — If UI export and CLI get use different code paths, coverage gaps may exist.

---

#### 12. C2642210 — Insufficient Verification Scope

**File**: `external_endpoint_test.go:299-330`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642210 | ExternalEndpoint API GET/LIST credential field is masked | Untested |

**TestRail Steps**:
1. Create ExternalEndpoint
2. **API GET** verify masking
3. **API LIST** verify masking

**E2E Actual Code**: Only `CLI get ExternalEndpoint <name> -o json` → verify masking

**Issue**: **Does not verify LIST operation masking**. LIST and GET may use different serialization paths.

**Severity**: **Medium**

---

#### 13. C2642233 — E2E Has Extra Steps but Missing Inference Verification

**File**: `cluster_upgrade_test.go:215-261`

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2642233 | After cluster upgrade, deployed endpoint can still serve inference normally | Passed |

**TestRail Steps**:
1. Deploy endpoint → verify inference OK
2. Upgrade cluster
3. **Test inference API again → works normally**

**E2E Actual Code**:
- C2642233 It block: upgrade → wait cluster Running → **wait endpoint Running** (no inference call)
- Inference verification is in the **next** It block ("should serve inference after upgrade", no Case ID label)

**Issue**: C2642233's own assertion stops at `waitEndpointRunning()` — **inference verification is in a separate It without a Case ID**. If C2642233 is run individually, inference verification won't execute.

**Severity**: **Medium** — Depends on Ordered execution order; not a self-contained test.

---

### Issue Type D: TestRail Failed but E2E Related

#### 14. C2612944 — TestRail Failed

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2612944 | Inference Instance - Status - Deploy failure shows Failed status | **Failed** |

**E2E Code**: apply endpoint with non-existent model → wait Failed → verify phase="Failed"

**Issue**: TestRail marks Failed indicating the case didn't pass last run. Need to confirm whether it's an environment issue or code bug.

---

#### 15. C2613227 — TestRail Failed

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2613227 | Engine - Create - Upload engine package via CLI creates new engine | **Failed** |

**E2E Code**: Start local registry → build engine package (with real Docker image) → import → verify

**Issue**: This case depends on local Docker daemon + registry container; heavy environment dependency makes it prone to environment-related failures.

---

#### 16. C2644068 — TestRail Failed

| Case | TestRail Title | TestRail Status |
|------|---------------|----------------|
| C2644068 | SSH cluster NFSv4 Docker mount works correctly on kernel 5.6+ | **Failed** |

**E2E Code**: apply endpoint → wait Running → inference verification

**Issue**: TestRail requires checking Docker container NFS mount parameters (type=nfs + nfsvers=X), but E2E **only verifies functional correctness** without checking mount parameter types. If the issue is in mount parameters, E2E may not detect it.

---

### Issue Type E: TestRail Untested but E2E Code Exists

The following 18 cases are marked Untested in TestRail (never executed), but E2E code already exists. Results need to be synced.

| Case | Title | Likely Reason |
|------|-------|--------------|
| C2612830 | Add Worker Node to cluster | E2E code exists, TestRail has no steps defined |
| C2613102 | Failed→Running recovery | E2E code exists, TestRail has no steps defined |
| C2613216 | CLI import adds new engine version | E2E code exists, results not synced |
| C2642176 | Anthropic non-stream | E2E code exists, results not synced |
| C2642177 | Anthropic models list | Same as above |
| C2642178 | Auth header forwarding | Same as above |
| C2642179 | 400 for unmapped model | Same as above |
| C2642180 | Anthropic stream | Same as above |
| C2642207 | Engine remove-version | E2E code exists, results not synced |
| C2642208 | Engine remove-version --force | Same as above |
| C2642209 | Engine remove-version in use | Same as above |
| C2642210 | Credential masking JSON | Same as above |
| C2642212 | CLI get ExternalEndpoint | Same as above |
| C2642243 | Deployment UNHEALTHY→Failed | Same as above |
| C2642247 | Auto tensor_parallel_size | Same as above |
| C2642283 | Accelerator non-string validation | Same as above |
| C2642284 | Accelerator valid string map | Same as above |
| C2644056 | Credentials API admin | Same as above |
| C2644058 | YAML export masking | Same as above |
| C2644371-74 | vLLM v0.17.1 series | New cases, E2E code exists but never run |

---

## 3. Issue Summary

| Severity | Count | Cases |
|----------|-------|-------|
| **High — False Pass** | 3 | C2613388, C2613397, C2613402 |
| **Medium — Insufficient Assertions** | 5 | C2642247, C2642248, C2644064, C2642210, C2644058 |
| **Medium — Intermediate State Not Captured** | 3 | C2642277, C2642231, C2642232 |
| **Medium — Steps Inconsistent** | 3 | C2642183, C2642233, C2644058 |
| **Low — Minor Differences** | 4 | C2642278, C2642189, C2644063, C2612656 |
| **Failed — Needs Investigation** | 3 | C2612944, C2613227, C2644068 |
| **Untested — Needs Sync** | 18 | See table above |

### Recommended Fix Priority

1. **Fix Immediately**: C2613388, C2613397, C2613402 — Three cases with hollow assertions; add actual value verification
2. **Short-term Enhancement**: C2642247, C2642248 — Add tensor_parallel_size actual value assertions
3. **Short-term Enhancement**: C2642210 — Add LIST operation masking verification
4. **Investigate Failures**: C2612944, C2613227, C2644068 — Confirm root cause of failures
5. **Sync Results**: Run the 18 Untested cases and sync results to TestRail

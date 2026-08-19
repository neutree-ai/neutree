# NVML 替换方案:Feature Discovery 对齐 + 去驱动挂载

## 背景与目标

当前 NodeAgent 通过 `NVMLGPUHardwareInfoProvider`（`hardware/nvml_provider_linux_cgo.go`）**直接用 cgo 调用 NVML 库**探测 GPU 静态硬件信息。这带来两个问题：

1. **NodeAgent 必须挂载驱动库**（`/run/nvidia` 等），与宿主机驱动 ABI 耦合。
2. **与 NVIDIA GPU Feature Discovery（GFD）重复探测**——GFD 已经用 NVML 探测了同样的信息并写成了 node label。

**目标**：移除 NodeAgent 的 NVML cgo 依赖，NodeAgent **不再挂载加速器相关驱动**。静态硬件信息改由 Feature Discovery 组件提供（K8s 读 node label / Docker 读文件），Neutree 只消费。

## 现状:NVML 提供的字段 vs 替代入口

### NVML 字段逐项核对(已确认)

| # | 字段 | NVML 来源 | 替代入口 | 替代可行? |
|---|---|---|---|---|
| 1 | UUID | `device.UUID()` | DCGM 指标 label(`UUID`) | ✅ 已在用 |
| 2 | Index | `device.Index()` | DCGM `DCGM_FI_DEV_NVML_INDEX` | ✅ 已在用 |
| 3 | MinorNumber | `device.MinorNumber()` | DCGM `DCGM_FI_DEV_GPU_MINOR_NUMBER`(需加进 counters)或 `/proc/driver/nvidia/gpus/<busid>/information` | ✅ 可行 |
| 4 | Product | `device.Product()` | GFD label `nvidia.com/gpu.product` 或 DCGM `modelName` | ✅ 可行 |
| 5 | Architecture | `device.Architecture()` | GFD `nvidia.com/gpu.family` | ✅ 可行(注意大小写映射) |
| 6 | CUDACapability | `device.CUDACapability()` | GFD `nvidia.com/gpu.compute.major/minor` 或 DCGM `DCGM_FI_DEV_CUDA_COMPUTE_CAPABILITY` | ✅ 可行 |
| 7 | DriverVersion | `client.DriverVersion()` | GFD `nvidia.com/cuda.driver-version.full` 或 DCGM `DCGM_FI_DRIVER_VERSION` | ✅ 可行 |
| 8 | CUDADriverVersion | `client.CUDADriverVersion()` | GFD 同上 或 DCGM `DCGM_FI_CUDA_DRIVER_VERSION` | ✅ 可行 |
| 9 | MemoryTotalMiB | `device.MemoryTotalBytes()` | DCGM `DCGM_FI_DEV_FB_TOTAL`(设备级)或 GFD `gpu.memory`(节点级) | ✅ 可行 |
| 10 | PCIEBusID | `device.PCIEBusID()` | DCGM `DCGM_FI_DEV_PCI_BUSID` | ✅ 已在用 |
| 11 | PCIEGeneration | `device.PCIEGeneration()` | DCGM `DCGM_FI_DEV_PCIE_MAX_LINK_GEN`/`LINK_GEN` | ✅ 已在用 |
| 12 | PCIEWidth | `device.PCIEWidth()` | DCGM `DCGM_FI_DEV_PCIE_MAX_LINK_WIDTH`/`LINK_WIDTH` | ✅ 已在用 |
| 13 | NUMANode | `device.NUMANode()` + sysfs | sysfs `numaNodeFromSysFS`(代码已有) | ✅ 可行 |

**注意**：NVLink/NVSwitch 本就不来自 NVML provider（接口无此方法），来自 DCGM 指标，移除无影响。

### 结论:13 个字段全部有替代入口

| 替代来源 | 字段 |
|---|---|
| DCGM scrape(已在用) | UUID, Index, PCIEBusID, PCIEGeneration, PCIEWidth, MemoryTotalMiB |
| DCGM scrape(需加 counters) | MinorNumber(`DCGM_FI_DEV_GPU_MINOR_NUMBER`) |
| GFD label(需新增读取) | Product, Architecture, CUDACapability, DriverVersion, CUDADriverVersion |
| sysfs(已有 fallback) | NUMANode |

## MinorNumber 的处理(关键决策)

**GFD 社区不定义 device minor number**（GFD 源码 `resource.Device` 接口 12 个方法无 minor；所有 "minor" label 都是版本号语义）。替代来源：

| 方案 | 复杂度 | 说明 |
|---|---|---|
| **A. DCGM 加 `DCGM_FI_DEV_GPU_MINOR_NUMBER`** | 最低 | counters 加一行 + `applyDCGMHardwareSample` 加一个 case；DCGM 直接给每 UUID 的 minor |
| **B. 读 `/proc/driver/nvidia/gpus/<busid>/information`** | 中 | busid → "Device Minor" 字段；UUID 经 `DCGM_FI_DEV_PCI_BUSID` 关联 |
| **C. sysfs class 设备** | 高 | `/sys/class/nvidia/nvidiaN/dev` 无法从 busid 正向定位 N | ❌ 不推荐 |

**推荐方案 A**：DCGM 直接提供，零关联逻辑。**这使 MinorNumber 在移除 NVML 后仍可完整保留**，无需接受 nil。

## 方案:按环境分层的静态信息获取

### Kubernetes 集群

NodeAgent 读 node label（GFD 已探测好的）：

```
nvidia.com/gpu.product=Tesla-T4        → Product
nvidia.com/gpu.family=turing           → Architecture(注意小写→"Turing"映射)
nvidia.com/gpu.compute.major/minor=7/5 → CUDACapability
nvidia.com/cuda.driver-version.full    → DriverVersion
nvidia.com/gpu.memory=15360            → MemoryTotalMiB(节点级 fallback)
```

新增一个 `nvidiaFeatureDiscoveryProvider`，从 node metadata.labels 提取静态字段，作为 `GPUHardwareInfoProvider` 的 primary；DCGM scrape 作为设备级字段（UUID/Index/PCIe/Minor）的权威源。`Merge` 合并。

### 静态 Ray/SSH（Docker）

**NFD worker 的 standalone 模式已 deprecated**（只探测不发布 label，依赖弃用 gRPC API）。正确做法是**直接部署 GFD 容器**：

```bash
docker run --rm \
  -v /sys:/sys \
  -v /run/nvidia:/run/nvidia \
  -v /path/output:/etc/kubernetes/node-feature-discovery/features.d \
  ${GFD_IMAGE} \
  gpu-feature-discovery --output=/etc/kubernetes/node-feature-discovery/features.d/gfd
```

GFD 的 `--output` 写文件模式（对应配置 `outputFile` + `sleepInterval: 60s` + `oneshot: false`）天然支持 Docker。NodeAgent 读该文件（yaml，含 `labels:` 段），与 K8s 读 node label 对齐。

### 架构变化

```
现状:  NodeAgent(cgo NVML) ──► 驱动挂载 + 直接探测
目标:  GFD (K8s: node label / Docker: 文件)
          │
          ▼
       NodeAgent 读静态信息(无驱动挂载) + DCGM scrape(设备级/动态)
```

## 实施步骤

1. **新增 `nvidiaFeatureDiscoveryProvider`**：读 node label（K8s）或 GFD 文件（静态 Docker），提取 product/architecture/cuda/driver/memory。
2. **DCGM counters 加 `DCGM_FI_DEV_GPU_MINOR_NUMBER`**：`applyDCGMHardwareSample` 加 case。
3. **移除 `NVMLGPUHardwareInfoProvider`**（`nvml_provider_linux_cgo.go` + stub + 接口）。
4. **`applyHardwareLabelHints` 扩展**：支持读 `nvidia.com/*` label（当前只读 DCGM 指标 label）。
5. **NodeAgent 装配**：`GPUHardwareProvider` 改为 feature-discovery provider；去掉驱动挂载。
6. **测试**：K8s 与静态 Docker 两路径的硬件 info 断言；`normalizer_test.go` 迁移。
7. **验证**：对照移除前后 `neutree_node_accelerator_hardware_info`/`nvidia_info` 输出一致。

## Ascend NPU 可行性分析(已确认:本地源码分析)

### openFuyao npu-feature-discovery 实际机制(源码确认)

本地源码 `/Users/huangwei/go/src/npu-feature-discovery` 分析：

- 定位："generate labels for NPU devices"，**NPU Operator 的 K8s 前置依赖**。
- **不是硬件探测组件，是元数据加工**：信息来自 ① node 已有 label（NFD PCI label）② PCI sysfs（`/sys/bus/pci/devices/*/class`，`GetCardInfo`）③ exec `npu-smi`（`getBoardID`/`getNPUInfo`）。
- **不挂载驱动**：daemonset 只挂 `/etc/localtime`；npu-smi 走 exec 命令（宿主机 `/usr/local/sbin/`），不挂库。
- 产出 label：`openfuyao.com/npu.present`、`accelerator`（设备类型）、`accelerator-type`（module/half）、`host-arch`、`server-usage`、`container.runtime`、RDMA/网络 label。
- **不产驱动版本/算力/设备级 identity**——无 `cuda.driver-version` 等价物、无设备粒度。
- **不支持 standalone**：`Output()` 硬编码 `client.CoreV1().Nodes().Update()` 写回 K8s，无 GFD 的 `--output` 文件模式。

### npu exporter 已提供的静态信息(实测确认,更全)

| exporter 指标 | 值(实测) | 信息类型 | FD 等价物 |
|---|---|---|---|
| `npu_chip_info_name{name=...}` | 310P3-Ascend-V1 | **精确型号** | 比 FD `huawei-Ascend310P` 更精确 |
| `npu_chip_info_product_type` | Atlas 300I Duo | 卡型 | ≈ FD `server-usage` |
| `machine_npu_nums` / `machine_card_nums` | 4 / 2 | 设备数/卡数 | ≈ FD `npu-smi info -m` |
| `node_base_info{driverVersion=...}` | 25.5.2 | **驱动版本** | **FD 没有** |
| `npu_chip_info_serial_number` | 210603... | 序列号 | FD 没有 |
| `vdie_id` / `id` / `pcie_bus_info` | — | **设备级 identity + PCIe** | **FD 没有**（无设备粒度） |

### 最终方案:NodeAgent 完全不挂驱动

**参考 npu-feature-discovery 的采集方式（node label + PCI sysfs），NFD 依赖自采，已有内容直接用 exporter**：

```
┌─ NodeAgent 静态信息（不挂驱动）─────────────────────────┐
│  ① 设备级: exporter 指标(model_name/vdie_id/          │
│     product_type/machine_npu_nums/pcie_bus_info/       │
│     node_base_info)           ← 已有,直接消费          │
│  ② 设备类型: 自采 PCI sysfs(class 0x1200 + vendor     │
│     19e5 + device d500/d80x) ← 复用 FD 的 GetCardInfo  │
│     方式,不依赖 NFD                                    │
│  ③ NUMA: sysfs(/sys/bus/pci/devices/<id>/numa_node)   │
│     ← 可选                                              │
└───────────────────────────────────────────────────────┘
      NodeAgent 无驱动挂载 ✅
      (驱动访问全部在 npu-exporter,它是采集器必须挂)
```

**原则落实**：
1. **NodeAgent 不挂驱动**——静态信息从 exporter + PCI sysfs 拿，DCMI/驱动访问全部在 npu-exporter。
2. **NFD 依赖自采**——复用 FD 的 `GetCardInfo` PCI sysfs 逻辑（读 class/vendor/device 匹配），不依赖 NFD label。
3. **npu exporter 已有内容直接用**——精确型号/卡型/设备数/驱动版本/设备级 identity 全有，甚至比 FD 更全。
4. **FD 的 npu-smi exec 不需要**——boardID/accelerator-type 细分 Neutree 调度不消费。
5. **npu-feature-discovery 的 K8s 价值保留**：给 planner 打存在性/类型 label（若集群装了它）；静态集群不用它（无 K8s）。

## 结论

- **NVIDIA**：移除 NVML cgo 完全可行，13 个字段全部有替代（DCGM + GFD label + sysfs），NodeAgent 不再挂载驱动。
- **Ascend**：NodeAgent **不挂驱动可行**——npu exporter 已覆盖静态信息（且比 npu-feature-discovery 更全），NFD 依赖用 PCI sysfs 自采，与 NVIDIA"去驱动挂载"对称。

## 参考

- [NVIDIA GFD 源码](https://github.com/NVIDIA/k8s-device-plugin/blob/main/internal/lm/nvml.go)
- [GFD resource.Device 接口](https://github.com/NVIDIA/k8s-device-plugin/blob/main/internal/resource/types.go)
- [openFuyao npu-feature-discovery](https://gitcode.com/openFuyao/npu-feature-discovery)
- [openFuyao NPU Operator 文档](https://docs.openfuyao.cn/en/docs/v26.03/user_guide/npu_dra_plugin.html)
- [RBLN npu-feature-discovery](https://github.com/RBLN-SW/rbln-npu-feature-discovery)
- [DCGM Field IDs](https://docs.nvidia.com/datacenter/dcgm/latest/dcgm-api/dcgm-api-field-ids.html)
- [nvidia open-gpu-kernel-modules: Device Minor](https://github.com/NVIDIA/open-gpu-kernel-modules/discussions/336)

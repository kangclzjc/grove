# Volcano Scheduler Backend

## 概述

本文档描述了在 Grove Operator 中添加 [Volcano](https://volcano.sh) 调度器后端支持的实现。
Volcano 是 CNCF 开源的批处理调度器，专为 AI/ML 工作负载设计，提供 Gang Scheduling 和网络拓扑感知调度能力。

Grove 已有 Scheduler Backend Framework（见 [GREP-375](../375-scheduler-backend-framework/README.md)），
本实现遵循该框架，在不修改任何现有 controller 逻辑的前提下，新增 Volcano 作为第三个可选调度器后端。

---

## 背景：Grove Scheduler Backend 框架

在理解本实现之前，需要先了解 Grove 的调度器后端框架。

### SchedBackend 接口

所有调度器后端必须实现以下接口（`operator/internal/schedulerbackend/types.go`）：

```go
type SchedBackend interface {
    Name() string
    Init() error
    SyncPodGang(ctx context.Context, podGang *PodGang) error
    OnPodGangDelete(ctx context.Context, podGang *PodGang) error
    PreparePod(pod *corev1.Pod)
    ValidatePodCliqueSet(ctx context.Context, pcs *PodCliqueSet) error
}
```

| 方法 | 触发时机 | 作用 |
|---|---|---|
| `Name()` | 任意时刻 | 返回后端唯一标识符，也是 `Pod.Spec.SchedulerName` 的值 |
| `Init()` | Operator 启动时 | 一次性初始化，创建集群级资源 |
| `SyncPodGang()` | PodGang 创建/更新时 | 将 PodGang 同步为调度器特有的 CR |
| `OnPodGangDelete()` | PodGang 删除时 | 清理调度器特有的 CR |
| `PreparePod()` | Pod 创建前 | 注入调度器所需的 schedulerName、annotation 等 |
| `ValidatePodCliqueSet()` | PodCliqueSet 准入时 | 后端特有的合法性校验 |

### 已有后端

| 后端 | SchedulerName | 说明 |
|---|---|---|
| `kube` | `kube-scheduler` | Kubernetes 默认调度器，Pod.Spec.SchedulerName = "default-scheduler" |
| `kaischeduler` | `kai-scheduler` | NVIDIA KAI 调度器 |
| `volcano`（本实现）| `volcano` | Volcano 批处理调度器 |

---

## Volcano 基本概念

### PodGroup（`scheduling.volcano.sh/v1beta1`）

Volcano 用 `PodGroup` CR 实现 Gang Scheduling，它是 Volcano 识别一批 Pod 属于同一调度组的核心资源。

```yaml
apiVersion: scheduling.volcano.sh/v1beta1
kind: PodGroup
metadata:
  name: my-training-job
  namespace: default
spec:
  minMember: 8          # 至少需要 8 个 Pod 同时调度成功
  queue: default         # 使用的 Volcano Queue
  priorityClassName: high-priority
  networkTopology:       # 可选：网络拓扑约束（需要 HyperNode 支持）
    mode: hard           # hard=强制约束, soft=尽力而为
    highestTierAllowed: 2  # 允许跨越的最高 HyperNode tier
```

### HyperNode（`topology.volcano.sh/v1alpha1`）

`HyperNode` 是 Volcano v1.12+ 引入的集群级 CR，代表一组具有相同网络拓扑特征的节点（如同一机架、同一区域）。Tier 数字越小表示拓扑域越紧（越靠近硬件）。

```
Tier 1 = Host（最紧，同一主机）
Tier 2 = Rack（同一机架）
Tier 3 = Zone（同一可用区）
```

**注意**：HyperNode 由集群管理员创建和维护，Grove 不负责管理。

### Pod Annotation

Volcano 通过 Pod 上的 annotation 识别该 Pod 属于哪个 PodGroup：

```
scheduling.volcano.sh/group-name: <podgroup-name>
```

---

## 实现方案

### 映射关系：Grove PodGang → Volcano PodGroup

| Grove PodGang 字段 | Volcano PodGroup 字段 | 说明 |
|---|---|---|
| `sum(PodGroup[i].MinReplicas)` | `spec.minMember` | 将所有子组的最小副本数加总 |
| `spec.priorityClassName` | `spec.priorityClassName` | 直接映射 |
| `VolcanoConfig.Queue`（默认 "default"）| `spec.queue` | 从后端配置读取 |
| `TopologyConstraint.PackConstraint.Required` | `spec.networkTopology.mode = "hard"` + `highestTierAllowed = tier` | 硬约束：Required 的 topology key 对应的 HyperNode tier |
| `TopologyConstraint.PackConstraint.Preferred`（仅此字段）| `spec.networkTopology.mode = "soft"` + `highestTierAllowed = tier` | 软约束：Preferred 的 topology key 对应的 HyperNode tier |
| 无 topology 约束 | `spec.networkTopology` 不设置 | 普通 gang 调度 |

#### Topology Key → Tier 映射

Grove 中 PodGang 的 `TopologyConstraint` 存储的是节点标签键（如 `topology.kubernetes.io/rack`），而 Volcano 的 `NetworkTopology` 需要一个整数 tier 编号。

这个映射通过 `VolcanoSchedulerConfiguration.TopologyKeyToTier` 显式配置，避免依赖隐含约定。

示例配置：
```yaml
scheduler:
  profiles:
  - name: volcano
    config:
      queue: gpu-jobs
      topologyKeyToTier:
        "kubernetes.io/hostname": 1          # Host = tier 1（最紧）
        "topology.kubernetes.io/rack": 2     # Rack = tier 2
        "topology.kubernetes.io/zone": 3     # Zone = tier 3
```

管理员创建 HyperNode 时的 tier 编号须与此配置一致。

---

## 代码改动详解

### 1. `operator/go.mod`

**变更**：添加 `volcano.sh/apis v1.13.0` 作为直接依赖。

```
volcano.sh/apis v1.13.0
```

使用的 Volcano API 包：
- `volcano.sh/apis/pkg/apis/scheduling/v1beta1` — PodGroup、NetworkTopologySpec 等类型

不使用的包：
- `topology/v1alpha1`（HyperNode）— Grove 不创建/管理 HyperNode，无需引入

---

### 2. `operator/internal/client/scheme.go`

**变更**：在全局 `Scheme` 中注册 `volcanoschedulingv1beta1`，使得：
- `controllerutil.CreateOrPatch` 能为 Volcano PodGroup 构造正确的 GVK
- 测试使用的 fake client 能存取 Volcano PodGroup 对象

```go
// 新增
volcanoschedulingv1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"

// 在 localSchemeBuilder 中新增
volcanoschedulingv1beta1.AddToScheme,
```

---

### 3. `operator/api/config/v1alpha1/types.go`

**变更一**：新增调度器名称常量并更新支持列表。

```go
// 新增常量
SchedulerNameVolcano SchedulerName = "volcano"

// SupportedSchedulerNames 加入 volcano
SupportedSchedulerNames = []SchedulerName{SchedulerNameKai, SchedulerNameKube, SchedulerNameVolcano}
```

**变更二**：`SchedulerProfile.Name` 的 kubebuilder 枚举注解加入 `volcano`。

```go
// +kubebuilder:validation:Enum=kai-scheduler;kube-scheduler;volcano
```

**变更三**：新增 `VolcanoSchedulerConfiguration` 配置类型。

```go
type VolcanoSchedulerConfiguration struct {
    // Queue 是提交 PodGroup 的 Volcano Queue 名称，默认 "default"。
    Queue string `json:"queue,omitempty"`

    // TopologyKeyToTier 将节点标签键映射到 Volcano HyperNode tier 编号。
    // 启用拓扑感知调度时必须配置，tier 编号须与集群管理员创建的 HyperNode 一致。
    // 示例：{"kubernetes.io/hostname": 1, "topology.kubernetes.io/rack": 2}
    TopologyKeyToTier map[string]int `json:"topologyKeyToTier,omitempty"`
}
```

---

### 4. `operator/api/config/v1alpha1/zz_generated.deepcopy.go`

**变更**：为 `VolcanoSchedulerConfiguration` 添加 deepcopy 方法。

`TopologyKeyToTier` 是 `map[string]int`，不能直接值拷贝，需要手动遍历复制：

```go
func (in *VolcanoSchedulerConfiguration) DeepCopyInto(out *VolcanoSchedulerConfiguration) {
    *out = *in
    if in.TopologyKeyToTier != nil {
        in, out := &in.TopologyKeyToTier, &out.TopologyKeyToTier
        *out = make(map[string]int, len(*in))
        for key, val := range *in {
            (*out)[key] = val
        }
    }
}
```

---

### 5. `operator/internal/schedulerbackend/volcano/backend.go`（新文件）

核心实现文件。

#### Backend 结构体

```go
type Backend struct {
    client        client.Client
    scheme        *runtime.Scheme
    eventRecorder record.EventRecorder
    profile       configv1alpha1.SchedulerProfile
    queue         string          // Volcano queue，从配置解析，默认 "default"
    keyToTier     map[string]int  // topology key → HyperNode tier 映射
}
```

#### `New()` — 构造函数

从 `profile.Config`（`runtime.RawExtension`）中反序列化 `VolcanoSchedulerConfiguration`，
提取 `Queue` 和 `TopologyKeyToTier`。若 Config 为空或解析失败，使用默认值（queue = "default"，无 topology 映射）。

#### `SyncPodGang()` — 核心同步逻辑

1. 构造一个与 PodGang 同名同 namespace 的 `PodGroup` skeleton
2. 调用 `controllerutil.CreateOrPatch` 幂等创建或更新
3. 在 mutate 函数中：
   - 设置 OwnerReference（`SetControllerReference`），使 PodGroup 随 PodGang 被 GC 清理
   - 累加所有 `PodGroup.MinReplicas` 得到 `spec.minMember`
   - 设置 `spec.queue` 和 `spec.priorityClassName`
   - 调用 `buildNetworkTopology()` 填充 `spec.networkTopology`（可能为 nil）

#### `OnPodGangDelete()` — 删除清理

显式删除 Volcano PodGroup，用 `client.IgnoreNotFound` 保证幂等性。
（Owner Reference 机制也会触发 GC，显式删除让清理更及时）

#### `PreparePod()` — Pod 准备

```go
pod.Spec.SchedulerName = "volcano"
pod.Annotations["scheduling.volcano.sh/group-name"] = podGangName
```

PodGang 名称从 Pod 的 `grove.io/podgang` label 读取。若 label 不存在则跳过 annotation（graceful 降级）。

#### `buildNetworkTopology()` — Topology 映射

```
PodGang.Spec.TopologyConstraint.PackConstraint
  ├── Required = "topology.kubernetes.io/rack"
  │     → keyToTier["topology.kubernetes.io/rack"] = 2
  │     → NetworkTopologySpec{Mode: "hard", HighestTierAllowed: &2}
  │
  └── Preferred = "kubernetes.io/hostname"（仅设 Preferred）
        → keyToTier["kubernetes.io/hostname"] = 1
        → NetworkTopologySpec{Mode: "soft", HighestTierAllowed: &1}
```

若 `keyToTier` 为空，或 key 在 map 中找不到，返回 `nil`（不设置 NetworkTopology，普通 gang 调度）。

---

### 6. `operator/internal/schedulerbackend/manager.go`

**变更一**：添加编译期检查。

```go
_ SchedBackend = (*volcano.Backend)(nil)
```

**变更二**：在 `newBackendForProfile()` switch 中添加 volcano case。

```go
case configv1alpha1.SchedulerNameVolcano:
    b := volcano.New(cl, scheme, rec, p)
    if err := b.Init(); err != nil {
        return nil, err
    }
    return b, nil
```

---

### 7. `operator/internal/schedulerbackend/manager_test.go`

**变更**：原来测试 "volcano" 会返回 "not supported" 错误，现在改为期望成功：

```go
// Before
{schedulerName: "volcano", wantErr: true, errContains: "not supported"}

// After
{schedulerName: configv1alpha1.SchedulerNameVolcano, wantErr: false, expectedName: "volcano"}
```

---

### 8. `operator/api/config/validation/validation_test.go`

**变更**：
- 将原来以 "volcano" 为 unsupported profile 的测试改为使用 "unknown-scheduler"（保留测试用例语义）
- 新增 "valid: volcano profile" 测试用例，验证 volcano 现在是合法的 profile name

---

### 9. `operator/internal/schedulerbackend/volcano/backend_test.go`（新文件）

共 10 个测试用例，覆盖所有主要场景：

| 测试名 | 验证内容 |
|---|---|
| `TestBackend_Name` | Name() 返回 "volcano" |
| `TestBackend_PreparePod` | SchedulerName 和 group-name annotation 正确设置 |
| `TestBackend_PreparePod_NoLabel` | 无 podgang label 时不 panic，annotation 不设置 |
| `TestBackend_SyncPodGang_Create` | minMember=5（3+2），默认 queue，priority，owner ref |
| `TestBackend_SyncPodGang_Update` | 幂等更新：已有 minMember=1 → 更新为 4 |
| `TestBackend_SyncPodGang_CustomQueue` | 从 profile.Config 解析自定义 queue |
| `TestBackend_SyncPodGang_WithTopology_Required` | Required key → hard mode + tier=2 |
| `TestBackend_SyncPodGang_WithTopology_PreferredOnly` | Preferred key → soft mode + tier=1 |
| `TestBackend_SyncPodGang_NoTopologyMapping` | 无 keyToTier 配置 → NetworkTopology=nil |
| `TestBackend_OnPodGangDelete` | PodGroup 被成功删除 |
| `TestBackend_OnPodGangDelete_AlreadyGone` | PodGroup 不存在时也不报错（幂等） |

---

## 使用方式

### OperatorConfiguration 配置示例

**最简配置（仅 gang scheduling，无 topology）：**

```yaml
scheduler:
  profiles:
  - name: volcano
  defaultProfileName: volcano
```

**完整配置（含 topology 感知调度）：**

```yaml
scheduler:
  profiles:
  - name: volcano
    config:
      queue: gpu-training
      topologyKeyToTier:
        "kubernetes.io/hostname": 1
        "topology.kubernetes.io/rack": 2
        "topology.kubernetes.io/zone": 3
  defaultProfileName: volcano
```

### 前提条件

1. 集群中已安装 Volcano（`volcano.sh` CRD 已注册）
2. 目标 Queue 已创建且处于 Open 状态
3. 若启用 topology 感知调度，管理员需预先创建 HyperNode CR，tier 编号与 `topologyKeyToTier` 配置一致

### 调度流程

```
用户创建 PodCliqueSet
    │
    ▼
Operator 创建 PodGang（含 MinReplicas、TopologyConstraint）
    │
    ▼
Volcano Backend: SyncPodGang()
    ├── 创建 Volcano PodGroup（minMember、queue、networkTopology）
    └── PodGroup ownerRef → PodGang（自动 GC）
    │
    ▼
Operator 创建 Pods
    └── Volcano Backend: PreparePod()
        ├── Pod.Spec.SchedulerName = "volcano"
        └── Pod.Annotations["scheduling.volcano.sh/group-name"] = <podgang-name>
    │
    ▼
Volcano Scheduler 读取 PodGroup 和 Pods，执行 gang scheduling
```

---

## 设计决策

### 为什么用显式 TopologyKeyToTier 而不是自动推导？

**候选方案 A**：从 `ClusterTopology.spec.levels` 的顺序自动推导 tier（反转排序，最窄 = tier 1）。

**候选方案 B**：在 `VolcanoSchedulerConfiguration` 中显式配置 key → tier 映射。

选择方案 B 的原因：
- `ClusterTopology` 可能不存在（TAS 未启用时）
- 自动推导依赖 ClusterTopology 的 level 顺序与 HyperNode tier 编号之间的隐含约定，一旦不一致会导致难以排查的问题
- 显式配置即是文档，管理员一眼就能看到 key 到 tier 的对应关系

### 为什么不管理 HyperNode？

Volcano 的 `HyperNode` 代表集群实际的硬件拓扑（每个 rack 一个 HyperNode），需要了解集群物理结构才能正确创建。这不是 Grove Operator 的职责范围，由集群管理员负责维护更合适。

### 为什么 Required 和 Preferred 同时设置时只用 Required？

当两者都设置时：
- `Required` 是硬约束（必须满足），映射为 `mode=hard`
- `Preferred` 是 Required 范围内的软偏好

Volcano 的 `NetworkTopologySpec` 只支持单一约束（一个 mode + 一个 tier），无法同时表达两层约束。选择 Required 是因为它是不可违背的硬约束，比 Preferred 的语义更强。

# Blockchain Node Operator 🚀

> A Production-Grade Kubernetes Operator for orchestrating Blockchain Infrastructure.
> 
> 
> Automates the lifecycle, security, and observability of Ethereum Full Nodes (Execution + Consensus layers).
> 

## 🌟 The Problem

Running blockchain nodes in production is hard. You have to manage:

- **Complex Architecture:** Coordinating Execution Clients (Geth) and Consensus Clients (Prysm).
- **Security:** Handling sensitive JWT tokens for Engine API authentication.
- **State Persistence:** Managing Terabytes of data with correct PVC bindings.
- **Network Identity:** Ensuring stable P2P identities and discovery.
- **Day-2 Operations:** Updates, drifts, and self-healing.

## 💡 The Solution

The **Blockchain Operator** codifies SRE operational knowledge into software. It treats an Ethereum Node as a first-class citizen in Kubernetes.

### Key Features

- ✅ **GitOps Ready:** Declarative `EthereumNode` CRD.
- ✅ **The "Merge" Architecture:** Automatically deploys Geth + Prysm as sidecars with shared auth.
- ✅ **Self-Healing:** Detects configuration drift (e.g., deleted services, changed ports) and fixes it automatically.
- ✅ **Zero-Touch Security:** Auto-generates and manages secure JWT tokens for Engine API.
- ✅ **Observability:** Built-in Prometheus metrics for Block Height and Sync Status (`ethereum_node_block_height`).
- ✅ **Instant Sync:** Support for Checkpoint Sync to start validating in seconds, not days.

## 🏗 Architecture

The operator implements the **Reconcile Loop** pattern to enforce the desired state.

```
graph TD
    User[User / GitOps] -->|Apply YAML| API[K8s API Server]
    API -->|Event| Operator[Blockchain Operator]

    subgraph "Managed Infrastructure"
        Operator -->|Reconciles| STS[StatefulSet]
        Operator -->|Reconciles| SVC_H[Headless Service]
        Operator -->|Reconciles| SVC_LB[P2P LoadBalancer]
        Operator -->|Reconciles| Secret[JWT Secret]

        STS -->|Mounts| PVC[Persistent Volume]
        STS -->|Runs| Pod[Node Pod]

        subgraph "Pod (Sidecar Pattern)"
            Geth[Execution Client] <-->|Engine API (Auth)| Prysm[Consensus Client]
        end
    end

```

## 🚀 Getting Started

### Prerequisites

- Kubernetes Cluster (v1.24+)
- Helm v3+
- `kubectl`

### Installation (Via Helm)

We provide a production-ready Helm chart for easy installation.

```
# 1. Create the namespace
kubectl create namespace blockchain-operator-system

# 2. Install the Operator
helm install blockchain-operator ./deploy/charts/blockchain-operator \
  -n blockchain-operator-system

```

### Usage: Deploying your First Node

Once the operator is running, deploying a full node is as simple as applying this manifest:

```
apiVersion: infra.blockchain.corp/v1
kind: EthereumNode
metadata:
  name: ethereumnode-sample
  namespace: blockchain-operator-system
spec:
  # Network Configuration
  network: "sepolia"
  syncMode: "snap"
  replicas: 1

  # Storage (NVMe/SSD recommended)
  storageSize: "500Gi"

  # Performance Tuning
  resources:
    requests:
      cpu: "2000m"
      memory: "4Gi"
    limits:
      cpu: "4000m"
      memory: "8Gi"

  # Instant Sync (Checkpoint)
  checkpointSyncURL: "[https://sepolia.beaconstate.info](https://sepolia.beaconstate.info)"

```

Apply it:

```
kubectl apply -f config/samples/infra_v1_ethereumnode.yaml

```

## 🛠 Technical Deep Dive (For Engineers)

### Drift Detection Strategy

Unlike simple controllers that just create resources, this operator implements Semantic Drift Detection.

It calculates a SHA256 hash of the desired Spec and compares it with the live state. It creates patches only when strictly necessary, preventing infinite update loops and respecting Kubernetes defaults.

### Security Model

- **Cluster Scoped:** Can manage nodes across multiple namespaces.
- **RBAC:** Strictly scoped permissions (Least Privilege).
- **Secret Management:** If no `jwtSecretName` is provided, the operator uses `crypto/rand` to generate a cryptographically secure token and injects it into the Pods via a Volume Mount.

### Observability & SLOs

The operator exposes custom metrics on port `:8080` for Prometheus scraping.

| **Metric Name** | **Type** | **Description** |
| --- | --- | --- |
| `ethereum_node_reconcile_errors_total` | Counter | Failures in the reconciliation loop. |
| `ethereum_node_block_height` | Gauge | Real-time block height fetched via JSON-RPC. |

**Sample Alert (PromQL):**

```
# Alert if node is stuck (Block height not increasing)
rate(ethereum_node_block_height[5m]) == 0

```

## 🤝 Contributing

This project is built with [Kubebuilder](https://book.kubebuilder.io/).

1. **Clone:** `git clone https://github.com/mvleandro/blockchain-operator.git`
2. **Test:** `make test`
3. **Run Local:** `make run`
4. **Build Image:** `make docker-build`

## 📜 License

Apache 2.0 - See [LICENSE](./LICENSE) for details.

*Built with ❤️ by a [@mvleandro](https://github.com/mvleandro).*
# TaskFly Architecture

## Overview

TaskFly is a distributed task orchestration system consisting of a daemon (server) and CLI (client) that work together to deploy and manage applications across cloud infrastructure. The system provisions cloud instances, distributes application bundles, and manages the lifecycle of distributed workloads.

## System Components

```mermaid
graph TB
    subgraph "Client Side"
        CLI[TaskFly CLI]
        Config[taskfly.yml]
        AppFiles[Application Files]
    end

    subgraph "Server Side - TaskFly Daemon"
        API[REST API Server]
        Orchestrator[Orchestrator Engine]
        StateStore[In-Memory State Store]
        CloudProviders[Cloud Providers]
    end

    subgraph "Cloud Infrastructure"
        AWS[AWS Provider]
        Local[Local Provider]
        Instances[Cloud Instances/Nodes]
    end

    CLI -->|Bundle & Deploy| API
    Config -->|Configuration| CLI
    AppFiles -->|Bundle| CLI
    API -->|Process Deployment| Orchestrator
    Orchestrator -->|Update State| StateStore
    Orchestrator -->|Provision Instances| CloudProviders
    CloudProviders --> AWS
    CloudProviders --> Local
    AWS -->|Launch| Instances
    Local -->|Launch| Instances
    Instances -->|Register & Heartbeat| API
    API -->|Status Updates| StateStore
```

---

## State Management Architecture

```mermaid
graph LR
    subgraph "State Store (In-Memory)"
        Deps[Deployments Map]
        Nodes[Nodes Map]
        NodesByDep[Nodes by Deployment Index]
    end

    subgraph "Deployment State Machine"
        Pending[Pending]
        Provisioning[Provisioning]
        Running[Running]
        Completed[Completed]
        Failed[Failed]
        Terminating[Terminating]
        Terminated[Terminated]

        Pending --> Provisioning
        Provisioning --> Running
        Running --> Completed
        Running --> Failed
        Provisioning --> Failed
        Running --> Terminating
        Terminating --> Terminated
    end

    subgraph "Node State Machine"
        NPending[Pending]
        NProvisioning[Provisioning]
        NBooting[Booting]
        NRegistering[Registering]
        NDownloading[Downloading Assets]
        NRunning[Running Script]
        NCompleted[Completed]
        NFailed[Failed]
        NTerminating[Terminating]
        NTerminated[Terminated]

        NPending --> NProvisioning
        NProvisioning --> NBooting
        NBooting --> NRegistering
        NRegistering --> NDownloading
        NDownloading --> NRunning
        NRunning --> NCompleted
        NRunning --> NFailed
        NProvisioning --> NFailed
        NBooting --> NFailed
        NRunning --> NTerminating
        NTerminating --> NTerminated
    end

    Deps -.->|1:N| NodesByDep
    NodesByDep -.->|index| Nodes
```

---

## Cloud Provider Architecture

```mermaid
graph TB
    subgraph "Provider Interface"
        PI[Provider Interface]
    end

    subgraph "Implementations"
        AWS[AWS Provider]
        Local[Local Provider]
        Future[Future Providers]
    end

    subgraph "Resource Pooling Layer"
        Pool[Resource Pool]
        PooledProv[Pooled Provider Adapter]
    end

    subgraph "Operations"
        Provision[Provision Instance]
        Status[Get Status]
        Terminate[Terminate Instance]
    end

    PI --> AWS
    PI --> Local
    PI --> Future

    AWS --> PooledProv
    PooledProv --> Pool

    Pool --> Acquire[Acquire from Pool]
    Pool --> Release[Release to Pool]
    Pool --> CleanupIdle[Cleanup Idle]

    Provision --> PI
    Status --> PI
    Terminate --> PI
```

### Resource Pool Benefits
- **Cost Optimization**: Reuse instances instead of constant create/destroy cycles
- **Faster Provisioning**: Pre-warmed instances reduce startup time
- **Idle Management**: Automatic cleanup of unused instances after timeout
- **Provision-Ahead**: Proactively create instances to reduce wait times

---

## API Endpoints

### Deployment Endpoints
```
POST   /api/v1/deployments          Create new deployment
GET    /api/v1/deployments          List all deployments
GET    /api/v1/deployments/:id      Get deployment status
DELETE /api/v1/deployments/:id      Terminate deployment
POST   /api/v1/deployments/:id/cleanup   Cleanup deployment files
POST   /api/v1/cleanup/all          Cleanup all completed deployments
```

### Node Endpoints
```
POST   /api/v1/nodes/register       Register node with provision token
GET    /api/v1/nodes/assets         Download application bundle
POST   /api/v1/nodes/heartbeat      Send heartbeat
POST   /api/v1/nodes/status         Update node status
```

### Health & Stats
```
GET    /api/v1/health               Health check
GET    /api/v1/stats                Get daemon statistics
```

---

### Why Two Bundles?

1. **Client Bundle** (`bundle.tar.gz`): Contains `taskfly.yml` + application files
   - Sent from CLI to daemon
   - Used for orchestration configuration

2. **Worker Bundle** (`worker_bundle.tar.gz`): Contains only application files
   - Created by daemon after parsing config
   - Distributed to nodes (nodes don't need taskfly.yml)
   - Keeps node bootstrap minimal

---

## Metadata Distribution

```mermaid
graph TB
    subgraph "Configuration in taskfly.yml"
        GM[Global Metadata]
        DL[Distributed Lists]
        CT[Config Template]
    end

    subgraph "Node Configuration Generation"
        MetaGen[Metadata Generator]
    end

    subgraph "Per-Node Configuration"
        N1Config[Node 1 Config]
        N2Config[Node 2 Config]
        NNConfig[Node N Config]
    end

    GM -->|Copy to all| MetaGen
    DL -->|Round-robin distribute| MetaGen
    CT -->|Copy to all| MetaGen

    MetaGen --> N1Config
    MetaGen --> N2Config
    MetaGen --> NNConfig

    N1Config -->|Environment vars| Node1[Node 1]
    N2Config -->|Environment vars| Node2[Node 2]
    NNConfig -->|Environment vars| NodeN[Node N]
```

### Metadata Types

1. **Global Metadata**: Shared across all nodes
   ```yaml
   global_metadata:
     database_url: "postgres://..."
     api_key: "secret123"
   ```

2. **Distributed Lists**: Values split across nodes (round-robin)
   ```yaml
   distributed_lists:
     worker_ids: [1, 2, 3, 4, 5]  # Node 0 gets 1, Node 1 gets 2, etc.
     regions: ["us-west", "us-east"]  # Cycles through
   ```

3. **Config Template**: Static per-node configuration
   ```yaml
   config_template:
     memory_limit: "2G"
     timeout: 300
   ```

---

## Concurrency & Parallelism

### Concurrent Operations

```mermaid
graph TB
    subgraph "Daemon Goroutines"
        Main[Main HTTP Server]
        Cleanup1[Periodic Cleanup<br/>Every 10 min]
        Cleanup2[Periodic Cleanup<br/>Every 1 hour]
    end

    subgraph "Per-Deployment Goroutines"
        Deploy[Deployment Executor]
    end

    subgraph "Per-Node Goroutines"
        Node1[Node 1 Provisioner]
        Node2[Node 2 Provisioner]
        NodeN[Node N Provisioner]
    end

    Main --> Deploy
    Deploy --> Node1
    Deploy --> Node2
    Deploy --> NodeN
```

### Key Concurrency Patterns

1. **Asynchronous Deployment**: Deployment processing happens in background goroutine
2. **Parallel Node Provisioning**: Each node provisioned concurrently
3. **Thread-Safe State**: All state updates protected by sync.RWMutex
4. **Non-Blocking Cleanup**: File cleanup happens in background after termination

---

## Security Model

```mermaid
sequenceDiagram
    participant Node as Cloud Instance
    participant API as Daemon API
    participant Store as State Store

    Note over Node: Instance boots with<br/>provision token in<br/>user-data

    Node->>API: POST /register {provision_token}
    API->>Store: FindNodeByProvisionToken()
    Store-->>API: Found node record
    API->>Store: GenerateAuthToken()
    API-->>Node: {auth_token}

    Note over Node: Use auth_token for<br/>all subsequent requests

    Node->>API: GET /assets {Bearer auth_token}
    API->>Store: FindNodeByAuthToken()
    Store-->>API: Validated
    API-->>Node: worker_bundle.tar.gz

    Node->>API: POST /heartbeat {Bearer auth_token}
    API->>Store: FindNodeByAuthToken()
    Store-->>API: Validated
    API-->>Node: OK
```

### Security Flow

1. **Provision Token**: One-time use token embedded in instance user-data
2. **Auth Token**: Long-lived token generated after registration
3. **Token Validation**: All node requests validate auth token
4. **No Persistence**: Tokens stored in-memory only (daemon restart = new tokens)

---

## Failure Handling

```mermaid
graph TB
    subgraph "Failure Detection"
        Heartbeat[Heartbeat Timeout]
        Status[Status Update: Failed]
        Provision[Provision Failure]
    end

    subgraph "State Updates"
        NodeFail[Update Node: Failed]
        CheckDeploy[Check Deployment Completion]
    end

    subgraph "Deployment Status"
        AllDone{All Nodes<br/>Complete or Failed?}
        AnyFailed{Any Node<br/>Failed?}
        DeployFail[Deployment: Failed]
        DeployComplete[Deployment: Completed]
    end

    Heartbeat --> NodeFail
    Status --> NodeFail
    Provision --> NodeFail

    NodeFail --> CheckDeploy
    CheckDeploy --> AllDone
    AllDone -->|Yes| AnyFailed
    AnyFailed -->|Yes| DeployFail
    AnyFailed -->|No| DeployComplete
    AllDone -->|No| Continue[Continue Running]
```

### Failure Scenarios

1. **Node Provision Failure**: Node marked as failed, deployment continues
2. **Node Heartbeat Timeout**: Detected by monitoring (future enhancement)
3. **Script Execution Failure**: Node reports failed status
4. **Deployment Failure**: If any node fails, entire deployment marked failed
5. **Partial Success**: Some nodes complete, others fail = deployment failed

---

## Extension Points

### Adding a New Cloud Provider

1. Implement the `Provider` interface in [internal/cloud/provider.go](../internal/cloud/provider.go)
2. Add provider initialization in `Orchestrator.createProvider()` in [internal/orchestrator/engine.go](../internal/orchestrator/engine.go)
3. Update `ProviderFactory.NewProvider()` in [internal/cloud/provider.go](../internal/cloud/provider.go)

```go
type Provider interface {
    ProvisionInstance(ctx context.Context, config InstanceConfig) (*InstanceInfo, error)
    GetInstanceStatus(ctx context.Context, instanceID string) (string, error)
    TerminateInstance(ctx context.Context, instanceID string) error
    GetProviderName() string
}
```

### Adding New API Endpoints

1. Add route in [cmd/taskflyd/main.go](../cmd/taskflyd/main.go) `runDaemon()` function
2. Implement handler function in same file
3. Update state via `Store` methods in [internal/state/store.go](../internal/state/store.go)

---

## Performance Characteristics

| Operation                   | Scalability      | Notes                              |
|-----------------------------|------------------|------------------------------------|
| Deployment Creation         | O(n) nodes       | Linear with node count             |
| Node Provisioning           | Parallel         | All nodes provisioned concurrently |
| State Lookups               | O(1)             | HashMap-based store                |
| Heartbeat Updates           | O(1)             | Direct node lookup                 |
| Deployment Completion Check | O(n) nodes       | Scans all nodes per status update  |
| Cleanup                     | O(d) deployments | Linear with deployment count       |

### Bottlenecks

1. **In-Memory State**: Limited by RAM, no persistence
2. **Cloud Provider Rate Limits**: Parallel provisioning may hit API limits
3. **Bundle Transfer**: Network bandwidth for large bundles
4. **Completion Check**: Currently scans all nodes on every status update

---

## Future Enhancements

### Planned Features
- [ ] Persistent state storage (database)
- [ ] Node heartbeat timeout detection
- [ ] Graceful node termination
- [ ] Resource pool for instance reuse
- [ ] GCP and Azure providers
- [ ] Deployment templates
- [ ] Node auto-scaling
- [ ] Web UI dashboard
- [ ] Metrics and monitoring
- [ ] Rolling deployments
- [ ] Deployment rollback

### Architectural Improvements
- [ ] Message queue for node events
- [ ] Separate orchestrator service
- [ ] Multi-tenancy support
- [ ] API rate limiting
- [ ] Webhook notifications
- [ ] Deployment snapshots
- [ ] Node health checks beyond heartbeat

---

## Dependencies

### Core Libraries
- **Echo**: HTTP web framework
- **Logrus**: Structured logging
- **urfave/cli**: CLI argument parsing
- **AWS SDK**: AWS provider implementation
- **YAML**: Configuration parsing

### Standard Library Usage
- `archive/tar` & `compress/gzip`: Bundle handling
- `sync`: Concurrency primitives (RWMutex)
- `context`: Cancellation and timeouts
- `crypto/rand`: Token generation
- `net/http`: HTTP client/server

---

## Monitoring & Observability

### Current Logging
- Structured logs via Logrus
- Log levels: Debug, Info, Warn, Error
- Key events logged:
  - Deployment lifecycle
  - Node registration
  - Provisioning status
  - Cleanup operations

### Available Metrics (via /stats endpoint)
```json
{
  "total_deployments": 5,
  "total_nodes": 25,
  "deployment_status": {
    "running": 2,
    "completed": 3
  },
  "uptime": "2h30m15s"
}
```

### Future Observability
- Prometheus metrics
- Distributed tracing
- Performance profiling
- Audit logs
- Error tracking integration

---

## Configuration Management

### Daemon Configuration
- Environment variables or CLI flags
- Configuration hierarchy:
  1. CLI flags (highest priority)
  2. Environment variables
  3. Default values

### Node Configuration
- Passed as environment variables to node agent
- Merged from three sources:
  1. Global metadata (all nodes)
  2. Distributed lists (per-node selection)
  3. Config template (all nodes)

---

## Testing Strategy

### Unit Tests
- State store operations
- Metadata generation
- Provider interface implementations
- Configuration parsing

### Integration Tests
- API endpoint testing
- End-to-end deployment flow
- Cloud provider interactions (mocked)
- Bundle extraction and processing

### Manual Testing
- Real cloud provisioning
- Multi-node deployments
- Failure scenarios
- Cleanup operations

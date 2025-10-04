# TaskFly

**Distributed task orchestration made simple.**

TaskFly is a powerful CLI tool and daemon for deploying and managing distributed applications across cloud infrastructure. It provides a simple yet flexible way to spin up multiple nodes, distribute configuration, and run tasks across your cloud environment.

It was designed and built because I didn't have the time to invest in a full blown Kubernetes setup for small to medium scale distributed workloads that are one-off or short-lived. TaskFly aims to fill that gap with a lightweight, easy-to-use solution. It is ideal for cases like:

- Running distributed batch data processing jobs
- Spinning up small clusters for testing or development of ephemeral workloads
- Rapid prototyping of distributed applications  

**TaskFly is currently in early development.** While the core functionality is in place, expect ongoing improvements, new features, and bug fixes. This was written with the power of vibe coding in a single afternoon so take that as you will. Contributions are welcome!

## Architecture

TaskFly consists of three main components:

### 1. TaskFly CLI (`taskfly`)
The client-side command-line interface for managing deployments. Features include:
- Bundle and deploy applications
- Interactive REPL shell for managing deployments
- Real-time dashboard with live metrics (refreshes every second)
- Docker-compose style log streaming
- Deployment status monitoring

### 2. TaskFly Daemon (`taskflyd`)
The server-side daemon that handles:
- Cloud infrastructure provisioning
- Node registration and authentication
- Configuration distribution
- Deployment lifecycle management
- Distributed log collection and storage
- System metrics aggregation
- Health monitoring and cleanup

### 3. TaskFly Agent (`taskfly-agent`)
Lightweight agent deployed to each node that:
- Registers with the daemon
- Downloads application bundles
- Executes setup scripts
- Streams logs back to daemon (every 2 seconds)
- Collects and reports system metrics (CPU, memory, load)
- Sends regular heartbeats

## Features

- 🚀 **Simple Deployment** - Bundle and deploy with a single command
- 📦 **Asset Distribution** - Automatically distribute application files to all nodes
- 📊 **Real-time Dashboard** - Live monitoring with system metrics, deployment status, and progress tracking (auto-refreshes every second)
- 📝 **Distributed Logging** - Docker-compose style log streaming from all nodes with follow mode and filtering
- 💻 **Interactive Shell** - REPL interface for managing deployments with command history
- 📈 **System Metrics** - CPU cores, memory usage, and load average monitoring from all nodes
- 🎨 **Beautiful Tables** - Clean, formatted output with color-coded status indicators
- 🔄 **Flexible Configuration** - Support for global metadata and distributed lists
- 🧹 **Automatic Cleanup** - Built-in cleanup for completed deployments
- ☁️ **Multi-Cloud Ready** - Extensible architecture for multiple cloud providers

## Quick Start

### Starting the Daemon

The taskflyd daemon can be started with default or custom settings and runs in memory. In most cases, you should dedicate a server or VM to run the daemon continuously.

```bash
# Start with default settings (localhost:8080)
taskflyd

# Start with custom settings
taskflyd --listen-ip 10.0.0.1 --listen-port 8080 --daemon-ip <your-reachable-ip>

# With verbose logging
taskflyd --verbose
```

### Deploying an Application

1. Create a `taskfly.yml` configuration file in your project directory (similar to a docker-compose file). Example:

```yaml
cloud_provider: "aws"  # or "gcp", "azure" (coming soon)

instance_config:
  aws:
    region: "us-west-2"
    instance_type: "t3.micro"
    ami: "ami-0c55b159cbfafe1f0"

# Files to bundle and distribute to nodes
application_files:
  - "app.py"
  - "requirements.txt"
  - "config.json"

# Where files will be extracted on remote nodes
remote_dest_dir: "/opt/myapp"

# Script to run after setup (optional)
remote_script_to_run: "setup.sh"

# Name of the bundle file (optional)
bundle_name: "myapp_bundle.tar.gz"

# Node configuration
nodes:
  count: 5  # Number of nodes to provision

  # Global metadata available to all nodes
  global_metadata:
    app_name: "my_distributed_app"
    version: "1.0.0"
    environment: "production"

  # Lists distributed across nodes (each node gets one item)
  distributed_lists:
    worker_ids: [1, 2, 3, 4, 5]
    zones: ["us-west-2a", "us-west-2b", "us-west-2c", "us-west-2a", "us-west-2b"]

  # Template for node-specific config
  config_template:
    log_level: "info"
    heartbeat_interval: 30
```

2. Deploy your application using the CLI:

```bash
# Deploy to daemon on localhost
taskfly up

# Deploy to remote daemon
taskfly --daemon-ip <daemon-ip> up
```

### Managing Deployments

```bash
# List all deployments
taskfly list

# Get deployment status
taskfly status --id <deployment-id>

# View logs from deployment (Docker-compose style)
taskfly logs --id <deployment-id>

# Follow logs in real-time
taskfly logs --id <deployment-id> --follow

# Filter logs by specific node
taskfly logs --id <deployment-id> --node <node-id>

# Terminate a deployment
taskfly down --id <deployment-id>
```

### Interactive Shell & Dashboard

```bash
# Start interactive shell
taskfly shell

# Within the shell, you can use these commands:
taskfly> dashboard     # Show live dashboard (refreshes every second, Ctrl+C to exit)
taskfly> list          # List all deployments
taskfly> status <id>   # Show deployment status
taskfly> logs <id>     # View logs
taskfly> down <id>     # Terminate deployment
taskfly> help          # Show all commands
taskfly> exit          # Exit shell

# Or run dashboard directly
taskfly dashboard
```

**Dashboard Features:**
- **System Resources**: Total CPU cores, load average, memory usage, active nodes
- **Deployment Overview**: Deployment counts by status (running, provisioning, completed, failed)
- **Recent Deployments**: Last 5 deployments with progress bars
- **Node Metrics**: Per-node CPU, load, memory, and last update time
- **Color-coded alerts**: Green (healthy), Yellow (70-90% utilization), Red (>90% utilization)
- **Auto-refresh**: Updates every second with live data

## Configuration

### Environment Variables

#### TaskFly CLI
- `TASKFLY_DAEMON_IP` - IP address of the TaskFly daemon (default: `localhost`)
- `TASKFLY_DAEMON_PORT` - Port of the TaskFly daemon (default: `8080`)
- `TASKFLY_VERBOSE` - Enable verbose logging

#### TaskFly Daemon
- `TASKFLY_LISTEN_IP` - IP address to listen on (default: `0.0.0.0`)
- `TASKFLY_LISTEN_PORT` - Port to listen on (default: `8080`)
- `TASKFLY_DAEMON_IP` - Public IP for nodes to callback (default: `localhost`)
- `TASKFLY_DAEMON_PORT` - Public port for node callbacks (default: `8080`)
- `TASKFLY_VERBOSE` - Enable verbose logging
- `TASKFLY_DEPLOYMENT_DIR` - Directory for deployment files (default: `deployments`)

### CLI Flags

```bash
# Global flags (available for all commands)
--daemon-ip, -d     IP address of daemon (default: "localhost")
--daemon-port, -p   Port of daemon (default: "8080")
--verbose, -v       Enable verbose logging

# Example usage
taskfly --daemon-ip 10.0.0.1 --daemon-port 8080 list
taskfly -d 10.0.0.1 -p 8080 dashboard
```

### Node Configuration Patterns

TaskFly supports flexible node configuration through three mechanisms:

#### 1. Global Metadata
Shared configuration available to all nodes:
```yaml
global_metadata:
  app_name: "my_app"
  database_url: "postgres://..."
  api_key: "secret123"
```

#### 2. Distributed Lists
Values distributed evenly across nodes (round-robin):
```yaml
distributed_lists:
  worker_ids: [1, 2, 3, 4, 5]  # Node 0 gets 1, Node 1 gets 2, etc.
  regions: ["us-west", "us-east"]  # Cycles through values
```

#### 3. Config Template
Static configuration for each node:
```yaml
config_template:
  memory_limit: "2G"
  timeout: 300
```

## Contributing
TaskFly is actively looking for maintainers so feel free to help out when:

- Reporting a bug
- Discussing the current state of the code
- Submitting a fix
- Proposing new features

### We Develop with Github
We use github to host code, to track issues and feature requests, as well as accept pull requests.

### All Code Changes Happen Through Pull Requests
1. Fork the repo and create your branch from `master`.
2. If you've added code that should be tested, add tests.
3. If you've changed APIs, update the documentation.
4. Ensure the test suite passes.
5. Make sure your code lints.
6. Issue that pull request!

### Any contributions you make will be under the MIT Software License
In short, when you submit code changes, your submissions are understood to be under the same [MIT License](http://choosealicense.com/licenses/mit/) that covers the project. Feel free to contact the maintainers if that's a concern.

### Report bugs using Github's [Issues](https://github.com/JustinTimperio/TaskFly/issues)
We use GitHub issues to track public bugs. Report a bug by opening a new issue; it's that easy!

### Write bug reports with detail, background, and sample code
**Great Bug Reports** tend to have:

- A quick summary and/or background
- Steps to reproduce
  - Be specific!
  - Give sample code if you can.
- What you expected would happen
- What actually happens
- Notes (possibly including why you think this might be happening, or stuff you tried that didn't work)

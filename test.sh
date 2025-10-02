#!/bin/bash

# TaskFly Test Runner Script

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print colored output
print_msg() {
    echo -e "${GREEN}[TaskFly Test]${NC} $1"
}

print_warn() {
    echo -e "${YELLOW}[Warning]${NC} $1"
}

print_error() {
    echo -e "${RED}[Error]${NC} $1"
}

# Function to show help
show_help() {
    echo "TaskFly Test Runner"
    echo ""
    echo "Usage: ./test.sh [command]"
    echo ""
    echo "Commands:"
    echo "  unit              Run unit tests only"
    echo "  integration       Run integration tests (requires LocalStack)"
    echo "  localstack        Run LocalStack-specific EC2 tests"
    echo "  e2e               Run end-to-end deployment test (requires LocalStack)"
    echo "  all               Run all tests"
    echo "  localstack-up     Start LocalStack container"
    echo "  localstack-down   Stop LocalStack container"
    echo "  localstack-logs   Show LocalStack logs"
    echo "  clean             Clean test cache"
    echo "  help              Show this help message"
    echo ""
    echo "Examples:"
    echo "  ./test.sh unit                    # Run unit tests"
    echo "  ./test.sh localstack-up           # Start LocalStack"
    echo "  ./test.sh integration             # Run integration tests"
    echo "  ./test.sh e2e                     # Run end-to-end test with deployment"
    echo "  ./test.sh localstack-down         # Stop LocalStack"
}

# Check if LocalStack is running
check_localstack() {
    if curl -s http://localhost:4566/_localstack/health > /dev/null 2>&1; then
        return 0
    else
        return 1
    fi
}

# Run unit tests
run_unit_tests() {
    print_msg "Running unit tests..."
    go test -v -short ./internal/cloud/
}

# Run integration tests
run_integration_tests() {
    print_msg "Running integration tests..."

    if ! check_localstack; then
        print_warn "LocalStack is not running. Start it with: ./test.sh localstack-up"
        exit 1
    fi

    export LOCALSTACK_ENDPOINT=http://localhost:4566
    go test -v -tags=integration ./internal/cloud/
}

# Run LocalStack-specific tests
run_localstack_tests() {
    print_msg "Running LocalStack EC2 tests..."

    if ! check_localstack; then
        print_warn "LocalStack is not running. Start it with: ./test.sh localstack-up"
        exit 1
    fi

    export LOCALSTACK_ENDPOINT=http://localhost:4566
    go test -v -tags=integration -run TestLocalStack ./internal/cloud/
}

# Start LocalStack
start_localstack() {
    print_msg "Starting LocalStack..."

    if check_localstack; then
        print_warn "LocalStack is already running"
        return 0
    fi

    docker-compose -f docker-compose.test.yml up -d

    print_msg "Waiting for LocalStack to be ready..."
    for i in {1..30}; do
        if check_localstack; then
            print_msg "LocalStack is ready at http://localhost:4566"
            return 0
        fi
        echo -n "."
        sleep 2
    done

    print_error "LocalStack failed to start after 60 seconds"
    docker-compose -f docker-compose.test.yml logs localstack
    exit 1
}

# Stop LocalStack
stop_localstack() {
    print_msg "Stopping LocalStack..."
    docker-compose -f docker-compose.test.yml down
    print_msg "LocalStack stopped"
}

# Show LocalStack logs
show_localstack_logs() {
    docker-compose -f docker-compose.test.yml logs -f localstack
}

# Run all tests
run_all_tests() {
    print_msg "Running all tests..."

    # Run unit tests first
    run_unit_tests

    # Check if LocalStack is running for integration tests
    if check_localstack; then
        run_integration_tests
    else
        print_warn "Skipping integration tests (LocalStack not running)"
        print_warn "Start LocalStack with: ./test.sh localstack-up"
    fi
}

# Run end-to-end deployment test
run_e2e_test() {
    print_msg "Running end-to-end deployment test with 3 LocalStack instances"
    echo ""

    if ! check_localstack; then
        print_warn "LocalStack is not running. Start it with: ./test.sh localstack-up"
        exit 1
    fi

    # Configuration
    local DAEMON_PORT=$((8080 + RANDOM % 1000))  # Random port between 8080-9079
    local DAEMON_PID=""
    local TEST_DIR="/tmp/taskfly-e2e-test-$$"  # Unique temp dir per process
    local NUM_INSTANCES=3

    # Cleanup function using inline trap
    trap 'print_msg "Cleaning up..."; [ -n "$DAEMON_PID" ] && kill $DAEMON_PID 2>/dev/null; rm -rf "$TEST_DIR" /tmp/taskflyd /tmp/taskfly' EXIT INT TERM

    # Build binaries
    print_msg "Building TaskFly binaries..."
    go build -o /tmp/taskflyd ./cmd/taskflyd || { print_error "Failed to build taskflyd"; return 1; }
    go build -o /tmp/taskfly ./cmd/taskfly || { print_error "Failed to build taskfly"; return 1; }

    # Setup test environment
    print_msg "Setting up test environment..."
    mkdir -p "$TEST_DIR/deployments"
    mkdir -p "$TEST_DIR/test_app"
    cp -r example_app/* "$TEST_DIR/test_app/"

    # Create test config for 3 instances
    cat > "$TEST_DIR/test_app/taskfly.yml" <<'EOF'
cloud_provider: aws

instance_config:
  aws:
    region: us-east-1
    image_id: ami-12345678
    instance_type: t2.micro
    key_name: test-key
    security_groups:
      - default
    use_localstack: true
    localstack_endpoint: http://localhost:4566

application_files:
  - app.py
  - setup.sh

remote_dest_dir: /opt/taskfly
remote_script_to_run: setup.sh
bundle_name: test_bundle.tar.gz

nodes:
  count: 3
  global_metadata:
    PROJECT: test-project
    BUCKET: test-bucket
  distributed_lists:
    WORKER_ID: [1, 2, 3]
    BATCH_START: [0, 100, 200]
    BATCH_END: [100, 200, 300]
  config_template:
    TOTAL_WORKERS: "3"
EOF

    # Start daemon
    print_msg "Starting TaskFly daemon on port $DAEMON_PORT..."
    cd "$TEST_DIR"
    /tmp/taskflyd --listen-ip 0.0.0.0 --listen-port $DAEMON_PORT \
        --daemon-ip localhost --deployment-dir "$TEST_DIR/deployments" \
        > "$TEST_DIR/daemon.log" 2>&1 &
    DAEMON_PID=$!

    # Wait for daemon
    print_msg "Waiting for daemon (PID: $DAEMON_PID)..."
    for i in {1..30}; do
        if curl -s http://localhost:$DAEMON_PORT/api/v1/health > /dev/null 2>&1; then
            print_msg "✓ Daemon ready"
            break
        fi
        [ $i -eq 30 ] && { print_error "Daemon timeout"; return 1; }
        sleep 1
    done

    # Create bundle
    print_msg "Creating deployment bundle..."
    cd "$TEST_DIR/test_app"
    tar -czf "$TEST_DIR/test_bundle.tar.gz" taskfly.yml app.py setup.sh

    # Deploy
    print_msg "Deploying to $NUM_INSTANCES instances..."
    local RESPONSE=$(curl -s -X POST -F "bundle=@$TEST_DIR/test_bundle.tar.gz" \
        http://localhost:$DAEMON_PORT/api/v1/deployments)

    local DEPLOYMENT_ID=$(echo "$RESPONSE" | grep -o '"deployment_id":"[^"]*"' | head -1 | cut -d'"' -f4)

    if [ -z "$DEPLOYMENT_ID" ]; then
        print_error "Failed to parse deployment ID from response"
        echo "Response: $RESPONSE"
        cat "$TEST_DIR/daemon.log" | tail -20
        return 1
    fi
    print_msg "✓ Deployment: $DEPLOYMENT_ID"

    # Monitor provisioning (LocalStack only provisions, doesn't boot)
    print_msg "Monitoring EC2 provisioning (max 30 sec)..."
    local TIMEOUT=30
    local ELAPSED=0
    while [ $ELAPSED -lt $TIMEOUT ]; do
        local RESPONSE=$(curl -s http://localhost:$DAEMON_PORT/api/v1/deployments/$DEPLOYMENT_ID)
        # Get deployment status (first "status" field, not node status)
        local STATUS=$(echo "$RESPONSE" | grep -o '"status":"[^"]*"' | head -1 | cut -d'"' -f4)

        # LocalStack provisions quickly but instances won't actually boot
        # Accept both "booting" and "running" as success (LocalStack won't progress past booting)
        if [ "$STATUS" = "provisioning" ]; then
            print_msg "Status: provisioning (waiting for EC2...)"
        elif [ "$STATUS" = "booting" ]; then
            # In LocalStack, "booting" means instances are provisioned but won't actually boot
            print_msg "✓ Deployment in booting state (3 EC2 instances provisioned)"
            break
        elif [ "$STATUS" = "running" ]; then
            print_msg "✓ Deployment running (3 EC2 instances provisioned)"
            break
        elif [ "$STATUS" = "failed" ]; then
            print_error "Provisioning failed"
            return 1
        else
            print_msg "Status: $STATUS"
        fi

        sleep 2
        ELAPSED=$((ELAPSED + 2))
    done

    [ $ELAPSED -ge $TIMEOUT ] && { print_error "Provisioning timeout (status: $STATUS)"; return 1; }

    print_msg "Note: LocalStack only simulates EC2 API, instances won't actually boot"

    # Verify EC2 instances exist in LocalStack
    print_msg "Verifying EC2 instances in LocalStack..."

    # Check deployment has the right number of nodes
    local DETAILS=$(curl -s http://localhost:$DAEMON_PORT/api/v1/deployments/$DEPLOYMENT_ID)
    local INSTANCE_COUNT=$(echo "$DETAILS" | grep -o '"instance_id":"[^"]*"' | wc -l | tr -d ' ')

    if [ "$INSTANCE_COUNT" -eq "$NUM_INSTANCES" ]; then
        print_msg "✓ Deployment has $INSTANCE_COUNT EC2 instances"
    else
        print_error "Expected $NUM_INSTANCES instances, got $INSTANCE_COUNT"
        return 1
    fi

    # Verify instances exist in LocalStack EC2
    local LOCALSTACK_COUNT=$(aws --endpoint-url=http://localhost:4566 ec2 describe-instances \
        --region us-east-1 \
        --filters "Name=tag:CreatedBy,Values=TaskFly" "Name=instance-state-name,Values=running" \
        --query 'Reservations[*].Instances[*].InstanceId' \
        --output text 2>/dev/null | wc -w | tr -d ' ')

    if [ "$LOCALSTACK_COUNT" -ge "$NUM_INSTANCES" ]; then
        print_msg "✓ Found $LOCALSTACK_COUNT running instances in LocalStack EC2"
    else
        print_warn "Expected $NUM_INSTANCES instances in LocalStack, found $LOCALSTACK_COUNT"
    fi

    echo ""
    print_msg "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
    print_msg "✓ End-to-End Test PASSED!"
    print_msg "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"

    # Cleanup
    print_msg "Cleaning up deployment..."
    curl -s -X DELETE http://localhost:$DAEMON_PORT/api/v1/deployments/$DEPLOYMENT_ID > /dev/null

    # Stop daemon
    if [ -n "$DAEMON_PID" ]; then
        kill $DAEMON_PID 2>/dev/null || true
        wait $DAEMON_PID 2>/dev/null || true
    fi

    # Clean files
    rm -rf "$TEST_DIR"
    rm -f /tmp/taskflyd /tmp/taskfly
    trap - EXIT INT TERM
}

# Clean test cache
clean_cache() {
    print_msg "Cleaning test cache..."
    go clean -testcache
    print_msg "Test cache cleaned"
}

# Main script logic
case "${1:-help}" in
    unit)
        run_unit_tests
        ;;
    integration)
        run_integration_tests
        ;;
    localstack)
        run_localstack_tests
        ;;
    e2e)
        run_e2e_test
        ;;
    all)
        run_all_tests
        ;;
    localstack-up)
        start_localstack
        ;;
    localstack-down)
        stop_localstack
        ;;
    localstack-logs)
        show_localstack_logs
        ;;
    clean)
        clean_cache
        ;;
    help|--help|-h)
        show_help
        ;;
    *)
        print_error "Unknown command: $1"
        echo ""
        show_help
        exit 1
        ;;
esac
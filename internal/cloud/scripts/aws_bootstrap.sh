#!/bin/bash
set -e
set -x  # Enable command tracing

# Update package lists and install dependencies for Ubuntu/Debian
apt-get update -y
apt-get install -y curl wget unzip file

echo "=== TaskFly Node Bootstrap Starting ==="
echo "Host: $(hostname)"
echo "User: $(whoami)"
echo "Date: $(date)"

# Get instance metadata
INSTANCE_ID=$(curl -s http://169.254.169.254/latest/meta-data/instance-id)
PUBLIC_IP=$(curl -s http://169.254.169.254/latest/meta-data/public-ipv4)

echo "Instance ID: $INSTANCE_ID"
echo "Public IP: $PUBLIC_IP"
echo "Provision Token: {{.ProvisionToken}}"
echo "Daemon URL: {{.DaemonURL}}"

# Test network connectivity first
echo "=== Testing Network Connectivity ==="
echo "Testing DNS resolution..."
nslookup google.com || echo "DNS resolution failed"

echo "Testing daemon connectivity..."
curl -v --connect-timeout 10 "{{.DaemonURL}}/api/v1/health" || echo "Daemon health check failed"

# Create unique TaskFly directory
echo "=== Creating Working Directory ==="
WORK_DIR="/opt/taskfly-{{.ProvisionToken}}"
mkdir -p "$WORK_DIR"
cd "$WORK_DIR"
echo "Working directory: $WORK_DIR"

echo "=== Registering with Daemon ==="
echo "Calling: {{.DaemonURL}}/api/v1/nodes/register"
echo "Payload: {\"provision_token\": \"{{.ProvisionToken}}\"}"

# Register with daemon (with verbose output)
curl -v -X POST "{{.DaemonURL}}/api/v1/nodes/register" \
  -H "Content-Type: application/json" \
  -d '{"provision_token": "{{.ProvisionToken}}"}' \
  -o registration.json \
  --connect-timeout 30 \
  --max-time 60

echo "=== Registration Response ==="
if [ -f registration.json ]; then
    echo "Registration file created:"
    cat registration.json
    echo ""

    # Check if registration was successful
    if grep -q "error" registration.json; then
        echo "ERROR: Registration failed!"
        exit 1
    else
        echo "SUCCESS: Registration appears successful"

        # Extract key values from registration response
        AUTH_TOKEN=$(cat registration.json | grep -o '"auth_token":"[^"]*"' | cut -d'"' -f4)
        ASSETS_URL=$(cat registration.json | grep -o '"assets_url":"[^"]*"' | cut -d'"' -f4)
        STATUS_URL=$(cat registration.json | grep -o '"status_url":"[^"]*"' | cut -d'"' -f4)
        NODE_ID=$(cat registration.json | grep -o '"node_id":"[^"]*"' | cut -d'"' -f4)

        echo "Extracted AUTH_TOKEN: $AUTH_TOKEN"
        echo "Extracted ASSETS_URL: $ASSETS_URL"
        echo "Extracted STATUS_URL: $STATUS_URL"
        echo "Extracted NODE_ID: $NODE_ID"

        # Test the auth token first with a simple API call
        echo "=== Testing Auth Token ==="
        if curl -s -H "Authorization: Bearer $AUTH_TOKEN" "{{.DaemonURL}}/api/v1/health" > /dev/null; then
            echo "Auth token appears to be valid (health check passed)"
        else
            echo "WARNING: Auth token may be invalid (health check failed)"
            echo "Proceeding anyway..."
        fi

        echo "=== Updating Status to Downloading ==="
        # Use the status URL to post status updates
        curl -v -X POST "$STATUS_URL" \
            -H "Content-Type: application/json" \
            -H "Authorization: Bearer $AUTH_TOKEN" \
            -d '{"status": "downloading_assets", "message": "Downloading deployment bundle"}' \
            --connect-timeout 30 \
            --max-time 60

        echo "=== Downloading Bundle ==="

        # Try the assets URL first
        if [ -n "$ASSETS_URL" ]; then
            echo "Trying ASSETS_URL: $ASSETS_URL"
            curl -v -H "Authorization: Bearer $AUTH_TOKEN" \
                "$ASSETS_URL" \
                -o bundle.tar.gz \
                --connect-timeout 30 \
                --max-time 120
        else
            echo "No ASSETS_URL provided, using default endpoint"
            # Fallback to default assets endpoint
            curl -v -H "Authorization: Bearer $AUTH_TOKEN" \
                "{{.DaemonURL}}/api/v1/nodes/assets" \
                -o bundle.tar.gz \
                --connect-timeout 30 \
                --max-time 120
        fi

        # Check the HTTP response and downloaded content
        CURL_EXIT_CODE=$?
        if [ $CURL_EXIT_CODE -ne 0 ]; then
            echo "ERROR: curl failed with exit code $CURL_EXIT_CODE"
            exit 1
        fi

        if [ -f bundle.tar.gz ]; then
            echo "Bundle file created, checking content and size..."
            ls -la bundle.tar.gz
            file bundle.tar.gz

            # Check if it's actually a tar.gz file
            if file bundle.tar.gz | grep -q "gzip compressed"; then
                echo "Bundle appears to be a valid gzip file"
            else
                echo "ERROR: Downloaded file is not a gzip archive!"
                echo "File size: $(wc -c < bundle.tar.gz) bytes"
                echo "Content of downloaded file:"
                head -20 bundle.tar.gz
                echo ""
                echo "This likely means the daemon returned an error response instead of the bundle."
                echo "Check the daemon logs for authentication or authorization issues."
                exit 1
            fi
        else
            echo "ERROR: Bundle file was not created!"
            exit 1
        fi

        if [ -f bundle.tar.gz ]; then
            echo "Bundle downloaded successfully"

            echo "=== Extracting Bundle ==="
            tar -xzf bundle.tar.gz
            ls -la

            echo "=== Setting Environment Variables ==="
            # Set environment variables from node configuration
            {{range $key, $value := .NodeConfig}}
            export {{$key | ToUpper}}="{{$value}}"
            echo "Set {{$key | ToUpper}}={{$value}}"
            {{end}}

            echo "=== Updating Status to Running ==="
            curl -v -X POST "$STATUS_URL" \
                -H "Content-Type: application/json" \
                -H "Authorization: Bearer $AUTH_TOKEN" \
                -d '{"status": "running_script", "message": "Node is running deployment script"}' \
                --connect-timeout 30 \
                --max-time 60

            echo "=== Executing Deployment Script ==="
            if [ -f "setup.sh" ]; then
                chmod +x setup.sh
                echo "Running setup.sh..."
                ./setup.sh > setup.log 2>&1 &
                SETUP_PID=$!
                echo "Setup script started in background, PID: $SETUP_PID"

                # Give the script a moment to start, then mark as completed
                sleep 2
                echo "=== Updating Status to Completed ==="
                curl -v -X POST "$STATUS_URL" \
                    -H "Content-Type: application/json" \
                    -H "Authorization: Bearer $AUTH_TOKEN" \
                    -d '{"status": "completed", "message": "Deployment script started successfully"}' \
                    --connect-timeout 30 \
                    --max-time 60

                # Schedule cleanup for later (after giving the main script time to run)
                echo "=== Scheduling Directory Cleanup ==="
                (sleep 300 && rm -rf "$WORK_DIR" && echo "Cleaned up working directory $WORK_DIR") &
            else
                echo "No setup.sh found in bundle"
                echo "=== Updating Status to Completed ==="
                curl -v -X POST "$STATUS_URL" \
                    -H "Content-Type: application/json" \
                    -H "Authorization: Bearer $AUTH_TOKEN" \
                    -d '{"status": "completed", "message": "No deployment script found, node ready"}' \
                    --connect-timeout 30 \
                    --max-time 60

                # Schedule cleanup for later
                echo "=== Scheduling Directory Cleanup ==="
                (sleep 60 && rm -rf "$WORK_DIR" && echo "Cleaned up working directory $WORK_DIR") &
            fi

            echo "=== Node Deployment Completed ==="
        else
            echo "ERROR: Failed to download bundle!"
            exit 1
        fi
    fi
else
    echo "ERROR: No registration response file created!"
    exit 1
fi

echo "=== Agent Bootstrap Completed Successfully ==="

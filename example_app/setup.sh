#!/bin/bash
set -e

echo "TaskFly deployment starting..."
echo "Worker ID: $WORKER_ID"
echo "Data file: $DATA_FILE"


# Run the application
echo "Running application..."
python3 app.py

echo "TaskFly deployment completed successfully!"
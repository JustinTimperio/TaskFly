#!/usr/bin/env python3
"""
Example TaskFly application
"""

import os
import time
import random
import sys


class Unbuffered(object):
    def __init__(self, stream):
        self.stream = stream

    def write(self, data):
        self.stream.write(data)
        self.stream.flush()

    def writelines(self, datas):
        self.stream.writelines(datas)
        self.stream.flush()

    def __getattr__(self, attr):
        return getattr(self.stream, attr)


sys.stdout = Unbuffered(sys.stdout)


def main():
    # Enhanced metadata from the new system
    worker_id = os.environ.get("WORKER_ID", "unknown")
    worker_index = os.environ.get("WORKER_INDEX", "unknown")
    total_workers = os.environ.get("TOTAL_WORKERS", "unknown")
    project = os.environ.get("PROJECT", "unknown")
    bucket = os.environ.get("BUCKET", "unknown")
    data_source = os.environ.get("DATA_SOURCE", "unknown")
    worker_config = os.environ.get("WORKER_CONFIG", "unknown")
    output_path = os.environ.get("OUTPUT_PATH", "unknown")
    batch_start = os.environ.get("BATCH_START", "0")
    batch_end = os.environ.get("BATCH_END", "0")

    time.sleep(random.randint(1, 10))

    print(f"🚀 TaskFly Example Worker Starting!")
    print(f"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
    print(f"Worker ID: {worker_id}")
    print(f"Worker Index: {worker_index}/{total_workers}")
    print(f"Project: {project}")
    print(f"S3 Bucket: {bucket}")
    print(f"Data Source: {data_source}")
    print(f"Worker Config: {worker_config}")
    print(f"Output Path: {output_path}")
    print(f"Batch Range: {batch_start} - {batch_end}")
    print(f"━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")

    # Simulate processing based on metadata
    batch_size = int(batch_end) - int(batch_start)
    print(f"📊 Processing {batch_size} items in this batch...")

    for i in range(5):
        print(
            f"Worker {worker_index}: Processing step {i+1}/5 (items {int(batch_start) + i*batch_size//5} - {int(batch_start) + (i+1)*batch_size//5})..."
        )
        time.sleep(random.randint(1, 10))

    print(f"✅ Worker {worker_index}: Task completed successfully!")
    print(f"📤 Results saved to: {output_path}")


if __name__ == "__main__":
    main()

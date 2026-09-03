# ONR Chat Load Test

`tools/load_test_chat.py` is a standard-library Python script for exercising a
local OpenAI-compatible ONR endpoint. It defaults to 200 requests and 200
concurrent workers and does not store or print the Access Key.

```bash
export ONR_ACCESS_KEY='<access-key-secret>'
python3 tools/load_test_chat.py
```

Establish a low-concurrency baseline first, then compare it with the 200-way
run. This makes provider throttling and queueing visible in p95/p99 latency:

```bash
python3 tools/load_test_chat.py --requests 20 --concurrency 1
python3 tools/load_test_chat.py --requests 200 --concurrency 200
```

For the average latency of one request, compare the `latency_ms_success avg`
values. `--concurrency 1` sends the next request only after the previous one
finishes, so it is the serial baseline. The second command releases 200 workers
together:

```bash
python3 tools/load_test_chat.py --requests 200 --concurrency 1
python3 tools/load_test_chat.py --requests 200 --concurrency 200
```

Use `latency_ms_success avg` for the arithmetic mean of individual request
latencies. Use `elapsed_ms / requests` only as a throughput-oriented wall-clock
metric; in a concurrent run it is not the average latency experienced by one
request.

Useful options:

```bash
python3 tools/load_test_chat.py \
  --url http://127.0.0.1:3300/v1/chat/completions \
  --model qwen3.8-max \
  --requests 200 \
  --concurrency 200 \
  --max-tokens 16 \
  --timeout 120
```

The output reports status counts, throughput, success rate, p50/p90/p95/p99,
time to first response-body byte, and complete-response latency. A nonzero exit
code means at least one request failed.

This result includes upstream provider latency, TCP setup, Access Key lookup,
usage extraction, and ONR processing. It does not isolate ONR's own overhead.
Use a test Access Key, control provider cost, and repeat at lower concurrency
before increasing traffic. To isolate gateway overhead, run the same test
against a local mock upstream through an explicit test provider DSL.

Interpret the result using the status distribution and successful-request p95
and p99. `429` usually indicates provider or account rate limiting; connection
errors point to the listener, operating-system limits, or load balancer; rising
p95/p99 with stable `2xx` responses indicates queueing somewhere in the full
ONR-to-provider path.

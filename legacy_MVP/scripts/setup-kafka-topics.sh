#!/usr/bin/env bash
# setup-kafka-topics.sh
# Creates the required Kafka topics. Safe to run multiple times (--if-not-exists).
# Run from inside the Kafka broker container or any host with kafka-topics.sh on PATH.

set -euo pipefail

BOOTSTRAP_SERVER="${BOOTSTRAP_SERVER:-kafka-1:9092}"

echo "==> Waiting for Kafka to be ready at ${BOOTSTRAP_SERVER}..."
until kafka-topics.sh --bootstrap-server "${BOOTSTRAP_SERVER}" --list > /dev/null 2>&1; do
  sleep 2
done
echo "==> Kafka is ready."

# ── Topic definitions ───────────────────────────────────────
# Format: TOPIC_NAME PARTITIONS REPLICATION_FACTOR
TOPICS=(
  "metrics-topic          6  3"
  "analytics-output-topic 3  3"
  "infra-events-topic     1  3"
)

for entry in "${TOPICS[@]}"; do
  read -r topic partitions rf <<< "${entry}"
  echo "==> Creating topic: ${topic}  (partitions=${partitions}, rf=${rf})"
  kafka-topics.sh \
    --bootstrap-server "${BOOTSTRAP_SERVER}" \
    --create \
    --if-not-exists \
    --topic "${topic}" \
    --partitions "${partitions}" \
    --replication-factor "${rf}"
done

echo "==> All topics created. Current topic list:"
kafka-topics.sh --bootstrap-server "${BOOTSTRAP_SERVER}" --list

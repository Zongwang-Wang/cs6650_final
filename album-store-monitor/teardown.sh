#!/bin/bash
# teardown.sh — Stop the monitoring instance.
# Usage: ./teardown.sh [instance-id]
set -e

INSTANCE_ID="${1:-}"
REGION="${AWS_REGION:-us-west-2}"

if [ -z "$INSTANCE_ID" ]; then
  # Find it by tag
  INSTANCE_ID=$(aws ec2 describe-instances \
    --filters "Name=tag:Name,Values=album-store-monitor" \
              "Name=instance-state-name,Values=running,stopped" \
    --query 'Reservations[0].Instances[0].InstanceId' \
    --output text --region "$REGION" 2>/dev/null || echo "")
fi

if [ -z "$INSTANCE_ID" ] || [ "$INSTANCE_ID" = "None" ]; then
  echo "No monitor instance found."
  exit 0
fi

echo "Terminating monitor instance $INSTANCE_ID..."
aws ec2 terminate-instances --instance-ids "$INSTANCE_ID" --region "$REGION" \
  --query 'TerminatingInstances[0].[InstanceId,CurrentState.Name]' --output table

# Clean up security group too
echo "Removing monitor security group..."
sleep 30
aws ec2 delete-security-group \
  --group-name "album-store-monitor-sg" --region "$REGION" 2>/dev/null && echo "SG removed." || echo "SG: already gone or still in use."

echo "Done."

#!/bin/bash
# destroy.sh — Empties S3 bucket (force_destroy doesn't work for non-empty buckets
# in some TF versions) then runs terraform destroy.
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

S3_BUCKET=$(terraform output -raw s3_bucket 2>/dev/null || echo "")

if [ -n "$S3_BUCKET" ]; then
  echo "==> Emptying s3://$S3_BUCKET ..."
  aws s3 rm "s3://$S3_BUCKET" --recursive 2>/dev/null || true
  echo "    Done."
fi

echo ""
echo "==> Running terraform destroy..."
terraform destroy "$@"

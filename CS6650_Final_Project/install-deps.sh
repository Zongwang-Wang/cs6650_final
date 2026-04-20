#!/usr/bin/env bash
set -euo pipefail

echo "==> Step 1: System packages + Docker"
sudo apt-get update && sudo apt-get install -y \
  curl wget git jq unzip ca-certificates gnupg \
  python3 python3-pip python3-venv

curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"
echo "    Docker installed. Run 'newgrp docker' after this script to apply group."

echo "==> Step 3: Go 1.24"
curl -fsSL "https://go.dev/dl/go1.24.2.linux-amd64.tar.gz" -o /tmp/go.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
rm /tmp/go.tar.gz
grep -qxF 'export PATH=$PATH:/usr/local/go/bin' ~/.bashrc \
  || echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
export PATH=$PATH:/usr/local/go/bin
go version

echo "==> Step 4: Terraform"
wget -O- https://apt.releases.hashicorp.com/gpg \
  | sudo gpg --dearmor -o /usr/share/keyrings/hashicorp-archive-keyring.gpg
echo "deb [signed-by=/usr/share/keyrings/hashicorp-archive-keyring.gpg] \
https://apt.releases.hashicorp.com $(lsb_release -cs) main" \
  | sudo tee /etc/apt/sources.list.d/hashicorp.list
sudo apt-get update && sudo apt-get install -y terraform
terraform version

echo "==> Step 5: Python boto3"
pip3 install boto3

echo ""
echo "All done. Run 'newgrp docker' or open a new shell to use Docker without sudo."

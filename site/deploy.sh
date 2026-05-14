#!/usr/bin/env bash
# Deploy tribunal-site to k8s worker (192.168.1.7).
#
# What it does:
#   1. rsync site source to ~/tribunal-site on the worker (skip node_modules + dist)
#   2. docker build the multi-stage Dockerfile (Astro build -> nginx serve)
#   3. ctr import the image into the k8s.io containerd namespace so kubelet sees it
#   4. kubectl apply k8s/{deployment,service}.yaml (idempotent)
#   5. kubectl rollout restart so the new image lands
#
# Prereqs:
#   - SSH access to claude@192.168.1.7 with sudo for ctr / containerd import
#   - SSH access to claude@192.168.1.6 for kubectl
#
# The image is built and stored on the worker node directly. There is no
# registry round-trip — imagePullPolicy: Never plus the matching local image
# is the established pattern in this cluster.

set -euo pipefail

REMOTE_USER="zaphod-beeblebox"
REMOTE_HOST="192.168.6.56"
REMOTE_DIR="/home/${REMOTE_USER}/tribunal-site"
IMAGE_NAME="tribunal-site"
KCTL_HOST="claude@192.168.1.6"
KCTL='sudo kubectl --kubeconfig=/etc/kubernetes/admin.conf'

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "==> rsync to ${REMOTE_HOST}:${REMOTE_DIR}"
ssh "${REMOTE_USER}@${REMOTE_HOST}" "mkdir -p ${REMOTE_DIR}"
rsync -avz --delete \
  --exclude node_modules --exclude dist --exclude .astro --exclude .git \
  "${SCRIPT_DIR}/" "${REMOTE_USER}@${REMOTE_HOST}:${REMOTE_DIR}/"

echo "==> docker build on ${REMOTE_HOST}"
ssh "${REMOTE_USER}@${REMOTE_HOST}" "cd ${REMOTE_DIR} && docker build -t ${IMAGE_NAME}:latest ."

echo "==> import into containerd (k8s.io namespace)"
ssh "${REMOTE_USER}@${REMOTE_HOST}" "docker save ${IMAGE_NAME}:latest -o /tmp/${IMAGE_NAME}.tar && sudo ctr -n k8s.io images import /tmp/${IMAGE_NAME}.tar && rm /tmp/${IMAGE_NAME}.tar"

echo "==> apply k8s manifests"
scp "${SCRIPT_DIR}/k8s/deployment.yaml" "${SCRIPT_DIR}/k8s/service.yaml" "${REMOTE_USER}@${REMOTE_HOST}:/tmp/" >/dev/null
ssh "${KCTL_HOST}" "${KCTL} apply -f /tmp/deployment.yaml -f /tmp/service.yaml" || {
  echo "(apply via .6 needs the manifests there — falling back to scp'ing to .6)"
  scp "${SCRIPT_DIR}/k8s/deployment.yaml" "${SCRIPT_DIR}/k8s/service.yaml" "${KCTL_HOST}:/tmp/" >/dev/null
  ssh "${KCTL_HOST}" "${KCTL} apply -f /tmp/deployment.yaml -f /tmp/service.yaml"
}

echo "==> rollout restart"
ssh "${KCTL_HOST}" "${KCTL} -n zaphod rollout restart deployment/tribunal-site"
ssh "${KCTL_HOST}" "${KCTL} -n zaphod rollout status deployment/tribunal-site --timeout=60s"

echo "==> service info"
ssh "${KCTL_HOST}" "${KCTL} -n zaphod get svc tribunal-site"

echo
echo "==> done. Add this to /etc/caddy/Caddyfile on zaphod (192.168.6.56) if not already:"
echo
echo "    tribunal.mabus.ai {"
echo "        reverse_proxy CLUSTER_IP:80"
echo "    }"
echo
echo "Replace CLUSTER_IP with the ClusterIP above, then 'sudo systemctl reload caddy'."

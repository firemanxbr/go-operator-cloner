#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMG="${IMG:-ghcr.io/firemanxbr/go-operator-cloner:dev}"
KIND_CLUSTER="${KIND_CLUSTER:-go-operator-cloner}"
KIND_CONFIG="${KIND_CONFIG:-${ROOT_DIR}/hack/kind/cluster.yaml}"

cleanup() {
  if [[ "${CLEANUP_KIND_CLUSTER:-false}" == "true" ]]; then
    kind delete cluster --name "${KIND_CLUSTER}"
  fi
}
trap cleanup EXIT

if ! kind get clusters | grep -qx "${KIND_CLUSTER}"; then
  kind create cluster --name "${KIND_CLUSTER}" --config "${KIND_CONFIG}"
fi

docker build -t "${IMG}" "${ROOT_DIR}"
kind load docker-image "${IMG}" --name "${KIND_CLUSTER}"

pushd "${ROOT_DIR}" >/dev/null
make install
make deploy IMG="${IMG}"
popd >/dev/null

kubectl wait deployment/go-operator-cloner-controller-manager \
  -n go-operator-cloner-system \
  --for=condition=Available \
  --timeout=180s

kubectl apply -f "${ROOT_DIR}/config/samples/source-namespace.yaml"
kubectl apply -f "${ROOT_DIR}/config/samples/source-secret.yaml"
kubectl apply -f "${ROOT_DIR}/config/samples/sync_v1alpha1_secretclone.yaml"
kubectl apply -f "${ROOT_DIR}/config/samples/target-namespace.yaml"

for _ in $(seq 1 30); do
  if kubectl get secret ghcr-pull-secret -n team-a >/dev/null 2>&1; then
    break
  fi
  sleep 2
done

kubectl get secret ghcr-pull-secret -n team-a -o jsonpath='{.type}' | grep -qx 'kubernetes.io/dockerconfigjson'

#!/bin/bash

export KUBECONFIG=~/.hpk/kubeconfig

####### Preamble ###############
# Ensure Testing Namespace
if [[ -z "${TEST_NAMESPACE}" ]]; then
  # Define namespace based on the current directory's name
  export TEST_NAMESPACE=${PWD##*/}
fi

# Ensure namespace exists without failing if it is already present.
kubectl create namespace "${TEST_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
################################

# Update Helm repo
helm repo add dask https://helm.dask.org
helm repo update

helm upgrade --install dask dask/dask \
  --namespace "${TEST_NAMESPACE}" \
  --values values.yaml

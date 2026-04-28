# go-operator-cloner

`go-operator-cloner` is a Kubebuilder-based Kubernetes Operator written in Go. It watches a cluster-scoped `SecretClone` resource and synchronizes one source Secret into matching namespaces, which is useful for distributing a GitHub Packages pull Secret to workloads that start in new namespaces.

This repo is meant to be both useful and educational. The operator is intentionally built with standard Kubebuilder and controller-runtime patterns so you can study real Go reconciliation code instead of a shortcut script.

## Operator Model

The controller watches a `SecretClone` CRD rather than hardcoding one source Secret in the binary. That gives you a better learning surface:

- You practice API design with Kubebuilder markers.
- You work with reconciliation, finalizers, status conditions, and secondary watches.
- You can extend the behavior later without changing the deployment model.

Example:

```yaml
apiVersion: sync.firemanxbr.dev/v1alpha1
kind: SecretClone
metadata:
  name: github-pull-secret
spec:
  sourceSecretRef:
    namespace: github-secrets
    name: github-pat
  targetSecretName: ghcr-pull-secret
  namespaceSelector:
    matchLabels:
      secret-sync.firemanxbr.dev/enabled: "true"
  excludedNamespaces:
    - kube-system
```

## Repository

Go module:

```sh
github.com/firemanxbr/go-operator-cloner
```

Git remote for pushes:

```sh
git@github.com:firemanxbr/go-operator-cloner.git
```

## Prerequisites

- Go `1.25.x`
- Docker
- `kubectl`
- `kind`

Kubebuilder was used as the base scaffold. The same install command from the Kubebuilder quick start works locally:

```sh
curl -L -o kubebuilder "https://go.kubebuilder.io/dl/latest/$(go env GOOS)/$(go env GOARCH)"
chmod +x kubebuilder
sudo mv kubebuilder /usr/local/bin/
```

## Local Development

Install dependencies, generate code, and run tests:

```sh
go mod tidy
make test
make lint
```

Build the manager binary:

```sh
make build
```

Run the controller against your current kubeconfig context:

```sh
make run
```

## kind Workflow

Create a local cluster:

```sh
kind create cluster --name go-operator-cloner --config hack/kind/cluster.yaml
```

Run the end-to-end smoke flow:

```sh
make test-e2e IMG=ghcr.io/firemanxbr/go-operator-cloner:dev
```

That flow builds the operator image, loads it into `kind`, deploys the controller, applies the sample manifests, and verifies that the cloned Secret appears in the target namespace.

## Sample Manifests

Useful files:

- `config/samples/source-namespace.yaml`
- `config/samples/source-secret.yaml`
- `config/samples/sync_v1alpha1_secretclone.yaml`
- `config/samples/target-namespace.yaml`
- `config/samples/demo-workload.yaml`

The sample source Secret uses fake `stringData` so you can safely test replication. For real GitHub Packages pulls, replace it with a valid `kubernetes.io/dockerconfigjson` Secret containing your GitHub PAT-based registry auth.

## Deploying

Build and push an image:

```sh
make docker-build docker-push IMG=ghcr.io/firemanxbr/go-operator-cloner:v0.1.0
```

Install the CRD:

```sh
make install
```

Deploy the controller:

```sh
make deploy IMG=ghcr.io/firemanxbr/go-operator-cloner:v0.1.0
```

Generate a single installer bundle:

```sh
make build-installer IMG=ghcr.io/firemanxbr/go-operator-cloner:v0.1.0
```

## CI

GitHub Actions includes:

- `CI`: unit tests, envtest-backed controller registration, generated-file drift checks, and a hard `100.0%` coverage gate for production packages.
- `Lint`: `golangci-lint`.
- `Security`: `govulncheck`, `gosec`, and Trivy filesystem scanning with SARIF upload.
- `Integration`: a `kind` deployment and replication smoke test.

## License

Copyright 2026.

Licensed under the Apache License, Version 2.0.

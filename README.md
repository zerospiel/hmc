# k0rdent Cluster Manager (KCM)

## Overview

k0rdent Cluster Manager is part of k0rdent which is focused
on delivering a open source approach to providing an enterprise grade
multi-cluster kubernetes management solution based entirely on standard open
source tooling that works across private or public clouds.

## Documentation

Detailed documentation is available in [k0rdent Docs](https://docs.k0rdent.io)

## Installation

### TL;DR

```bash
helm install kcm oci://ghcr.io/k0rdent/kcm/charts/kcm --version 1.2.0 -n kcm-system --create-namespace
```

Then follow the [Deploy a cluster deployment](#create-a-clusterdeployment) guide to
create a cluster deployment.

> [!NOTE]
> The KCM installation using Kubernetes manifests does not allow
> customization of the deployment. To apply a custom KCM configuration, install
> KCM using the Helm chart.

### Development guide

See [Install KCM for development purposes](docs/dev.md#kcm-installation-for-development).

### Software Prerequisites

KCM requires the following:

1. Existing management cluster (minimum required kubernetes version 1.28.0).
2. `kubectl` CLI installed locally.

Optionally, the following CLIs may be helpful:

1. `helm` (required only when installing KCM using `helm`).
2. `clusterctl` (to handle the lifecycle of the cluster deployments).

### Providers configuration

Full details on the provider configuration can be found in the k0rdent Docs,
see [Documentation](#documentation)

### k0rdent Installation

```bash
export KUBECONFIG=<path-to-management-kubeconfig>

helm install kcm oci://ghcr.io/k0rdent/kcm/charts/kcm --version <kcm-version> -n kcm-system --create-namespace
```

#### Extended Management configuration

By default, kcm is being deployed with the following
configuration:

```yaml
apiVersion: k0rdent.mirantis.com/v1beta1
kind: Management
metadata:
  name: kcm
spec:
  providers:
  - name: cluster-api-provider-k0sproject-k0smotron
  - name: cluster-api-provider-azure
  - name: cluster-api-provider-vsphere
  - name: cluster-api-provider-aws
  - name: cluster-api-provider-openstack
  - name: cluster-api-provider-docker
  - name: cluster-api-provider-gcp
  - name: cluster-api-provider-ipam
  - name: cluster-api-provider-infoblox
  - name: projectsveltos
  release: kcm-1-2-0
```

There are two options to override the default management configuration
of KCM:

1. Update the `Management` object after the KCM installation using `kubectl`:

    `kubectl --kubeconfig <path-to-management-kubeconfig> edit management`

2. Deploy KCM skipping the default `Management` object creation and provide your
own `Management` configuration:

   * Create `management.yaml` file and configure core components and providers.
   See [Management API](api/v1beta1/management_types.go).

   * Specify `--create-management=false` controller argument and install KCM:

    If installing using `helm` add the following parameter to the `helm install`
    command:

    `--set="controller.createManagement=false"`

   * Create `kcm` `Management` object after KCM installation:

    `kubectl --kubeconfig <path-to-management-kubeconfig> create -f management.yaml`

## Create a ClusterDeployment

To create a ClusterDeployment:

1. Create `Credential` object with all credentials required.

   See [Credential system docs](https://k0rdent.github.io/docs/credential/main)
   for more information regarding this object.

2. Select the `ClusterTemplate` you want to use for the deployment. To list all
   available templates, run:

```bash
export KUBECONFIG=<path-to-management-kubeconfig>

kubectl get clustertemplate -n kcm-system
```

If you want to deploy hosted control plane template, make sure to check
additional notes on Hosted control plane in k0rdent Docs, see
[Documentation](#documentation).

2. Create the file with the `ClusterDeployment` configuration:

> [!NOTE]
> Substitute the parameters enclosed in angle brackets with the corresponding
> values. Enable the `dryRun` flag if required.
> For details, see [Dryrun](#dry-run).

```yaml
apiVersion: k0rdent.mirantis.com/v1beta1
kind: ClusterDeployment
metadata:
  name: <cluster-name>
  namespace: <cluster-namespace>
spec:
  template: <template-name>
  credential: <credential-name>
  dryRun: <true/false>
  config:
    <cluster-configuration>
```

3. Create the `ClusterDeployment` object:

`kubectl create -f clusterdeployment.yaml`

4. Check the status of the newly created `ClusterDeployment` object:

`kubectl -n <clusterdeployment-namespace> get clusterdeployments <clusterdeployment-name> -o=yaml`

5. Wait for infrastructure to be provisioned and the cluster to be deployed (the
provisioning starts only when `spec.dryRun` is disabled):

```bash
kubectl -n <clusterdeployment-namespace> get cluster <clusterdeployment-name> -o=yaml
```

> [!NOTE]
> You may also watch the process with the `clusterctl describe` command
> (requires the `clusterctl` CLI to be installed): ``` clusterctl describe
> cluster <clusterdeployment-name> -n <clusterdeployment-namespace> --show-conditions
> all ```

6. Retrieve the `kubeconfig` of your cluster deployment:

```bash
kubectl get secret -n kcm-system <clusterdeployment-name>-kubeconfig -o go-template='{{.data.value|base64decode}}' > kubeconfig
```

### Dry run

KCM `ClusterDeployment` supports two modes: with and without (default) `dryRun`.

If no configuration (`spec.config`) provided, the `ClusterDeployment` object will
be populated with defaults (default configuration can be found in the
corresponding `Template` status) and automatically marked as `dryRun`.

After you adjust your configuration and ensure that it passes validation
(`TemplateReady` condition from `status.conditions`), remove the `spec.dryRun`
flag to proceed with the deployment.

## Cleanup

1. Remove the Management object:

> [!NOTE]
> Make sure you have no KCM `ClusterDeployment` objects left in the cluster prior to
> Management deletion

```bash
kubectl delete management.k0rdent kcm
```

2. Remove the `kcm` Helm release:

```bash
helm uninstall kcm -n kcm-system
```

3. Remove the `kcm-system` namespace:

```bash
kubectl delete ns kcm-system
```

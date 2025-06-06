apiVersion: k0rdent.mirantis.com/v1beta1
kind: ClusterDeployment
metadata:
  name: ${CLUSTER_DEPLOYMENT_NAME}
  namespace: ${NAMESPACE}
spec:
  template: ${CLUSTER_DEPLOYMENT_TEMPLATE}
  credential: gcp-credential
  config:
    project: ${GCP_PROJECT}
    region: ${GCP_REGION}
    network:
      name: default
    controlPlane:
      instanceType: n1-standard-2
      image: projects/ubuntu-os-cloud/global/images/ubuntu-2004-focal-v20250213
      publicIP: true
    controlPlaneNumber: 1
    worker:
      instanceType: n1-standard-2
      image: projects/ubuntu-os-cloud/global/images/ubuntu-2004-focal-v20250213
      publicIP: true
    workersNumber: 1

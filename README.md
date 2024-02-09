# k8s-oci-operator

Manage OCI Reserved IPs as Custom Resources in your Kubernetes cluster and assign them your pods or private IPs.

**Warning:** This project is still work in progress. There might be breaking API changes in the future. Use at your own risk.

## Requirements

* Your pod IPs must be allocated from your VCN subnets or the pod must be running in `hostNetwork`.
* Your worker nodes must reside in a public subnet.

## Installation

### Install the operator

Run:

```bash
$ kubectl apply -f config/crd/bases # install Custom Resource Definition (CRD) for ReservedIP Custom Resource
$ kubectl apply -f deploy/          # install the operator
```

## Usage

### ReservedIPs

#### Basic usage

##### Allocate a ReservedIP

Create a new file `example.yaml`:
```yaml
apiVersion: oci.k8s.logmein.com/v1alpha1
kind: ReservedIP
metadata:
  name: my-reserved-ip
spec:
  tags:
    owner: My team
```

Apply it:
```bash
$ kubectl apply -f example.yaml
ReservedIP.oci.k8s.logmein.com/my-reserved-ip created
```

Describe it:
```bash
$ kubectl get reservedip my-reserved-ip
NAME            STATE      PUBLIC IP       POD
my-reserved-ip  allocated  34.228.250.93
```

###### Using BYOIP and requesting a specific address

Request a random address from a BYOIP address pool:

```yaml
apiVersion: oci.k8s.logmein.com/v1alpha1
kind: ReservedIP
# ...
spec:
  publicIPv4Pool: <your pool ID here>
  # ...
```

Request a specific address from a BYOIP address pool:

```yaml
apiVersion: oci.k8s.logmein.com/v1alpha1
kind: ReservedIP
# ...
spec:
  publicIPv4Address: 12.34.56.78
  # ...
```

##### Assign the ReservedIP to a pod

Adjust `example.yaml` to include an `assignment` section:
```yaml
apiVersion: oci.k8s.logmein.com/v1alpha1
kind: ReservedIP
metadata:
  name: my-reserved-ip
spec:
  tags:
    owner: My team
  assignment:
    podName: some-pod
```

Apply it:
```bash
$ kubectl apply -f example.yaml
reservedip.oci.k8s.logmein.com/my-reserved-ip configured
```

Describe it:
```bash
$ kubectl get reservedip my-reserved-ip
NAME           STATE      PUBLIC IP       POD
my-reserved-ip assigned   34.228.250.93   my-pod
```

Allocating and assigning can also be done in one step.

##### Unassign an ReservedIP from a pod

Remove the `assignment` section again and reapply the manifest.

##### Release the ReservedIP

```bash
$ kubectl delete reservedip my-reserved-ip
ReservedIP.oci.k8s.logmein.com/my-reserved-ip deleted
```

Unassigning and releasing can also be done in one step.

#### One ReservedIP per pod in a deployment / statefulset

##### ReservedIP creation

You can use an `initContainer` as part of your pod definition to create the `ReservedIP` custom resource. This requires that your pod has RBAC permissions to create `ReservedIP` resources.

```yaml
apiVersion: v1
kind: ServiceAccount
metadata:
  name: reservedip-user
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ReservedIP-user-role
rules:
- apiGroups:
  - oci.k8s.logmein.com
  resources:
  - reservedips
  verbs:
  - '*'
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: reservedip-user-rolebinding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: reservedip-user-role
subjects:
- kind: ServiceAccount
  name: reservedip-user
---
apiVersion: apps/v1
kind: Deployment
# ...
spec:
  # ...
  template:
    spec:
      # ...
      serviceAccountName: reservedip-user
      initContainers:
      - name: init-reservedip
        image: <some image that has kubectl>
        env:
        - name: MY_POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        - name: MY_POD_NAMESPACE
          valueFrom:
            fieldRef:
              fieldPath: metadata.namespace
        command:
        - /bin/sh
        - -c
        - |
            # allocate and assign ReservedIP
            cat <<EOS | kubectl apply -f-
            apiVersion: oci.k8s.logmein.com/v1alpha1
            kind: ReservedIP
            metadata:
              name: $(MY_POD_NAME)
              namespace: $(MY_POD_NAMESPACE)
            spec:
              tags:
                owner: My team
                pod: $(MY_POD_NAME)
                namespace: $(MY_POD_NAMESPACE)
              assignment:
                podName: $(MY_POD_NAME)
            EOS

            # wait for ReservedIP to be assigned
            while [ "$(kubectl get reservedip $(MY_POD_NAME) -o jsonpath='{.status.state}')" != "assigned" ]
            do
              sleep 1
            done
```

##### Cleanup

You can ensure that an ReservedIP is released when your pod is terminated by including `ownerReferences` in your `ReservedIP` resource. Setting `blockOwnerDeletion: true` prevents the pod from vanishing until the ReservedIP is unassigned and released.

```yaml
apiVersion: oci.k8s.logmein.com/v1alpha1
kind: ReservedIP
metadata:
  name: my-reserved-ip
  ownerReferences:
  - apiVersion: v1
    kind: Pod
    name: some-pod
    uid: ... # put the UID of the pod here
    blockOwnerDeletion: true
spec:
  tags:
    owner: My team
  assignment:
    podName: some-pod
```

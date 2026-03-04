# k8s-mend

k8s-mend is a Kubebuilder-based self-healing Kubernetes operator. It introduces a `SelfHealingPolicy` CRD that lets platform users define:
- which resource kind to monitor (`Pod` or `Deployment`),
- unhealthy conditions and thresholds,
- what healing actions are allowed,
- whether AI should advise or enforce the selected action.

An optional companion AI service can be called by the controller to rank/choose the best action from the policy allow-list.

## How it works

1. The controller periodically evaluates each `SelfHealingPolicy`.
2. Matching resources are listed using namespace + label selector.
3. Rules are checked:
   - `RestartCount` for Pods
   - `UnavailableReplicas` for Deployments
4. The action is selected:
   - deterministic fallback: first `allowedActions` entry
   - optional AI decision from `ai.endpoint`
5. If not `dryRun`, healing is executed (delete, rollout restart, annotate).
6. Status is updated with last evaluation time, last action, reason, and healed count.



## Example policy

```yaml
apiVersion: reliability.platform.ai/v1alpha1
kind: SelfHealingPolicy
metadata:
  name: selfhealingpolicy-sample
  namespace: default
spec:
  target:
    apiVersion: v1
    kind: Pod
    namespace: default
    labelSelector:
      app: demo
  conditions:
    - type: RestartCount
      threshold: 3
      minAgeSeconds: 60
  allowedActions:
    - Delete
    - Annotate
  ai:
    enabled: true
    mode: advisory
    endpoint: http://ai-service:8081/evaluate
    timeoutSeconds: 5
  dryRun: true
```

## Getting Started

### Prerequisites
- go version v1.24.6+
- docker version 17.03+.
- kubectl version v1.11.3+.
- Access to a Kubernetes v1.11.3+ cluster.

### To Deploy on the cluster
**Build and push your image to the location specified by `IMG`:**

```sh
make docker-build docker-push IMG=<some-registry>/k8s-mend:tag
```

**Install the CRDs into the cluster:**

```sh
make install
```

**Deploy the Manager to the cluster with the image specified by `IMG`:**

```sh
make deploy IMG=<some-registry>/k8s-mend:tag
```

**Create instances of your solution**

```sh
kubectl apply -k config/samples/
```

### To Uninstall

```sh
kubectl delete -k config/samples/
make uninstall
make undeploy
```

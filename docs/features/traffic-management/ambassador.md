# Ambassador Edge Stack

Ambassador Edge Stack provides the functionality you need at the edge your Kubernetes cluster (hence, an "edge stack"). This includes an API gateway, ingress controller, load balancer, developer portal, canary traffic routing and more. It provides a group of CRDs that users can configure to enable different functionalities. 

Argo-Rollouts provides an integration that leverages Ambassador's [canary routing capability](https://www.getambassador.io/docs/pre-release/topics/using/canary/). This allows the traffic to your application to be gradually incremented while new versions are being deployed.

## How it works

Ambassador Edge Stack provides a resource called `Mapping` that is used to configure how to route traffic to services. Ambassador canary deployment is achieved by creating 2 mappings with the same URL prefix pointing to different services. Consider the following example:

```yaml
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: stable-mapping
spec:
  prefix: /someapp
  rewrite: /
  service: someapp-stable:80
---
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: canary-mapping
spec:
  prefix: /someapp
  rewrite: /
  service: someapp-canary:80
  weight: 30
```

In the example above we are configuring Ambassador to route 30% of the traffic coming from `<public ingress>/someapp` to the service `someapp-canary` and the rest of the traffic will go to the service `someapp-stable`. If users want to gradually increase the traffic to the canary service, they have to update the `canary-mapping` setting the weight to the desired value either manually or automating it somehow. 

With Argo-Rollouts there is no need to create the `canary-mapping`. The process of creating it and gradually update its weight is fully automated by the Argo-Rollouts controller. The following example shows how to configure the `Rollout` resource to use Ambassador as a traffic router for canary deployments:


```yaml
apiVersion: argoproj.io/v1alpha1
kind: Rollout
...
spec:
  strategy:
    canary:
      stableService: someapp-stable
      canaryService: someapp-canary
      trafficRouting:
        ambassador:
          apiVersion: getambassador.io/v2
          mappings:
            - stable-mapping
      steps:
      - setWeight: 30
      - pause: {duration: 60s}
      - setWeight: 60
      - pause: {duration: 60s}
```

Under `spec.strategy.canary.trafficRouting.ambassador` there are 2 possible attributes:

- `apiVersion`: Optional. If you are using an older version of Ambassador you can specify its `apiVersion` (e.g.: `getambassador.io/v1`). If not provided, Argo-Rollouts will use the default value as `getambassador.io/v2`
- `mappings`: Required. If your application exposes 2 different ports for different servers (e.g.: REST and gRPC) you can provide the stable mappings in this list and Argo-Rollouts will create the canary mappings for both of them. At least one mapping is necessary to be provided. If no mapping is provided Argo-Rollouts will send an error event and the rollout will be aborted. 

When Ambassador is configured in the `trafficRouting` attribute of the manifest, the Rollout controller will:
1. Create one canary mapping for each stable mapping provided in the Rollout manifest
1. Proceed with the steps according to the configuration updating the canary mapping weight
1. At the end of the process Argo-Rollout will delete all the canary mappings created

## Endpoint Resolver

Argo-Rollout will dynamically modify the `Service` selectors when the rollout starts and when it concludes in order to do version promotion. Ambassador mappings use service resolver by default to route traffic to Pods. However when the connection is opened, changes in the service selectors aren't directly reflected and traffic can be sent to old pods if they are still alive. For this reason, it is important to configure Ambassador mappings to use endpoint resolver to avoid this problem.

To configure Ambassador to use endpoint resolver it is necessary to apply the following resource in the cluster:

```yaml
apiVersion: getambassador.io/v2
kind: KubernetesEndpointResolver
metadata:
  name: endpoint
```

And then configure the mapping to use it:

```
apiVersion: getambassador.io/v2
kind:  Mapping
metadata:
  name: stable-mapping
spec:
  resolver: endpoint
  prefix: /someapp
  rewrite: /
  service: someapp-stable:80
```

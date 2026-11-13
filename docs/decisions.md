# Decisions

A stub. The reasoning behind each item is in `PLAN.md`; this lists what was
decided and where it overrides the plan.

1. **One shared PostgreSQL**, in the `kagent` namespace, holding kagent's and
   Substrate's schemas and roles, as kagent's own `kagent install` deploys it.
   Pod certificates (signer `postgres.podcert.ate.dev`), no passwords.
   `postgres:18-alpine` pinned by the digest kagent uses. Overrides the plan's
   two instances. Upstream's manifest does not bootstrap roles in the pod (the
   `ate-setup` CLI does); a native sidecar, `db-setup`, runs both `setup.sql`
   scripts instead of a post-install hook, which would deadlock with
   helm-controller's wait.
2. **Cluster label** `addons.kubernetes.vmware.com/kagent: enabled`.
3. **RustFS without plain-text credentials.** The substrate chart's RustFS is
   switched off and this chart renders its own, reading a Secret that the
   bootstrap Job generates. Every consumer of the keys can take a Secret.
4. **One umbrella chart, release `kagent`** in `ate-system` (the ACD default),
   with kagent in `kagent`. The CRDs of `substrate-crds` and `kagent-crds` are
   copied into `crds/`, not added as dependencies.
5. **The bootstrap Job writes only to the release namespace**, which the
   helm-controller creates, so it creates no namespace and needs no
   ClusterRole. The `kagent` namespace is a plain chart template.
6. **The SPIFFE trust domain stays `cluster.local`** whatever the cluster's DNS
   domain; only `global.clusterDomain` follows the Cluster resource.
7. **All kagent**, on kagent's Substrate fork v1.0.0, the pairing kagent v1.0.1
   tests. Revisit when upstream Substrate ships Helm charts (epic #3170).
8. **The egress gateway's extra CA is built declaratively, not by the bootstrap
   Job.** The gateway verifies upstream TLS only against its image's
   `ca-certificates.crt`, so the chart ships Mozilla's public roots
   (`chart/kagent/files/ca-roots.pem`, refreshed by `scripts/sync-ca-roots.sh`)
   and always renders them, plus `egressUpstreamTrust.caBundle` when set, into
   the ConfigMap `egress-ca-bundle`. A MutatingAdmissionPolicy and binding that
   mount it over the gateway's bundle come from the ACD as cluster-scoped
   outputs, not from the chart as Helm hooks: hooks are imperative and
   untracked (Helm leaves them behind on uninstall), while the addon manager
   applies and removes ACD outputs with the addon, before the HelmRelease runs.
   Because the ACD always emits the policy, the ConfigMap is always rendered,
   or the gateway pods would hang in ContainerCreating; without a private CA
   the gateway trusts the chart's roots instead of its image's. A Job that
   wrote the ConfigMap and deleted pods was imperative, so it was dropped. The
   cost is manual: the pods read the bundle only at start, so after changing the
   value run `kubectl -n ate-system delete pod -l app=atenet-egress`. The
   Deployment recreates the pods and the policy mounts the new bundle.
   Restarting the Deployment would edit its pod template, which Flux drift
   detection on the HelmRelease may revert, rolling the pods again.
   The shipped roots also go stale until the chart is released again. All of it
   goes when kagent-dev/substrate#101 ships (tracked in
   agent-substrate/substrate#2684).

# Models and agents

What is specific to this addon when you connect a model and run agents. For
kagent itself, see the [kagent project](https://github.com/kagent-dev/kagent). Examples:
[`examples/modelconfig.yaml`](../examples/modelconfig.yaml) and
[`examples/agent.yaml`](../examples/agent.yaml).

- **Use the `kagent` namespace.** kagent's worker pool `kagent-default` and the
  egress credential policy are set up for that namespace only. Put the
  ModelConfig, its API key Secret and the Agent there.
- **`modelProvider` is optional.** The AddonConfig value only fills in
  `default-model-config`, the ModelConfig kagent's built-in agents use. Your
  own agents can name their own ModelConfig, as in the examples.
- **The key never reaches the sandbox.** Agents call the model through
  Substrate's egress gateway, which injects the key from the ModelConfig's
  Secret.
- **The endpoint needs a DNS name.** The egress gateway does not connect to an
  IP address.
- **A private CA goes in `egressCABundle`.** Otherwise the egress gateway fails
  with `invalid peer certificate: UnknownIssuer`. See the
  [FAQ](faq.md#how-do-i-trust-a-private-ca-for-egress).

## Reaching the UI

The UI Service, `kagent-ui` in `kagent`, is a ClusterIP Service. Port-forward
it on port 8080, or make it a LoadBalancer in the AddonConfig:

```yaml
spec:
  values:
    helmValues:
      kagent:
        ui:
          service:
            type: LoadBalancer
```

kagent's server has no authentication by default. Anyone who reaches the UI can
run agents and use the model.

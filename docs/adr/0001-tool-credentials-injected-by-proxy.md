# Tool and shell credentials are injected by the proxy, not held by the agent

Status: accepted

The agent executes model output, so a secret that generated code can read is a secret one prompt
injection away from disclosure. The boundary is therefore drawn as an access rule rather than as a
rule about which process happens to hold what:

- Credentials for hosts the agent reaches through the shell are held by `q15-proxy` and injected
  into the request at egress, for the hosts the deployment's policy names. The sandbox that runs
  generated code carries a placeholder for each of them and no value, and the proxy substitutes the
  value only in a request that matches a rule. The sandbox can name a secret and cannot read it.
- The model provider's key is in the container that runs the agent loop, because that process is the
  one that calls the provider. Generated code never runs there: it runs in the sandbox, where that
  key does not exist. No tool route reaches it.

Two things the boundary does not claim:

- The agent can still use an injected credential, by asking for a host the policy matches. There is
  nothing to steal and plenty to abuse, so the policy is the boundary and it has to be narrow.
- A hosted provider sees the prompt either way. This boundary is about what the agent can reach, not
  about what leaves the machine.

## Considered options

- Hand the agent the tokens and rely on the prompt. Simpler, one less hop, and what most agent
  runtimes do. It turns the model context and the transcript into a credential store.
- Inject from a library inside the agent process. Same trust boundary as handing them over, and it
  needs a container bindings import that the module does not take (see
  [ADR 0003](0003-cli-lives-in-the-agent-module.md)).

## Consequences

- A new integration needs a policy rule, not a key in the agent's configuration, so the policy file
  is a security-relevant artefact rather than a convenience.
- The proxy is on the critical path for any request that needs a credential.
- Credentials are provisioned per deployment and are never read back: the operator-facing secret
  verbs are write-only.
- The boundary is not network isolation. The sandbox is a peer on the stack's network and can
  resolve the other services. What holds is that the container running the agent loop accepts no
  connection from it.

Evidence: [docs/architecture.md](../architecture.md), `deploy/compose/proxy-policy.yaml`,
[#215](https://github.com/q15co/q15/issues/215).

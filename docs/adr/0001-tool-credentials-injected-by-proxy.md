# Tool and shell credentials are injected by the proxy, not held by the agent

Status: accepted

The agent executes model output, so anything in its own environment is one prompt injection away
from disclosure. Credentials for hosts the agent reaches through the shell are therefore held by
`q15-proxy`, which injects them into the request at egress for the hosts the deployment's policy
names. The agent's environment does not carry them, so neither its prompt nor its transcript has
anything to leak for those hosts.

Both limits are load-bearing and are stated rather than hidden:

- The agent does hold the key for the model provider it calls, because it is the process that calls
  the provider. A hosted provider sees the prompt either way.
- The agent can still use an injected credential, by asking for a host the policy matches. There is
  nothing to steal and plenty to abuse, so the policy is the boundary and it has to be narrow.

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

Evidence: [docs/architecture.md](../architecture.md), `deploy/compose/proxy-policy.yaml`,
[#215](https://github.com/q15co/q15/issues/215).

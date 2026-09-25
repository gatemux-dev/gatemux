# vLLM

GateMux connects to vLLM through its OpenAI-compatible API. Start vLLM with
`vllm serve <model> --served-model-name my-model`, then use
[the example configuration](../examples/vllm.yaml) as a starting point.

Set `base_url` to the vLLM server's reachable URL, including `/v1`. The example
uses `http://localhost:8000/v1`; when GateMux runs in a container, use a hostname
or address reachable from that container instead of assuming its `localhost` is
the vLLM host.

GateMux sends `upstream_model` as the OpenAI-compatible request's `model` value.
It must match a name accepted by vLLM: `--served-model-name` if specified, or the
model argument passed to `vllm serve` otherwise.

An API key is optional when the vLLM server does not require one. GateMux's
configuration still requires an `api_key_env` field; for an unauthenticated vLLM
server, the example names `VLLM_API_KEY` but that environment variable can remain
unset. If vLLM is configured with `--api-key`, set the same key in that
environment variable. Restrict network access to the vLLM server: vLLM documents
that its API-key option does not authenticate every server endpoint ([security
limitations](https://docs.vllm.ai/en/latest/usage/security/#api-key-authentication-limitations)).

This example is configuration-only; it does not require a GPU or connect to a
running vLLM server during GateMux's config tests.

# SMR cost, privacy and routing behavior

SMR is an Apache-2.0 local LLM router. Its classifier is a paid ACME AI service. This guide explains the two costs and the data flow so you can decide whether it fits your coding workflow.

[Quick start](QUICKSTART.md) · [English README](../README.md) · [Tiếng Việt](../README.vi.md)

## Two separate bills

| Cost | Who bills it | What it covers |
|---|---|---|
| Classification | ACME AI, from prepaid VND balance | A difficulty tier for the representative text |
| Model usage | Your configured provider | Input, output and any other provider-billed usage |

The open-source client license does not make either service free. You supply both an ACME AI key and your own provider keys. SMR does not use a ChatGPT Plus session or Claude Pro OAuth session.

Classification charges appear in response headers and on the [dashboard](https://llm.acmeai.vn/). A server-side cache hit costs 10% of the original classification charge, rounded up with a minimum of 1 VND. The cache window is five minutes. See [billing behavior](../README.md#billing-behavior) for top-up details.

## Measure total cost

Compare a representative workload with and without routing, using current prices from your own providers:

```text
Baseline total = provider charges for the baseline workload
Routed total   = provider charges across all tiers + classification charges
Difference    = baseline total - routed total
```

Keep the currency consistent. Include input, output, cached tokens, retries and other billed usage where applicable. Count failed attempts and corrective follow-ups rather than comparing only the first successful response.

Record output quality and completion time alongside spend. A cheaper first call that needs several repairs may cost more overall. Repeat the same quality checks on both workflows, and keep any changes in model, context or task mix visible in the comparison.

The four tiers are `easy`, `lower_medium`, `upper_medium` and `hard`. You choose every model mapping. Routing does not guarantee lower cost or equivalent quality, and the README's arithmetic example is not a benchmark.

## What leaves your machine

| Destination | Data sent |
|---|---|
| ACME AI classifier | Representative text and host name, authenticated with the ACME AI key |
| Your selected model provider | Model request and the provider authentication configured for that tier |

The representative text is the last user text, or the last assistant text when user text is absent. Plain-string Responses input is used directly. Text is truncated to `max_chars`, 12,000 characters by default.

SMR does not select system messages or tool-result blocks as classifier text. However, secrets or code pasted into a user message can be part of that text. This is a cloud classification step; it is not an offline-only workflow.

The provider keys are stored locally and sent to their configured provider, never to ACME AI. The full conversation is not submitted to the classification endpoint. The selected model provider receives the model request, with the `model` field changed to your configured tier model.

The server's documented storage policy keeps a hash, tier, token count, price and timestamps rather than raw text. Consult the [service privacy policy](https://llm.acmeai.vn/privacy) for service-side processing terms. The public client source demonstrates its outbound payload; it cannot independently prove server-side retention.

## Failure and fallback behavior

An insufficient-balance response from the classifier stops the request with HTTP 402 before a provider model is called.

The default configuration uses `upper_medium` as the fallback tier for eligible transient classifier errors. Empty extracted text also uses the fallback tier. This can still incur provider usage. Check `X-SMR-Classifier` to distinguish `fallback` from a normal classification, and review `on_transient_error` in [the configuration example](../config.example.yaml).

## Inspect the implementation

- [Text extraction and truncation](../internal/state/state.go)
- [Classification request](../internal/classifier/client.go)
- [Model selection, fallback and provider forwarding](../internal/proxy/proxy.go)
- [Routing tests](../internal/proxy/proxy_test.go)

For setup and troubleshooting, continue with the [quick start](QUICKSTART.md).

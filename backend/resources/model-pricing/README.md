# Model Pricing Data

This directory contains a local copy of the mirrored model pricing data as a fallback mechanism.

## Source
The original file is maintained by the LiteLLM project and mirrored into the `price-mirror` branch of this repository via GitHub Actions:
- Mirror branch (configurable via `PRICE_MIRROR_REPO`): https://raw.githubusercontent.com/<your-repo>/price-mirror/model_prices_and_context_window.json
- Upstream source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

## Purpose

DeepSeek entries use this site's credit convention as of 2026-10-16: ¥1 purchases
$1 of credit, so official CNY price numbers are stored as dollar-denominated credit
rates without currency conversion. Off-peak input/output/cache-read rates per
million tokens are $1/$4/$0.02 for Flash and $4.5/$13.5/$0.15 for Pro.
The runtime pricing policy also applies these rates when the remote USD catalog
is loaded. Existing daily peak windows (09:00–12:00 and 14:00–18:00 Beijing time,
including weekends and holidays) still charge 2× these rates. Group and user
multipliers are unchanged and continue to apply separately.

Explicit snapshots have separate Bailian CNY cards (input/output/cache read per
million tokens): `deepseek-v4-flash-0731` is $1.5/$4.5/$0.3 and
`deepseek-v4-pro-0813` is $4.5/$13.5/$0.9 off-peak. The cache prices use Bailian's
20% input-price rule. Sources: [model pricing](https://help.aliyun.com/zh/model-studio/model-pricing)
and [context cache](https://help.aliyun.com/zh/model-studio/context-cache).
Unversioned aliases keep the direct API defaults above because production mixes
official and reseller routes under these names. In particular, do not globally
apply Bailian's legacy `deepseek-v4-pro` price to the direct API alias. Explicit
snapshot cards do not change account model mappings or group/user multipliers.

This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process

Gemini text-model additions were checked against Google's official model and
[pricing documentation](https://ai.google.dev/gemini-api/docs/pricing) on 2026-10-15.
The bundled catalog and billing fallbacks include Gemini 4 Argon ($2 input / $10
output / $0.10 cached input per million tokens), Gemini 3.8/3.7/3.6 Flash
($0.75/$3.75/$0.075), and Gemini 3.5 Flash-Lite ($0.30/$2.50/$0.03).
Gemini 3.7 is retained only as a billing compatibility entry: Google deprecated
it and automatically routes its API requests to 3.8. It is not added to the
recommended API model presets.
These use USD rates, independently of the Kimi/DeepSeek credit convention above.
Argon's introductory rates end on 2026-11-30 and double on 2026-12-01; the Flash
promotional rates double on 2027-01-01. Refresh both the bundled catalog and
hard-coded billing fallbacks at those dates if remote pricing is unavailable.

Argon supports implicit cache reads, not explicit cache creation. Its 1M-token
shared context is not a single GenerateContent response limit: the client preset
uses the conservative 32K non-streaming limit. Automatic continuation for long
decode is not implemented here. Native GenerateContent and streaming requests
reuse the existing forwarding path. API catalog entries do not grant upstream
access; existing account/group allowlists still apply. Legacy Google One OAuth
and the Antigravity default catalog are not expanded to claim Argon availability.

The pricingService will:
1. First attempt to download the latest version from GitHub
2. If download fails, use this local copy as fallback
3. Log a warning when using the fallback file

## Manual Update
To manually update this file with the latest pricing data (if automation is unavailable):
```bash
curl -s https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json -o model_prices_and_context_window.json
```

## File Format
The file contains JSON data with model pricing information including:
- Model names and identifiers
- Input/output token costs
- Context window sizes
- Model capabilities

Last updated: 2025-08-10

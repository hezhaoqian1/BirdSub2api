# Model Pricing Data

This directory contains a local copy of the mirrored model pricing data as a fallback mechanism.

## Source
The original file is maintained by the LiteLLM project and mirrored into the `price-mirror` branch of this repository via GitHub Actions:
- Mirror branch (configurable via `PRICE_MIRROR_REPO`): https://raw.githubusercontent.com/<your-repo>/price-mirror/model_prices_and_context_window.json
- Upstream source: https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json

## Purpose

DeepSeek entries use this site's credit convention as of 2026-10-15: ¥1 purchases
$1 of credit, so official CNY price numbers are stored as dollar-denominated credit
rates without currency conversion. Off-peak input/output/cache-read rates per
million tokens are $1/$4/$0.02 for Flash and $4.5/$13.5/$0.15 for Pro.
The runtime pricing policy also applies these rates when the remote USD catalog
is loaded. Existing daily peak windows (09:00–12:00 and 14:00–18:00 Beijing time,
including weekends and holidays) still charge 2× these rates. Group and user
multipliers are unchanged and continue to apply separately.

This local copy serves as a fallback when the remote file cannot be downloaded due to:
- Network restrictions
- Firewall rules
- DNS resolution issues
- GitHub being blocked in certain regions
- Docker container network limitations

## Update Process
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

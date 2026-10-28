# 智谱 GLM / MiniMax 站内额度定价

本站充值规则为 **¥1 人民币购买 $1 额度**。智谱 GLM 和 MiniMax 默认价卡使用国内官网
人民币定价的数值，继续以美元额度（$）显示和扣费，不按外汇汇率折算，口径与
[Kimi](KIMI_PRICING.md)、DeepSeek 一致。此前这两家的默认价卡用的是国际站美元价
（z.ai / platform.minimax.io），2026-10-29 起改为人民币数值。

## 智谱 GLM

价格核对于 2026-10-29，来源：[智谱开放文档 API 定价](https://docs.bigmodel.cn/cn/guide/start/pricing)；
glm-5.3-prime 来自[阿里云百炼模型价格](https://help.aliyun.com/zh/model-studio/model-pricing)（华北2）。

| 模型 | 输入 / 百万 tokens | 输出 / 百万 tokens | 缓存命中 / 百万 tokens |
| --- | ---: | ---: | ---: |
| glm-5.3、glm-5.2 | $8 | $28 | $2 |
| glm-5.3-prime | $16 | $56 | $4 |
| glm-5.3-flashx（含 -legal） | $2 | $7 | $0.57 |
| glm-5.3-flash | $0.8 | $2.8 | $0.23 |
| glm-4.7-flashx | $0.5 | $3 | $0.1 |
| glm-4.7-flash、glm-4.5-flash | 免费 | 免费 | 免费 |

以下型号按输入长度分档，输入（含缓存命中）达到 32,000 tokens 后整次请求按长上下文价计费：

| 模型 | 输入 <32K（输入 / 输出 / 缓存） | 输入 ≥32K（输入 / 输出） |
| --- | ---: | ---: |
| glm-5.1 | $6 / $24 / $1.3 | $8 / $28 |
| glm-5-turbo | $5 / $22 / $1.2 | $7 / $26 |
| glm-5 | $4 / $18 / $1 | $6 / $22 |
| glm-4.7、glm-4.6、glm-4.5 | $3 / $14 / $0.6 | $4 / $16 |
| glm-4.5-air | $0.8 / $6 / $0.16 | $1.2 / $8 |

说明：

- 长上下文档的缓存命中价按输入倍率推算。glm-5.1（推算 $1.73，官方 $2）和 glm-5-turbo
  （推算 $1.68，官方 $1.8）略低于官方，其余与官方一致。
- glm-4.7 和 glm-4.5-air 的 <32K 档官方还按输出长度细分（输出 <200 tokens 更便宜），
  这里取输出 ≥200 tokens 的常见档。
- glm-4.6、glm-4.5 已不在官方价目页。它们在国际站与 glm-4.7 同价，官方建议迁移到
  glm-4.7，所以按 glm-4.7 计价。
- glm-4.5-x、glm-4.5-airx、glm-4-32b-0414-128k 官方已无人民币价，暂沿用原国际站数值，
  不受本次调整影响。
- 缓存存储官方限时免费，不单独计费。

## MiniMax

价格核对于 2026-10-29，来源：[MiniMax 按量计费](https://platform.minimax.cn/docs/guides/pricing-paygo)。
M2.x 已不在该页面，按官方人民币价目的[文档镜像](https://github.com/hkai-ai/LLM_OFFICIAL_DOCUMENTATION/blob/main/minimax/pricing.md)计。

| 模型 | 输入 / 百万 tokens | 输出 / 百万 tokens | 缓存读取 / 百万 tokens | 缓存写入 / 百万 tokens |
| --- | ---: | ---: | ---: | ---: |
| minimax-m3.1-flash | $1 | $4 | $0.1 | — |
| minimax-m3（输入 ≤512K） | $2.1 | $8.4 | $0.42 | — |
| minimax-m2.7 | $2.1 | $8.4 | $0.42 | $2.625 |
| minimax-m2.7-highspeed | $4.2 | $16.8 | $0.42 | $2.625 |
| minimax-m2.5、minimax-m2.1、minimax-m2 | $2.1 | $8.4 | $0.21 | $2.625 |
| minimax-m2.5-highspeed | $4.2 | $16.8 | $0.21 | $2.625 |

说明：

- M3 为官方永久五折后的价格。输入超过 512,000 tokens 时整次按 2 倍计。
- M3 的 `service_tier=priority` 档按官方 1.5 倍计：$3.15 / $12.6 / $0.63。
- 官方未公布 M3 和 M3.1-Flash 的缓存写入价，暂不单独计费。
- minimax-m2.1、minimax-m2 已不在官方价目页。它们在国际站与 M2.5 同价，所以按 M2.5 计价。

## 生效范围

默认展示和扣费共用 `glmDefaultPricing` / `minimaxDefaultPricing`。远端价格目录及本地目录
覆盖文件中的国际美元价格不会覆盖这些站内默认价。分组和渠道的自定义价格仍然优先，
现有倍率也照常生效。

只设置了部分价格的渠道（例如只填了输入和输出），未设置的项会继承上表的人民币数值。
长上下文分档（GLM ≥32K、MiniMax M3 >512K）只在分组开启「长上下文计费」时生效，
该开关默认开启。开启时，渠道价同样会按上表倍率进入长上下文档。

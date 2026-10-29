# Qwen / 小米 MiMo / 腾讯混元 站内额度定价

本站充值规则为 **¥1 人民币购买 $1 额度**。以下默认价卡使用国内官网人民币定价的数值，
以美元额度（$）显示和扣费，不按汇率折算，口径与 [GLM / MiniMax](GLM_MINIMAX_PRICING.md)、
[Kimi](KIMI_PRICING.md)、DeepSeek 一致。远端 LiteLLM 目录里的美元价不会覆盖这些价卡；
分组 / 渠道配置的自定义价格仍然优先。2026-10-29 起生效，此前这些型号没有价卡。

## 阿里 Qwen（百炼，中国内地·华北2（北京））

来源：[阿里云百炼模型价格](https://help.aliyun.com/zh/model-studio/model-pricing)、
[上下文缓存](https://help.aliyun.com/zh/model-studio/context-cache)。
隐式缓存命中按输入价 20%，显式缓存创建按 125%；Qwen3.8 的命中价为官方单列数值。

| 模型 | 输入 | 输出 | 缓存命中 | 缓存创建 |
| --- | ---: | ---: | ---: | ---: |
| qwen3.8-max（含 -0902） | $12 | $36 | $1.5 | $15 |
| qwen3.8-flash | $0.8 | $2.7 | $0.1 | 不单独计费（官方未公布） |
| qwen3.7-max（含日期快照、preview） | $12 | $36 | $2.4 | $15 |

以下型号按输入长度分档，输入（含缓存）超过 256,000 tokens 时整次请求按长上下文价：

| 模型 | ≤256K（输入 / 输出 / 缓存命中 / 缓存创建） | >256K（输入 / 输出） |
| --- | ---: | ---: |
| qwen3.7-plus | $2 / $8 / $0.4 / $2.5 | $6 / $24 |
| qwen3.6-plus | $2 / $12 / $0.2 / $2.5 | $8 / $48 |

- qwen3.7-plus 取页面原价，官方限时 8 折不计入。
- 北京地域 qwen3.6-plus 只支持显式缓存，命中按 10% 计。
- 三档以上分档的型号（qwen3.5-*、qwen-plus、qwen-flash、qwen3.7-flash、coder / vl 等）没有默认价卡，
  需要时请在分组或渠道里配置价格。

## 小米 MiMo

来源：[MiMo 按量计费](https://mimo.mi.com/docs/zh-CN/price/pay-as-you-go)（页面更新于 2026-10-21），实时推理价。
缓存写入官方限时免费，不单独计费。MiMo-V2.5 系列已于 2026-10-21 下线。

| 模型 | 输入 | 输出 | 缓存命中 |
| --- | ---: | ---: | ---: |
| mimo-v2.6-pro | $3 | $6 | $0.025 |
| mimo-v2.6-flash | $1 | $2 | $0.02 |
| mimo-v2.6-pro-ultraspeed | $30 | $60 | $0.25 |

## 腾讯混元

来源：腾讯云 TokenHub（价格页更新于 2026-08-28）。

| 模型 | 输入 | 输出 | 缓存命中 |
| --- | ---: | ---: | ---: |
| hy4-preview | $6 | $18 | $0.3 |

单位均为每百万 tokens。代码位置：`backend/internal/service/qwen_pricing.go`、
`mimo_pricing.go`、`hunyuan_pricing.go`。

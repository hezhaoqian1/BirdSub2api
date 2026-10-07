export default {
  profit: {
    title: '利润统计',
    description: '收入 = 用户实际扣费；成本 = 官方价 × 上游成本倍率（按 ¥1 = $1 额度计）；毛利 = 收入 − 成本。',
    excludeAdmin: '排除管理员自用',
    loadFailed: '加载利润统计失败',
    cards: {
      revenue: '收入',
      cost: '上游成本',
      profit: '毛利',
      margin: '毛利率',
      requests: '{count} 次请求',
      standardCost: '官方价 ${value}',
      subscription: '订阅号收入（不计成本）',
      subscriptionHint: 'OAuth 订阅号按月付费，暂不计入利润'
    },
    unconfiguredWarning: '有 ${revenue}（占收入 {percent}）来自成本倍率仍为默认 1 的账号，这部分成本按官方原价计算，利润会被低估。请在「按上游账号」中填写成本倍率。',
    trend: {
      title: '每日收入 / 成本 / 毛利'
    },
    tabs: {
      group: '按分组',
      account: '按上游账号',
      model: '按模型',
      user: '按用户'
    },
    columns: {
      name: '名称',
      requests: '请求',
      standardCost: '官方价',
      revenue: '收入',
      cost: '成本',
      profit: '毛利',
      margin: '毛利率',
      costRate: '成本倍率',
      probeRate: '探测倍率',
      status: '状态'
    },
    status: {
      configured: '已配置',
      unconfigured: '未配置',
      subscription: '订阅号'
    },
    deleted: '已删除',
    rateSynced: '由上游探测自动同步',
    useProbeRate: '用探测值',
    saveRate: '保存',
    rateSaved: '成本倍率已更新，只影响之后的请求',
    rateInvalid: '请输入不小于 0 的倍率',
    empty: '该时间范围内没有数据',
    backfill: {
      title: '历史成本回填（一次性）',
      notApplied: '修改成本倍率只影响之后的请求。之前的记录仍按倍率 1 计成本；配置好各账号倍率后，可从指定日期起一次性按当前倍率回填历史成本。',
      applied: '已于 {appliedAt} 回填 {start} 起的 {rows} 条记录。之后修改倍率只影响新请求。',
      startDate: '回填起始日期',
      preview: '预览回填',
      previewTitle: '回填预览',
      previewHint: '将把 {start} 至今、仍按倍率 1 记录的按量账号请求改为账号当前成本倍率。订阅号与倍率为 1 的账号不受影响。只能执行一次。',
      noCandidates: '没有需要回填的记录：请先在「按上游账号」中填写成本倍率。',
      account: '账号',
      rate: '当前倍率',
      rows: '记录数',
      oldCost: '回填前成本',
      newCost: '回填后成本',
      confirm: '确认回填所选账号',
      success: '已回填 {rows} 条记录',
      failed: '回填失败'
    }
  }
}

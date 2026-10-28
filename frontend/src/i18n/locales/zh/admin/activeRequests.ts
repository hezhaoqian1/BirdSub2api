export default {
  activeRequests: {
    title: '实时并发',
    open: '查看正在使用并发的请求',
    concurrency: '并发',
    waiting: '排队',
    unlimited: '不限',
    byGroup: '按分组',
    byUser: '按用户',
    refresh: '刷新',
    loadFailed: '加载实时并发失败',
    staleData: '刷新失败，下面是上一次成功获取的数据。',
    empty: '当前没有正在进行的请求',
    columns: {
      user: '用户 / Key',
      key: 'Key',
      group: '分组',
      model: '模型',
      account: '账号',
      elapsed: '已运行'
    },
    noGroup: '无分组',
    noAccount: '未关联',
    noAccountHint: '还没有占到账号槽位：可能仍在选号或排队，也可能所用账号不限并发（不占槽位）。',
    unrecorded: '未记录详情',
    liveSession: 'Live 会话',
    unrecordedHint: '"未记录详情"的请求没有占用者信息：可能在本功能上线前就已开始、详情记录开关已关闭，或详情写入失败。',
    requestId: '请求 ID：{id}',
    accountSubtitle: '账号 #{id}',
    userSubtitle: '用户 #{id}'
  }
}

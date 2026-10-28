export default {
  activeRequests: {
    title: 'Live Concurrency',
    open: 'View requests holding concurrency',
    concurrency: 'Concurrency',
    waiting: 'Queued',
    unlimited: 'Unlimited',
    byGroup: 'By group',
    byUser: 'By user',
    refresh: 'Refresh',
    loadFailed: 'Failed to load live concurrency',
    staleData: 'Refresh failed. Showing the last data that loaded successfully.',
    empty: 'No requests in progress',
    columns: {
      user: 'User / Key',
      key: 'Key',
      group: 'Group',
      model: 'Model',
      account: 'Account',
      elapsed: 'Running'
    },
    noGroup: 'No group',
    noAccount: 'Not linked',
    noAccountHint: 'No account slot held yet: the request may still be selecting or queuing for an account, or the account has unlimited concurrency (it holds no slot).',
    unrecorded: 'No details',
    liveSession: 'Live session',
    unrecordedHint: '"No details" rows have no holder info: they started before this feature was deployed, holder details are turned off, or the details failed to save.',
    requestId: 'Request ID: {id}',
    accountSubtitle: 'Account #{id}',
    userSubtitle: 'User #{id}'
  }
}

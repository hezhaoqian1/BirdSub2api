export default {
  profit: {
    title: 'Profit',
    description: 'Revenue = what users were charged; cost = list price × upstream cost rate; profit = revenue − cost.',
    excludeAdmin: 'Exclude admin usage',
    loadFailed: 'Failed to load profit statistics',
    cards: {
      revenue: 'Revenue',
      cost: 'Upstream cost',
      profit: 'Gross profit',
      margin: 'Margin',
      requests: '{count} requests',
      standardCost: 'List price ${value}',
      subscription: 'Subscription revenue (cost not counted)',
      subscriptionHint: 'OAuth subscription accounts are paid monthly and excluded from profit for now'
    },
    unconfiguredWarning: '${revenue} ({percent} of revenue) comes from accounts whose cost rate is still the default 1, so their cost is counted at list price and profit is understated. Set the cost rate under "By upstream account".',
    trend: {
      title: 'Daily revenue / cost / profit'
    },
    tabs: {
      group: 'By group',
      account: 'By upstream account',
      model: 'By model',
      user: 'By user'
    },
    columns: {
      name: 'Name',
      requests: 'Requests',
      standardCost: 'List price',
      revenue: 'Revenue',
      cost: 'Cost',
      profit: 'Profit',
      margin: 'Margin',
      costRate: 'Cost rate',
      probeRate: 'Probed rate',
      status: 'Status'
    },
    status: {
      configured: 'Configured',
      unconfigured: 'Not set',
      subscription: 'Subscription'
    },
    deleted: 'Deleted',
    rateSynced: 'Synced automatically from the upstream probe',
    useProbeRate: 'Use probed',
    saveRate: 'Save',
    rateSaved: 'Cost rate updated; it only applies to later requests',
    rateInvalid: 'Enter a rate of 0 or more',
    empty: 'No data in this range',
    backfill: {
      title: 'Historical cost backfill (one-time)',
      notApplied: 'Changing a cost rate only affects later requests. Earlier records are still costed at rate 1; once account rates are set, you can backfill historical cost once from a chosen date using the current rates.',
      applied: 'Backfilled {rows} records from {start} on {appliedAt}. Later rate changes only affect new requests.',
      startDate: 'Backfill from',
      preview: 'Preview backfill',
      previewTitle: 'Backfill preview',
      previewHint: 'Requests on metered accounts from {start} until now that were recorded at rate 1 will be rewritten with the account’s current cost rate. Subscription accounts and accounts at rate 1 are untouched. This can only run once.',
      noCandidates: 'Nothing to backfill: set account cost rates under "By upstream account" first.',
      account: 'Account',
      rate: 'Current rate',
      rows: 'Records',
      oldCost: 'Cost before',
      newCost: 'Cost after',
      confirm: 'Backfill selected accounts',
      success: 'Backfilled {rows} records',
      failed: 'Backfill failed'
    }
  }
}

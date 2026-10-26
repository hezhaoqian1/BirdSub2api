import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import UserConcurrencyCell from '../UserConcurrencyCell.vue'

describe('UserConcurrencyCell', () => {
  it('renders a plain badge by default', async () => {
    const w = mount(UserConcurrencyCell, { props: { current: 0, max: 5 } })
    expect(w.find('button').exists()).toBe(false)
    await w.find('span').trigger('click')
    expect(w.emitted('click')).toBeUndefined()
  })

  it('emits click when clickable', async () => {
    const w = mount(UserConcurrencyCell, { props: { current: 2, max: 5, clickable: true, title: 'open' } })
    const button = w.get('button')
    expect(button.attributes('title')).toBe('open')
    expect(button.text()).toContain('2')
    await button.trigger('click')
    expect(w.emitted('click')).toHaveLength(1)
  })
})

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { mount, RouterLinkStub } from '@vue/test-utils'

import HomeView from '../HomeView.vue'

const { appStore, authStore } = vi.hoisted(() => ({
  appStore: {
    cachedPublicSettings: {} as Record<string, unknown>,
    siteName: 'Fallback site',
    siteLogo: '',
    docUrl: '',
    publicSettingsLoaded: true,
    fetchPublicSettings: vi.fn(),
  },
  authStore: {
    isAuthenticated: false,
    isAdmin: false,
    user: null as { email?: string } | null,
    checkAuth: vi.fn(),
  },
}))

vi.mock('@/stores', () => ({
  useAppStore: () => appStore,
  useAuthStore: () => authStore,
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => appStore,
}))

vi.mock('vue-i18n', async (importOriginal) => {
  const actual = await importOriginal<typeof import('vue-i18n')>()
  const translations: Record<string, string> = {
    'home.brandRoute.headlineLead': '所有模型，',
    'home.brandRoute.headlineTail': '只走一条航线。',
    'home.brandRoute.value1Eyebrow': '01 / CONNECT ONCE',
    'home.brandRoute.value2Eyebrow': '02 / STAY ONLINE',
    'home.brandRoute.value3Eyebrow': '03 / COUNT EVERY TOKEN',
    'home.brandRoute.registerAndStart': '注册并开始',
    'home.brandRoute.loginAndStart': '登录并开始',
    'home.goToDashboard': '进入控制台',
  }
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => translations[key] ?? key }),
  }
})

function mountHome(settings: Record<string, unknown> = {}) {
  appStore.cachedPublicSettings = {
    site_name: 'Test site',
    site_subtitle: 'Test subtitle',
    ...settings,
  }

  return mount(HomeView, {
    global: {
      stubs: {
        RouterLink: RouterLinkStub,
        LocaleSwitcher: { template: '<div data-testid="locale-switcher" />' },
        Icon: { template: '<span data-testid="icon" />' },
      },
    },
  })
}

function compactDestination(wrapper: ReturnType<typeof mountHome>) {
  return wrapper.get('[data-testid="compact-home"]').findComponent(RouterLinkStub).props('to')
}

function modelPlazaDestination(wrapper: ReturnType<typeof mountHome>) {
  return wrapper
    .findAllComponents(RouterLinkStub)
    .find((link) => link.props('to') === '/model-plaza')
    ?.props('to')
}

describe('HomeView compact mode', () => {
  beforeEach(() => {
    authStore.isAuthenticated = false
    authStore.isAdmin = false
    authStore.user = null
    authStore.checkAuth.mockClear()
    appStore.fetchPublicSettings.mockClear()
    localStorage.clear()
    vi.spyOn(window, 'matchMedia').mockReturnValue({ matches: false } as MediaQueryList)
  })

  it('renders custom HTML ahead of compact mode', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      home_content: '<section id="custom-home">Custom home</section>',
    })

    expect(wrapper.get('#custom-home').text()).toBe('Custom home')
    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
  })

  it('renders custom URL content ahead of compact mode', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      home_content: ' https://example.com/home ',
    })

    expect(wrapper.get('iframe').attributes('src')).toBe('https://example.com/home')
    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
  })

  it('treats whitespace-only custom content as empty and selects compact mode', () => {
    const wrapper = mountHome({ compact_home_enabled: true, home_content: ' \n\t ' })

    expect(wrapper.get('[data-testid="compact-home"]').text()).toContain('Test site')
  })

  it.each([undefined, false])('selects the BirdAPI brand route home when compact mode is %s', (enabled) => {
    const settings = enabled === undefined ? {} : { compact_home_enabled: enabled }
    const wrapper = mountHome(settings)

    expect(wrapper.find('[data-testid="compact-home"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="brand-route-home"]').exists()).toBe(true)
    expect(wrapper.text()).toContain('所有模型，')
    expect(wrapper.text()).toContain('只走一条航线。')
    expect(wrapper.text()).toContain('01 / CONNECT ONCE')
    expect(wrapper.text()).toContain('02 / STAY ONLINE')
    expect(wrapper.text()).toContain('03 / COUNT EVERY TOKEN')
    expect(wrapper.find('.terminal-container').exists()).toBe(false)
  })

  it('sends the default home primary CTA to registration when registration is enabled', () => {
    const wrapper = mountHome({ registration_enabled: true })
    const primaryCta = wrapper
      .findAllComponents(RouterLinkStub)
      .find((link) => link.text().includes('注册并开始'))

    expect(primaryCta?.props('to')).toBe('/register')
  })

  it('falls back to login when registration is disabled', () => {
    const wrapper = mountHome({ registration_enabled: false })
    const primaryCta = wrapper
      .findAllComponents(RouterLinkStub)
      .find((link) => link.text().includes('登录并开始'))

    expect(primaryCta?.props('to')).toBe('/login')
  })

  it('sends authenticated users from the default home to their dashboard', () => {
    authStore.isAuthenticated = true

    const wrapper = mountHome({ registration_enabled: true })
    const primaryCta = wrapper
      .findAllComponents(RouterLinkStub)
      .find((link) => link.text().includes('进入控制台'))

    expect(primaryCta?.props('to')).toBe('/dashboard')
  })

  it('sends authenticated administrators from the default home to the admin dashboard', () => {
    authStore.isAuthenticated = true
    authStore.isAdmin = true

    const wrapper = mountHome({ registration_enabled: true })
    const primaryCta = wrapper
      .findAllComponents(RouterLinkStub)
      .find((link) => link.text().includes('进入控制台'))

    expect(primaryCta?.props('to')).toBe('/admin/dashboard')
  })

  it('lets keyboard users activate the route value cards with Space', async () => {
    const wrapper = mountHome()
    const valueCard = wrapper.get('[role="button"][data-step="01"]')

    await valueCard.trigger('keydown', { key: ' ' })

    expect(valueCard.classes()).toContain('active')
  })

  it('keeps navigation and display controls available in the mobile menu', async () => {
    const wrapper = mountHome({
      doc_url: 'https://example.com/docs',
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    await wrapper.get('.mobile-menu-toggle').trigger('click')

    const menu = wrapper.get('#brand-route-mobile-menu')
    expect(menu.text()).toContain('home.docs')
    expect(menu.text()).toContain('home.brandRoute.pricing')
    expect(menu.text()).toContain('home.login')
    expect(menu.find('[data-testid="locale-switcher"]').exists()).toBe(true)
  })

  it('links unauthenticated visitors to login', () => {
    expect(compactDestination(mountHome({ compact_home_enabled: true }))).toBe('/login')
  })

  it('links authenticated users to their dashboard', () => {
    authStore.isAuthenticated = true

    expect(compactDestination(mountHome({ compact_home_enabled: true }))).toBe('/dashboard')
  })

  it('links administrators to the admin dashboard', () => {
    authStore.isAuthenticated = true
    authStore.isAdmin = true

    const wrapper = mountHome({ compact_home_enabled: true })
    expect(compactDestination(wrapper)).toBe('/admin/dashboard')
    expect(authStore.checkAuth).toHaveBeenCalledOnce()
    expect(appStore.fetchPublicSettings).not.toHaveBeenCalled()
  })

  it('shows the model plaza link to anonymous visitors when public access is enabled', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('hides the model plaza link from anonymous visitors when sign-in is required', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: true,
    })

    expect(modelPlazaDestination(wrapper)).toBeUndefined()
  })

  it('shows the model plaza link to authenticated visitors when sign-in is required', () => {
    authStore.isAuthenticated = true

    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: true,
      model_plaza_require_auth: true,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('shows the model plaza link in the default home header', () => {
    const wrapper = mountHome({
      model_plaza_enabled: true,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBe('/model-plaza')
  })

  it('hides the model plaza link when the feature is disabled', () => {
    const wrapper = mountHome({
      compact_home_enabled: true,
      model_plaza_enabled: false,
      model_plaza_require_auth: false,
    })

    expect(modelPlazaDestination(wrapper)).toBeUndefined()
  })
})

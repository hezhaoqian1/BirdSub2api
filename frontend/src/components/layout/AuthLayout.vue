<template>
  <div class="auth-shell min-h-screen">
    <header class="auth-mast">
      <router-link to="/home" class="auth-wordmark" :aria-label="`${siteName} ${t('auth.backHome')}`">
        <span class="bird-wingmark" aria-hidden="true"><i></i><i></i><i></i></span>
        <span>{{ siteName }}</span>
      </router-link>
      <div class="auth-mast-note">ONE ROUTE · EVERY MODEL<br>ACCESS NOTES · 01 / 2026</div>
      <router-link to="/home" class="auth-home-link">
        {{ t('auth.backHome') }}
        <span aria-hidden="true">↗</span>
      </router-link>
    </header>

    <main class="auth-frame">
      <section class="auth-brand-panel" aria-label="BirdAPI brand">
        <div class="auth-brand-meta">
          <span>BIRDAPI ACCESS / 01</span>
          <span>FOR INDEPENDENT DEVELOPERS</span>
        </div>

        <div class="auth-flight" aria-hidden="true">
          <svg class="auth-route" viewBox="0 0 760 300" preserveAspectRatio="none">
            <path class="auth-route-line auth-route-line-paper" d="M-30 235 C140 220 210 105 360 126 C495 145 530 254 790 82" />
            <path class="auth-route-line auth-route-line-yellow" d="M-35 270 C146 256 224 142 372 161 C510 179 551 283 795 119" />
            <path class="auth-route-line auth-route-line-red" d="M-42 304 C155 290 238 177 388 197 C526 216 575 310 804 154" />
          </svg>
          <svg class="auth-bird" viewBox="0 0 15 15">
            <path d="M1.63 11.24 5.89 8.04c-.62-.26-1.02-.48-1.2-.66-.27-.27-.35-2.13-.53-3.2l-.11-.07C3.16 3.46-.35.43.03.05.43-.35 5.62 1.65 6.29 2.32c.44.44.84 1.28 1.2 2.53.91-.25 1.51-.5 1.8-.74l.06-.06c.27-.27.8-.45 1.6-.53l1.33-.8-.8 1.33c-.08.73-.23 1.24-.46 1.52l-.07.08c-.26.26-.53.88-.8 1.86 1.25.36 2.09.76 2.53 1.2.67.67 2.67 5.86 2.27 6.26-.4.4-3.71-3.48-4.13-4.13-1.07-.18-2.93-.26-3.2-.53-.18-.18-.4-.58-.66-1.2l-3.2 4.26c-.71 0-1.24-.17-1.6-.53-.36-.36-.53-.89-.53-1.6Z" />
          </svg>
        </div>

        <div class="auth-brand-copy">
          <span class="auth-kicker">ONE ACCOUNT / CLAUDE · GPT · GEMINI</span>
          <h1>{{ t('auth.brandHeadlineLine1') }}<br>{{ t('auth.brandHeadlineLine2') }}</h1>
          <p>{{ t('auth.brandDescription') }}</p>
        </div>
      </section>

      <section class="auth-workspace">
        <nav class="auth-index" :aria-label="t('auth.accessNavigation')">
          <router-link to="/login" :class="{ active: route.path === '/login' }">01 {{ t('auth.signIn') }}</router-link>
          <router-link to="/register" :class="{ active: route.path === '/register' }">02 {{ t('auth.signUp') }}</router-link>
          <router-link to="/forgot-password" :class="{ active: isPasswordRoute }">03 {{ t('auth.forgotPassword') }}</router-link>
        </nav>

        <div class="auth-workspace-inner">
          <div class="auth-form-mark bird-wingmark" aria-hidden="true"><i></i><i></i><i></i></div>
          <div class="auth-card">
            <slot />
          </div>

          <div class="auth-footer text-sm">
            <slot name="footer" />
          </div>

          <div class="auth-copyright">
            &copy; {{ currentYear }} {{ siteName }} · ONE ROUTE / EVERY MODEL
          </div>
        </div>
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import { resolveDisplaySiteName } from '@/utils/branding'

const appStore = useAppStore()
const route = useRoute()
const { t } = useI18n()

const siteName = computed(() => resolveDisplaySiteName(appStore.siteName))
const isPasswordRoute = computed(() => ['/forgot-password', '/reset-password'].includes(route.path))
const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>

<style scoped>
.auth-mast {
  position: relative;
  z-index: 5;
  display: grid;
  min-height: 88px;
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
  gap: 24px;
  border-bottom: 1px solid var(--bird-ink);
  padding: 0 30px;
  background: var(--bird-paper);
}

.auth-wordmark {
  display: inline-flex;
  width: fit-content;
  align-items: center;
  gap: 12px;
  color: var(--bird-ink);
  font: 680 29px/1 var(--bird-font-display);
}

.bird-wingmark {
  display: grid;
  width: 28px;
  gap: 3px;
  transform: rotate(-8deg);
}

.bird-wingmark i {
  display: block;
  height: 3px;
  background: var(--bird-blue);
}

.bird-wingmark i:nth-child(2) { width: 20px; background: var(--bird-red); }
.bird-wingmark i:nth-child(3) { width: 13px; background: var(--bird-yellow); }

.auth-mast-note {
  text-align: center;
  color: var(--bird-ink);
  font: 10px/1.5 var(--bird-font-mono);
  letter-spacing: .035em;
}

.auth-home-link {
  justify-self: end;
  display: inline-flex;
  min-height: 46px;
  align-items: center;
  gap: 10px;
  border: 1px solid var(--bird-ink);
  padding: 0 20px;
  background: var(--bird-ink);
  color: var(--bird-paper);
  font-size: 13px;
  font-weight: 700;
  transition: transform 160ms ease, box-shadow 160ms ease;
}

.auth-home-link:hover { transform: translateY(-2px); box-shadow: 4px 4px 0 var(--bird-red); }

.auth-frame {
  display: grid;
  min-height: calc(100vh - 88px);
  grid-template-columns: minmax(440px, .88fr) minmax(560px, 1.12fr);
}

.auth-brand-panel {
  position: relative;
  isolation: isolate;
  overflow: hidden;
  display: flex;
  min-height: 720px;
  flex-direction: column;
  justify-content: space-between;
  border-right: 1px solid var(--bird-ink);
  padding: 32px 30px 40px;
  background: var(--bird-blue);
  color: white;
}

.auth-brand-panel::before {
  content: '';
  position: absolute;
  inset: 0;
  z-index: -2;
  background-image:
    linear-gradient(rgba(255,255,255,.07) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255,255,255,.07) 1px, transparent 1px);
  background-size: 64px 64px;
}

.auth-brand-panel::after {
  content: '';
  position: absolute;
  inset: 18px;
  z-index: -1;
  border: 1px solid rgba(255,255,255,.2);
  pointer-events: none;
}

.auth-brand-meta {
  display: flex;
  justify-content: space-between;
  gap: 24px;
  font: 10px/1.5 var(--bird-font-mono);
  letter-spacing: .035em;
}

.auth-flight { position: absolute; inset: 21% -4% auto -6%; height: 310px; }
.auth-route { width: 100%; height: 100%; overflow: visible; }
.auth-route-line { fill: none; stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.auth-route-line-paper { stroke: rgba(255,255,255,.72); }
.auth-route-line-yellow { stroke: rgba(230,200,75,.86); }
.auth-route-line-red { stroke: rgba(216,68,47,.9); }
.auth-bird {
  position: absolute;
  left: 55%;
  top: 32%;
  width: clamp(104px, 10vw, 156px);
  color: var(--bird-yellow);
  fill: currentColor;
  filter: drop-shadow(6px 7px 0 rgba(23,23,20,.22));
  transform: rotate(-9deg);
  transform-origin: 50% 50%;
  animation: auth-bird-glide 5.8s ease-in-out infinite;
}

.auth-brand-copy { position: relative; z-index: 1; max-width: 610px; }
.auth-kicker { display: block; margin-bottom: 18px; color: var(--bird-yellow); font: 11px var(--bird-font-mono); }
.auth-brand-copy h1 {
  margin: 0;
  font: 400 clamp(68px, 6.4vw, 112px)/1.06 var(--bird-font-headline);
  letter-spacing: 0;
  text-wrap: balance;
}
.auth-brand-copy p { max-width: 480px; margin: 24px 0 0; font-size: 16px; line-height: 1.7; text-wrap: pretty; }

.auth-workspace { display: grid; min-width: 0; grid-template-columns: 150px minmax(0, 1fr); background: var(--bird-paper); }
.auth-index {
  display: flex;
  flex-direction: column;
  gap: 6px;
  border-right: 1px solid var(--bird-ink);
  padding: 36px 20px;
}
.auth-index a {
  display: flex;
  min-height: 44px;
  align-items: center;
  padding: 0 10px;
  color: var(--bird-ink);
  font: 10px var(--bird-font-mono);
  transition: background-color 160ms ease, color 160ms ease;
}
.auth-index a:hover { background: var(--bird-yellow); }
.auth-index a.active { background: var(--bird-red); color: white; }

.auth-workspace-inner {
  width: min(100%, 640px);
  margin: auto;
  padding: 64px clamp(30px, 6vw, 92px) 36px;
}

.auth-form-mark { margin-bottom: 26px; }
.auth-card { color: var(--bird-ink); }
.auth-card :deep(> .space-y-6 > .text-center:first-child) { text-align: left; }
.auth-card :deep(> .space-y-6 > .text-center:first-child h2) {
  margin: 0;
  color: var(--bird-ink);
  font: 540 clamp(46px, 4.2vw, 64px)/1.08 var(--bird-font-display);
  letter-spacing: -.025em;
  text-wrap: balance;
}
.auth-card :deep(> .space-y-6 > .text-center:first-child p) { margin-top: 12px; max-width: 460px; color: var(--bird-muted); font-size: 15px; line-height: 1.65; }
.auth-card :deep(form) { margin-top: 34px; }
.auth-card :deep(.input) { min-height: 54px; background: rgba(255,255,255,.58); }
.auth-card :deep(.btn) { min-height: 54px; }
.auth-card :deep(.btn-primary) { background: var(--bird-red); border-color: var(--bird-red); box-shadow: 4px 4px 0 var(--bird-ink); }
.auth-card :deep(.btn-primary:hover) { background: #c93b29; transform: translateY(-2px); box-shadow: 6px 6px 0 var(--bird-ink); }
.auth-footer { margin-top: 28px; color: var(--bird-muted); }
.auth-footer :deep(a) { color: var(--bird-blue); font-weight: 700; }
.auth-copyright { margin-top: 54px; color: var(--bird-muted); font: 10px/1.5 var(--bird-font-mono); }

@keyframes auth-bird-glide {
  0%, 100% { transform: translate(-8px, 5px) rotate(-11deg); }
  45% { transform: translate(14px, -10px) rotate(-4deg); }
  70% { transform: translate(4px, -5px) rotate(-7deg); }
}

@media (max-width: 1080px) {
  .auth-frame { grid-template-columns: 1fr; }
  .auth-brand-panel { min-height: 470px; border-right: 0; border-bottom: 1px solid var(--bird-ink); }
  .auth-brand-copy h1 { font-size: clamp(58px, 10vw, 88px); }
  .auth-flight { inset: 8% -2% auto 34%; }
  .auth-workspace { min-height: 720px; }
}

@media (max-width: 720px) {
  .auth-mast { min-height: 76px; grid-template-columns: 1fr auto; padding: 0 16px; }
  .auth-mast-note { display: none; }
  .auth-wordmark { font-size: 25px; }
  .auth-home-link { min-height: 42px; padding: 0 14px; }
  .auth-frame { min-height: calc(100vh - 76px); }
  .auth-brand-panel { min-height: 390px; padding: 24px 16px 28px; }
  .auth-brand-panel::after { inset: 10px; }
  .auth-brand-meta { font-size: 8px; }
  .auth-brand-meta span:last-child { display: none; }
  .auth-flight { inset: 6% -40% auto 28%; height: 250px; }
  .auth-bird { width: 94px; }
  .auth-kicker { margin-bottom: 12px; font-size: 9px; }
  .auth-brand-copy h1 { font-size: 48px; line-height: 1.04; }
  .auth-brand-copy p { margin-top: 14px; font-size: 14px; }
  .auth-workspace { min-height: 0; grid-template-columns: 1fr; }
  .auth-index { flex-direction: row; gap: 0; overflow-x: auto; border-right: 0; border-bottom: 1px solid var(--bird-ink); padding: 0; }
  .auth-index a { flex: 1 0 auto; justify-content: center; padding: 0 16px; }
  .auth-workspace-inner { padding: 40px 18px 30px; }
  .auth-card :deep(> .space-y-6 > .text-center:first-child h2) { font-size: 42px; }
  .auth-copyright { margin-top: 40px; }
}

@media (prefers-reduced-motion: reduce) {
  .auth-bird { animation: none; }
  .auth-home-link,
  .auth-index a,
  .auth-card :deep(.btn-primary) { transition: none; }
}
</style>

<template>
  <div class="brand-route-home" data-testid="brand-route-home">
    <header class="mast">
      <router-link to="/home" class="brand" :aria-label="t('home.brandRoute.homeAria')">
        <span class="wingmark" aria-hidden="true"><i></i><i></i><i></i></span>
        <span>{{ siteName }}</span>
      </router-link>

      <div class="issue" aria-hidden="true">
        {{ t('home.brandRoute.issueLine1') }}<br />{{ t('home.brandRoute.issueLine2') }}
      </div>

      <div class="mast-actions">
        <div class="mast-tools">
          <LocaleSwitcher />
          <button
            class="icon-action"
            type="button"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="$emit('toggle-theme')"
          >
            <Icon :name="isDark ? 'sun' : 'moon'" size="sm" />
          </button>
        </div>
        <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer">{{ t('home.docs') }}</a>
        <router-link v-if="showModelPlazaEntry" to="/model-plaza">{{ t('home.brandRoute.pricing') }}</router-link>
        <router-link class="login-link" :to="loginDestination">{{ loginLabel }}</router-link>
        <button
          class="icon-action mobile-menu-toggle"
          type="button"
          :aria-label="t('common.toggleMenu')"
          :aria-expanded="mobileMenuOpen"
          aria-controls="brand-route-mobile-menu"
          @click="mobileMenuOpen = !mobileMenuOpen"
        >
          <Icon :name="mobileMenuOpen ? 'x' : 'menu'" size="md" />
        </button>
        <router-link class="build-link" :to="primaryDestination">{{ primaryHeaderLabel }} <span aria-hidden="true">↗</span></router-link>
      </div>

      <div
        v-if="mobileMenuOpen"
        id="brand-route-mobile-menu"
        class="mobile-menu"
        @keydown.esc="mobileMenuOpen = false"
      >
        <div class="mobile-menu-tools">
          <LocaleSwitcher />
          <button
            class="icon-action"
            type="button"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            :aria-label="isDark ? t('home.switchToLight') : t('home.switchToDark')"
            @click="handleMobileThemeToggle"
          >
            <Icon :name="isDark ? 'sun' : 'moon'" size="sm" />
          </button>
        </div>
        <a v-if="docUrl" :href="docUrl" target="_blank" rel="noopener noreferrer" @click="mobileMenuOpen = false">{{ t('home.docs') }}</a>
        <router-link v-if="showModelPlazaEntry" to="/model-plaza" @click="mobileMenuOpen = false">{{ t('home.brandRoute.pricing') }}</router-link>
        <router-link class="login-link" :to="loginDestination" @click="mobileMenuOpen = false">{{ loginLabel }}</router-link>
      </div>
    </header>

    <main>
      <section class="cover">
        <div class="cover-main">
          <div class="index">
            <span>{{ t('home.brandRoute.protocol') }}</span>
            <span>{{ t('home.brandRoute.audience') }}</span>
          </div>

          <h1>
            <span class="headline-line brand-name">{{ siteName }}</span>
            <span class="headline-line">{{ t('home.brandRoute.headlineLead') }}</span>
            <span class="headline-line blue">{{ t('home.brandRoute.headlineTail') }}</span>
          </h1>

          <div class="deck">
            <p>{{ t('home.brandRoute.description') }}</p>
            <div class="cta-stack">
              <div class="buttons">
                <router-link class="secondary-cta" :to="loginDestination">{{ loginLabel }}</router-link>
                <router-link class="primary-cta" :to="primaryDestination">{{ primaryCtaLabel }} <span aria-hidden="true">↗</span></router-link>
              </div>
              <span class="cta-note">{{ ctaNote }}</span>
            </div>
          </div>
        </div>

        <aside
          ref="stageRef"
          class="flight-stage"
          :aria-label="t('home.brandRoute.animationAria')"
          @pointermove="handlePointerMove"
          @pointerleave="resetParallax"
        >
          <div class="stage-label">{{ t('home.brandRoute.stageLine1') }}<br />{{ t('home.brandRoute.stageLine2') }}</div>
          <div class="stage-label right">{{ t('home.brandRoute.motionLine') }}<br />CLAUDE → GPT → GEMINI</div>
          <div class="flight-art">
            <svg class="flight-svg" viewBox="0 0 700 650" role="img" aria-labelledby="flight-title flight-desc">
              <title id="flight-title">{{ t('home.brandRoute.flightTitle') }}</title>
              <desc id="flight-desc">{{ t('home.brandRoute.flightDescription') }}</desc>
              <path ref="flightPathRef" class="route-base" :d="routePath" />
              <path ref="progressPathRef" class="route-progress" :d="routePath" />
              <path class="wake red" d="M-65 548 C49 540 91 467 184 399 C275 332 297 237 380 236 C468 235 495 329 576 344 C653 359 706 312 786 278" />
              <path class="wake blue" d="M-74 576 C60 563 109 497 203 431 C294 367 324 274 402 274 C483 274 519 363 598 377 C669 390 718 350 798 316" />

              <g id="nodeClaude" class="node" transform="translate(165 466)">
                <line class="node-tether" y1="-110" y2="-44" />
                <circle class="node-halo" r="57" />
                <circle class="node-ring" r="43" />
                <g transform="translate(-22 -22) scale(.172)">
                  <path fill="#D97757" d="m50.228 170.321 50.357-28.257.843-2.463-.843-1.361h-2.462l-8.426-.518-28.775-.778-24.952-1.037-24.175-1.296-6.092-1.297L0 125.796l.583-3.759 5.12-3.434 7.324.648 16.202 1.101 24.304 1.685 17.629 1.037 26.118 2.722h4.148l.583-1.685-1.426-1.037-1.101-1.037-25.147-17.045-27.22-18.017-14.258-10.37-7.713-5.25-3.888-4.925-1.685-10.758 7-7.713 9.397.649 2.398.648 9.527 7.323 20.35 15.75L94.817 91.9l3.889 3.24 1.555-1.102.195-.777-1.75-2.917-14.453-26.118-15.425-26.572-6.87-11.018-1.814-6.61c-.648-2.723-1.102-4.991-1.102-7.778l7.972-10.823L71.42 0 82.05 1.426l4.472 3.888 6.61 15.101 10.694 23.786 16.591 32.34 4.861 9.592 2.592 8.879.973 2.722h1.685v-1.556l1.36-18.211 2.528-22.36 2.463-28.776.843-8.1 4.018-9.722 7.971-5.25 6.222 2.981 5.12 7.324-.713 4.73-3.046 19.768-5.962 30.98-3.889 20.739h2.268l2.593-2.593 10.499-13.934 17.628-22.036 7.778-8.749 9.073-9.657 5.833-4.601h11.018l8.1 12.055-3.628 12.443-11.342 14.388-9.398 12.184-13.48 18.147-8.426 14.518.778 1.166 2.01-.194 30.46-6.481 16.462-2.982 19.637-3.37 8.88 4.148.971 4.213-3.5 8.62-20.998 5.184-24.628 4.926-36.682 8.685-.454.324.519.648 16.526 1.555 7.065.389h17.304l32.21 2.398 8.426 5.574 5.055 6.805-.843 5.184-12.962 6.611-17.498-4.148-40.83-9.721-14-3.5h-1.944v1.167l11.666 11.406 21.387 19.314 26.767 24.887 1.36 6.157-3.434 4.86-3.63-.518-23.526-17.693-9.073-7.972-20.545-17.304h-1.36v1.814l4.73 6.935 25.017 37.59 1.296 11.536-1.814 3.76-6.481 2.268-7.13-1.297-14.647-20.544-15.1-23.138-12.185-20.739-1.49.843-7.194 77.448-3.37 3.953-7.778 2.981-6.48-4.925-3.436-7.972 3.435-15.749 4.148-20.544 3.37-16.333 3.046-20.285 1.815-6.74-.13-.454-1.49.194-15.295 20.999-23.267 31.433-18.406 19.702-4.407 1.75-7.648-3.954.713-7.064 4.277-6.286 25.47-32.405 15.36-20.092 9.917-11.6-.065-1.686h-.583L44.07 198.125l-12.055 1.555-5.185-4.86.648-7.972 2.463-2.593 20.35-13.999-.064.065Z" />
                </g>
                <text class="node-label" y="68">Claude</text>
                <text class="node-meta" y="83">ANTHROPIC</text>
              </g>

              <g id="nodeGPT" class="node" transform="translate(360 300)">
                <line class="node-tether" y1="-110" y2="-44" />
                <circle class="node-halo" r="57" />
                <circle class="node-ring" r="43" />
                <g transform="translate(-23 -23) scale(.0753)">
                  <path fill-rule="evenodd" clip-rule="evenodd" d="M252.794 108.802C289.191 99.048 326.265 110.305 351.148 135.135C385.113 126.072 422.85 134.862 449.492 161.505C476.136 188.149 484.925 225.888 475.862 259.85C500.696 284.735 511.95 321.81 502.198 358.207C492.447 394.602 464.161 421.084 430.215 430.217C421.083 464.162 394.603 492.448 358.206 502.199C321.812 511.951 284.734 500.693 259.852 475.864C225.887 484.927 188.15 476.137 161.507 449.495C134.864 422.851 126.073 385.111 135.136 351.149C110.304 326.266 99.05 289.192 108.801 252.795C118.552 216.4 146.84 189.918 180.784 180.785C189.917 146.841 216.396 118.553 252.794 108.802ZM374.292 407.145V292.509L406.843 311.303C409.125 312.621 410.555 315.08 410.555 317.717V405.006C410.068 439.312 386.997 470.532 352.217 479.852C327.555 486.459 302.487 480.585 283.723 466.102L368.517 417.148C372.092 415.086 374.292 411.271 374.292 407.145ZM251.868 415.897L351.148 358.579V396.163C351.148 398.8 349.735 401.268 347.449 402.586L271.85 446.232C241.896 462.962 203.325 458.594 177.866 433.136C159.811 415.08 152.366 390.436 155.526 366.942L240.317 415.897C243.893 417.959 248.296 417.959 251.868 415.897ZM368.602 220.628L444.201 264.274C473.668 281.85 489.169 317.442 479.851 352.218C473.244 376.881 455.622 395.654 433.697 404.661V306.752C433.697 302.627 431.496 298.811 427.921 296.749L328.641 239.431L361.191 220.637C363.474 219.318 366.319 219.309 368.602 220.628ZM177.303 206.34V304.251C177.303 308.375 179.504 312.189 183.078 314.253L282.357 371.572L249.807 390.366C247.525 391.684 244.68 391.692 242.398 390.373L166.799 346.727C137.331 329.153 121.832 293.561 131.148 258.783C137.756 234.122 155.377 215.348 177.303 206.34ZM259.849 279.145V331.858L305.5 358.213L351.15 331.858V279.145L305.5 252.789L259.849 279.145ZM327.276 144.9L242.483 193.853C238.909 195.916 236.707 199.731 236.707 203.856V318.493L204.158 299.7C201.875 298.381 200.445 295.923 200.445 293.286V205.995C200.931 171.691 224.002 140.471 258.782 131.15C283.445 124.543 308.512 130.418 327.276 144.9ZM433.137 177.867C451.189 195.922 458.635 220.567 455.473 244.06L370.682 195.105C367.108 193.041 362.703 193.041 359.132 195.105L259.852 252.423V214.838C259.852 212.202 261.265 209.734 263.55 208.415L339.149 164.769C369.103 148.038 407.675 152.407 433.137 177.867Z" fill="currentColor" />
                </g>
                <text class="node-label" y="68">GPT</text>
                <text class="node-meta" y="83">OPENAI</text>
              </g>

              <g id="nodeGemini" class="node" transform="translate(555 412)">
                <line class="node-tether" y1="-110" y2="-44" />
                <circle class="node-halo" r="57" />
                <circle class="node-ring" r="43" />
                <g transform="translate(-22 -22) scale(1.84)">
                  <path fill="#8E75B2" d="M11.04 19.32Q12 21.51 12 24q0-2.49.93-4.68.96-2.19 2.58-3.81t3.81-2.55Q21.51 12 24 12q-2.49 0-4.68-.93a12.3 12.3 0 0 1-3.81-2.58 12.3 12.3 0 0 1-2.58-3.81Q12 2.49 12 0q0 2.49-.96 4.68-.93 2.19-2.55 3.81a12.3 12.3 0 0 1-3.81 2.58Q2.49 12 0 12q2.49 0 4.68.96 2.19.93 3.81 2.55t2.55 3.81" />
                </g>
                <text class="node-label" y="68">Gemini</text>
                <text class="node-meta" y="83">GOOGLE</text>
              </g>

              <g id="trailMarkers" aria-hidden="true">
                <circle class="trail-marker" r="4" fill="var(--route-red)" />
                <circle class="trail-marker" r="3" fill="var(--route-blue)" />
                <circle class="trail-marker" r="2" fill="var(--route-paper)" />
              </g>

              <g ref="birdRef" class="flight-bird">
                <g ref="birdVisualRef">
                  <g ref="birdFlapRef">
                    <g transform="rotate(31)">
                      <g transform="translate(-90 -90) scale(12)">
                        <path class="bird-underprint" transform="translate(.34 .42)" :d="birdPath" />
                        <path class="bird-silhouette" :d="birdPath" />
                        <path class="bird-inset" d="M4.55 4.32 C6.1 5.12 7.04 6.13 7.5 7.63 C8.55 7.12 9.62 6.62 10.82 5.48" />
                      </g>
                    </g>
                  </g>
                </g>
              </g>
            </svg>
          </div>

          <div class="stage-note">
            <strong>{{ t('home.brandRoute.stageNoteTitle') }}</strong>
            {{ t('home.brandRoute.stageNoteDescription') }}
            <span class="route-status">{{ routeStatus }}</span>
          </div>
        </aside>
      </section>

      <section class="toc" aria-label="BirdAPI 路由价值">
        <article
          v-for="(item, index) in valueItems"
          :key="item.step"
          :data-step="item.step"
          :class="{ active: activeStep === index || hoveredStep === index }"
          role="button"
          tabindex="0"
          :aria-label="`${item.step} ${item.title}`"
          @mouseenter="hoveredStep = index"
          @mouseleave="hoveredStep = null"
          @focus="hoveredStep = index"
          @blur="hoveredStep = null"
          @click="seekToStep(index)"
          @keydown.enter.prevent="seekToStep(index)"
          @keydown.space.prevent="seekToStep(index)"
        >
          <small>{{ item.eyebrow }}</small>
          <h2>{{ item.title }}</h2>
        </article>
      </section>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'

const props = defineProps<{
  siteName: string
  docUrl: string
  showModelPlazaEntry: boolean
  isAuthenticated: boolean
  isDark: boolean
  dashboardPath: string
  registrationEnabled: boolean
}>()

const emit = defineEmits<{ 'toggle-theme': [] }>()

const { t } = useI18n()

const routePath = 'M-150 510 C-20 526 62 432 165 356 C252 292 270 190 360 190 C452 190 472 286 555 302 C646 320 702 264 835 214'
const birdPath = 'M1.63 11.24L5.89 8.04C5.27 7.78 4.87 7.56 4.69 7.38C4.42 7.11 4.34 5.25 4.16 4.18L4.05 4.11C3.16 3.46 -0.35 0.43 0.03 0.05C0.43 -0.35 5.62 1.65 6.29 2.32C6.73 2.76 7.13 3.6 7.49 4.85C8.4 4.6 9 4.35 9.29 4.11L9.35 4.05C9.62 3.78 10.15 3.6 10.95 3.52L12.28 2.72L11.48 4.05C11.4 4.78 11.25 5.29 11.02 5.57L10.95 5.65C10.69 5.91 10.42 6.53 10.15 7.51C11.4 7.87 12.24 8.27 12.68 8.71C13.35 9.38 15.35 14.57 14.95 14.97C14.55 15.37 11.24 11.49 10.82 10.84C9.75 10.66 7.89 10.58 7.62 10.31C7.44 10.13 7.22 9.73 6.96 9.11L3.76 13.37C3.05 13.37 2.52 13.2 2.16 12.84C1.8 12.48 1.63 11.95 1.63 11.24Z'

const valueItems = computed(() => [
  { step: '01', eyebrow: t('home.brandRoute.value1Eyebrow'), title: t('home.brandRoute.value1Title') },
  { step: '02', eyebrow: t('home.brandRoute.value2Eyebrow'), title: t('home.brandRoute.value2Title') },
  { step: '03', eyebrow: t('home.brandRoute.value3Eyebrow'), title: t('home.brandRoute.value3Title') },
])

const stageRef = ref<HTMLElement | null>(null)
const flightPathRef = ref<SVGPathElement | null>(null)
const progressPathRef = ref<SVGPathElement | null>(null)
const birdRef = ref<SVGGElement | null>(null)
const birdVisualRef = ref<SVGGElement | null>(null)
const birdFlapRef = ref<SVGGElement | null>(null)
const routeStatus = ref(t('home.brandRoute.routeReady'))
const activeStep = ref<number | null>(null)
const hoveredStep = ref<number | null>(null)
const mobileMenuOpen = ref(false)

const loginDestination = computed(() => (props.isAuthenticated ? props.dashboardPath : '/login'))
const primaryDestination = computed(() => {
  if (props.isAuthenticated) return props.dashboardPath
  return props.registrationEnabled ? '/register' : '/login'
})
const loginLabel = computed(() => (props.isAuthenticated ? t('home.dashboard') : t('home.login')))
const primaryHeaderLabel = computed(() => (props.isAuthenticated ? t('home.goToDashboard') : t('home.brandRoute.startBuilding')))
const primaryCtaLabel = computed(() => {
  if (props.isAuthenticated) return t('home.goToDashboard')
  return props.registrationEnabled ? t('home.brandRoute.registerAndStart') : t('home.brandRoute.loginAndStart')
})
const ctaNote = computed(() => {
  if (props.isAuthenticated) return t('home.brandRoute.authenticatedNote')
  return props.registrationEnabled
    ? t('home.brandRoute.registrationNote')
    : t('home.brandRoute.loginNote')
})

const nodes = [
  { selector: '#nodeClaude', x: 165, y: 466, at: 0.3132, label: 'CLAUDE' },
  { selector: '#nodeGPT', x: 360, y: 300, at: 0.5418, label: 'GPT' },
  { selector: '#nodeGemini', x: 555, y: 412, at: 0.7415, label: 'GEMINI' },
]
const duration = 11.8
const travelSamples = 900
const travelTimeline = new Float64Array(travelSamples + 1)
let animationFrame = 0
let routeLength = 0
let time = 0
let lastTick: number | null = null
let reducedMotion = false

function clamp(value: number, min: number, max: number) {
  return Math.min(max, Math.max(min, value))
}

function smootherStep(value: number) {
  const x = clamp(value, 0, 1)
  return x * x * x * (x * (x * 6 - 15) + 10)
}

function routeSpeed(position: number) {
  const slowdown = nodes.reduce((total, node) => {
    const sigma = 0.046
    const offset = position - node.at
    return total + 0.31 * Math.exp(-(offset * offset) / (2 * sigma * sigma))
  }, 0)
  return Math.max(0.48, 1 - slowdown)
}

function prepareTravelTimeline() {
  let totalTravelTime = 0
  for (let index = 1; index <= travelSamples; index += 1) {
    const from = (index - 1) / travelSamples
    const to = index / travelSamples
    totalTravelTime += (to - from) / routeSpeed((from + to) * 0.5)
    travelTimeline[index] = totalTravelTime
  }
  for (let index = 1; index <= travelSamples; index += 1) {
    travelTimeline[index] /= totalTravelTime
  }
}

function travelAt(raw: number) {
  const target = clamp(raw, 0, 1)
  let low = 0
  let high = travelSamples
  while (low + 1 < high) {
    const middle = (low + high) >> 1
    if (travelTimeline[middle] < target) low = middle
    else high = middle
  }
  const timeA = travelTimeline[low]
  const timeB = travelTimeline[high]
  const blend = timeB === timeA ? 0 : (target - timeA) / (timeB - timeA)
  return (low + blend) / travelSamples
}

function renderFlight(currentTime: number) {
  const path = flightPathRef.value
  const progressPath = progressPathRef.value
  const stage = stageRef.value
  const bird = birdRef.value
  const birdVisual = birdVisualRef.value
  const birdFlap = birdFlapRef.value
  if (!path || !progressPath || !stage || !bird || !birdVisual || !birdFlap || routeLength === 0) return

  const raw = clamp(currentTime / duration, 0, 1)
  const normalized = clamp((raw - 0.035) / 0.93, 0, 1)
  const travel = travelAt(normalized)
  const distance = routeLength * travel
  const point = path.getPointAtLength(distance)
  const previous = path.getPointAtLength(Math.max(0, distance - 18))
  const next = path.getPointAtLength(Math.min(routeLength, distance + 18))
  const angle = Math.atan2(next.y - previous.y, next.x - previous.x) * 180 / Math.PI
  const fade = clamp(raw / 0.055, 0, 1) * clamp((1 - raw) / 0.055, 0, 1)
  const bank = clamp(angle * 0.23, -8, 8) + Math.sin(normalized * Math.PI * 2.4) * 0.55
  const before = travelAt(clamp(normalized - 0.0025, 0, 1))
  const after = travelAt(clamp(normalized + 0.0025, 0, 1))
  const speed = clamp((after - before) / 0.005, 0.45, 1.45)
  const glide = nodes.reduce(
    (strongest, node) => Math.max(strongest, 1 - clamp(Math.abs(travel - node.at) / 0.065, 0, 1)),
    0,
  )
  const wingPhase = currentTime * Math.PI * 3.55 + Math.sin(normalized * Math.PI * 2) * 0.28
  const wingStroke = Math.sin(wingPhase)
  const wingEnergy = 0.32 + clamp((speed - 0.45) / 1, 0, 1) * 0.68
  const wingAmplitude = (0.005 + wingEnergy * 0.009) * (1 - glide * 0.58)
  const bodyBob = (
    Math.sin(currentTime * Math.PI * 1.7) * 0.52
    + Math.sin(currentTime * Math.PI * 0.73 + 1.2) * 0.34
  ) * (1 - glide * 0.28)
  const pitch = Math.sin(currentTime * Math.PI * 0.67 + 0.8) * 0.48 + wingStroke * 0.12

  bird.setAttribute('transform', `translate(${point.x} ${point.y}) rotate(${bank}) scale(.94)`)
  bird.style.opacity = String(fade)
  birdVisual.setAttribute('transform', `translate(0 ${bodyBob}) rotate(${pitch})`)
  birdFlap.setAttribute('transform', `scale(${1 - wingStroke * wingAmplitude * 0.28} ${1 + wingStroke * wingAmplitude})`)

  const trailMarkers = stage.querySelectorAll<SVGCircleElement>('#trailMarkers circle')
  trailMarkers.forEach((marker, index) => {
    const trailDistance = Math.max(0, distance - (34 + speed * 8) - index * (22 + speed * 5))
    const trailPoint = path.getPointAtLength(trailDistance)
    marker.setAttribute('cx', String(trailPoint.x))
    marker.setAttribute('cy', String(trailPoint.y))
    marker.style.opacity = String(fade * [0.66, 0.39, 0.19][index])
  })
  progressPath.style.strokeDashoffset = String(routeLength * (1 - travel))
  stage.querySelectorAll<SVGPathElement>('.wake').forEach((wake, index) => {
    wake.style.strokeDashoffset = String(-(raw * 145 + index * 18))
  })

  let nextStatus = t('home.brandRoute.routeReady')
  let nextActiveStep: number | null = null
  nodes.forEach((node, index) => {
    const element = stage.querySelector<SVGGElement>(node.selector)
    const halo = element?.querySelector<SVGCircleElement>('.node-halo')
    if (!element || !halo) return
    const proximity = smootherStep(1 - clamp(Math.abs(travel - node.at) / 0.09, 0, 1))
    halo.style.opacity = String(proximity)
    halo.setAttribute('r', String(53 + proximity * 12))
    element.setAttribute('transform', `translate(${node.x} ${node.y}) scale(${1 + proximity * 0.09})`)
    if (proximity > 0.28) {
      nextStatus = t('home.brandRoute.routing', { model: node.label })
      nextActiveStep = index
    }
  })
  routeStatus.value = nextStatus
  activeStep.value = nextActiveStep
}

function tick(now: number) {
  if (lastTick === null) {
    lastTick = now
  } else {
    time = (time + (now - lastTick) / 1000) % duration
    lastTick = now
  }
  renderFlight(time)
  animationFrame = window.requestAnimationFrame(tick)
}

function seekToStep(index: number) {
  const node = nodes[index]
  if (!node) return
  activeStep.value = index
  time = duration * clamp(node.at * 0.93 + 0.035, 0, 0.999)
  lastTick = null
  renderFlight(time)
}

function handlePointerMove(event: PointerEvent) {
  const stage = stageRef.value
  if (!stage || reducedMotion) return
  const rect = stage.getBoundingClientRect()
  const x = (event.clientX - rect.left) / rect.width - 0.5
  const y = (event.clientY - rect.top) / rect.height - 0.5
  stage.style.setProperty('--parallax-x', `${x * 12}px`)
  stage.style.setProperty('--parallax-y', `${y * 9}px`)
}

function resetParallax() {
  stageRef.value?.style.setProperty('--parallax-x', '0px')
  stageRef.value?.style.setProperty('--parallax-y', '0px')
}

function handleMobileThemeToggle() {
  emit('toggle-theme')
  mobileMenuOpen.value = false
}

onMounted(() => {
  const path = flightPathRef.value
  const progressPath = progressPathRef.value
  if (!path || !progressPath || typeof path.getTotalLength !== 'function') return

  prepareTravelTimeline()
  routeLength = path.getTotalLength()
  progressPath.style.strokeDasharray = String(routeLength)
  progressPath.style.strokeDashoffset = String(routeLength)
  reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches

  if (reducedMotion) {
    renderFlight(duration * 0.57)
    return
  }
  renderFlight(0)
  animationFrame = window.requestAnimationFrame(tick)
})

onBeforeUnmount(() => {
  if (animationFrame) window.cancelAnimationFrame(animationFrame)
})
</script>

<style scoped>
.brand-route-home {
  --route-paper: #f3efe5;
  --route-ink: #171714;
  --route-blue: #1d48d8;
  --route-red: #d8442f;
  --route-yellow: #e6c84b;
  --route-muted: #666159;
  position: relative;
  min-height: 100vh;
  overflow: hidden;
  background: var(--route-paper);
  color: var(--route-ink);
  font-family: var(--bird-font-body);
  font-synthesis: none;
  line-break: strict;
}

.brand-route-home::after {
  position: absolute;
  z-index: 30;
  inset: 0;
  background: repeating-linear-gradient(0deg, transparent 0 3px, rgba(23, 23, 20, 0.035) 4px);
  content: '';
  pointer-events: none;
  opacity: 0.12;
  mix-blend-mode: multiply;
}

.mast {
  position: relative;
  z-index: 40;
  display: grid;
  height: 92px;
  padding: 0 30px;
  border-bottom: 1px solid var(--route-ink);
  grid-template-columns: 1fr auto 1fr;
  align-items: center;
}

.brand {
  display: inline-flex;
  width: fit-content;
  align-items: center;
  gap: 11px;
  font-family: var(--bird-font-display);
  font-size: 30px;
  font-weight: 680;
  line-height: 1;
}

.wingmark {
  display: grid;
  width: 27px;
  gap: 3px;
  transform: rotate(-8deg);
}

.wingmark i { display: block; height: 3px; background: var(--route-blue); }
.wingmark i:nth-child(1) { width: 27px; }
.wingmark i:nth-child(2) { width: 20px; background: var(--route-red); }
.wingmark i:nth-child(3) { width: 13px; background: var(--route-yellow); }

.issue {
  font-family: var(--bird-font-mono);
  font-size: 11px;
  line-height: 1.5;
  text-align: center;
}

.mast-actions,
.mast-tools {
  display: flex;
  align-items: center;
}

.mast-actions {
  justify-self: end;
  gap: 20px;
  font-size: 13px;
}

.mast-tools { gap: 7px; }
.mobile-menu-toggle,
.mobile-menu { display: none; }
.mast-actions a:not(.build-link) { transition: color 160ms ease; }
.mast-actions a:not(.build-link):hover { color: var(--route-red); }
.login-link { color: var(--route-red); font-weight: 700; }

.icon-action {
  display: inline-flex;
  width: 32px;
  height: 32px;
  align-items: center;
  justify-content: center;
  border: 1px solid rgba(23, 23, 20, 0.22);
  background: transparent;
  color: currentColor;
}

.build-link {
  display: inline-flex;
  height: 50px;
  padding: 0 24px;
  align-items: center;
  gap: 8px;
  border: 1px solid var(--route-ink);
  background: var(--route-ink);
  color: var(--route-paper);
  font-weight: 700;
  transition: transform 160ms ease, background 160ms ease;
}

.build-link:hover { transform: translateY(-2px); background: var(--route-blue); }

.cover {
  display: grid;
  height: calc(100svh - 150px);
  min-height: 600px;
  max-height: 800px;
  border-bottom: 1px solid var(--route-ink);
  grid-template-columns: minmax(0, 1fr) minmax(570px, 42vw);
}

.cover-main {
  display: flex;
  min-width: 0;
  padding: 34px 30px 24px;
  flex-direction: column;
  justify-content: space-between;
}

.index {
  display: flex;
  justify-content: space-between;
  gap: 24px;
  font-family: var(--bird-font-mono);
  font-size: 11px;
}

.cover h1 {
  margin: 30px 0 36px;
  font-family: var(--bird-font-headline);
  font-size: 112px;
  font-weight: 400;
  font-feature-settings: 'halt' 1;
  line-height: 1.13;
  text-wrap: balance;
}

.headline-line { display: block; white-space: nowrap; }
.cover h1 .brand-name {
  margin-bottom: 0.11em;
  color: var(--route-blue);
  font-family: var(--bird-font-display);
  font-size: 0.78em;
  font-weight: 720;
  line-height: 0.92;
}
.cover h1 .blue { color: var(--route-blue); }

.deck {
  display: grid;
  align-items: end;
  gap: 34px;
  grid-template-columns: minmax(300px, 1.1fr) auto;
}

.deck p {
  max-width: 620px;
  margin: 0;
  font-size: 18px;
  line-height: 1.72;
  text-wrap: pretty;
}

.cta-stack { display: flex; align-items: flex-end; flex-direction: column; gap: 12px; }
.buttons { display: flex; justify-content: flex-end; gap: 12px; }
.secondary-cta,
.primary-cta {
  display: inline-flex;
  height: 64px;
  min-width: 128px;
  padding: 0 28px;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--route-ink);
  gap: 8px;
  font-size: 17px;
  font-weight: 660;
  transition: transform 160ms ease, box-shadow 160ms ease;
}
.secondary-cta:hover,
.primary-cta:hover { transform: translateY(-3px); }
.primary-cta {
  min-width: 196px;
  border-color: var(--route-red);
  background: var(--route-red);
  box-shadow: 5px 5px 0 var(--route-ink);
  color: #fff;
}
.cta-note { color: var(--route-muted); font-family: var(--bird-font-mono); font-size: 11px; line-height: 1.4; }

.flight-stage {
  --parallax-x: 0px;
  --parallax-y: 0px;
  position: relative;
  isolation: isolate;
  overflow: hidden;
  border-left: 1px solid var(--route-ink);
  background: var(--route-yellow);
}

.flight-stage::before {
  position: absolute;
  z-index: 0;
  inset: 18px;
  border: 1px solid rgba(23, 23, 20, 0.28);
  background-image:
    linear-gradient(rgba(23, 23, 20, 0.075) 1px, transparent 1px),
    linear-gradient(90deg, rgba(23, 23, 20, 0.075) 1px, transparent 1px);
  background-size: 46px 46px;
  content: '';
  pointer-events: none;
}

.stage-label {
  position: absolute;
  z-index: 5;
  top: 34px;
  left: 36px;
  font-family: var(--bird-font-mono);
  font-size: 10px;
  line-height: 1.5;
}
.stage-label.right { right: 36px; left: auto; text-align: right; }

.flight-art {
  position: absolute;
  z-index: 2;
  inset: 48px 2px 116px;
  transform: translate(var(--parallax-x), var(--parallax-y));
  transition: transform 450ms cubic-bezier(0.2, 0.75, 0.2, 1);
}

.flight-svg { display: block; width: 100%; height: 100%; overflow: visible; }
.route-base { fill: none; stroke: rgba(23, 23, 20, 0.22); stroke-width: 1.4; stroke-dasharray: 3 8; }
.route-progress { fill: none; stroke: var(--route-ink); stroke-width: 2.2; stroke-linecap: round; }
.wake { fill: none; stroke-width: 2.2; stroke-linecap: round; stroke-dasharray: 5 10; }
.wake.blue { stroke: var(--route-blue); }
.wake.red { stroke: var(--route-red); }
.node { transition: none; }
.node-ring { fill: var(--route-paper); stroke: var(--route-ink); stroke-width: 1.5; vector-effect: non-scaling-stroke; }
.node-halo { fill: none; stroke: var(--route-red); stroke-width: 2; opacity: 0; vector-effect: non-scaling-stroke; }
.node-label { fill: var(--route-ink); font-family: var(--bird-font-body); font-size: 15px; font-weight: 680; text-anchor: middle; }
.node-meta { fill: rgba(23, 23, 20, 0.65); font-family: var(--bird-font-mono); font-size: 9px; text-anchor: middle; }
.node-tether { stroke: rgba(23, 23, 20, 0.4); stroke-width: 1.2; stroke-dasharray: 2 5; vector-effect: non-scaling-stroke; }
.flight-bird { filter: drop-shadow(4px 5px 0 rgba(23, 23, 20, 0.16)); transform-origin: 0 0; will-change: transform; }
.bird-underprint { fill: var(--route-red); opacity: 0.94; }
.bird-silhouette { fill: var(--route-blue); }
.bird-inset { fill: none; stroke: var(--route-paper); stroke-width: 0.12; stroke-linecap: round; opacity: 0.78; }
.trail-marker { stroke: var(--route-ink); stroke-width: 1.1; vector-effect: non-scaling-stroke; }

.stage-note {
  position: absolute;
  z-index: 6;
  right: 0;
  bottom: 0;
  left: 0;
  min-height: 116px;
  padding: 22px 30px;
  border-top: 1px solid var(--route-ink);
  background: var(--route-ink);
  color: #fff;
  font-family: var(--bird-font-mono);
  font-size: 11px;
  line-height: 1.55;
}
.stage-note strong { display: block; margin-bottom: 5px; color: var(--route-yellow); font-family: var(--bird-font-body); font-size: 20px; font-weight: 680; }
.route-status { position: absolute; top: 26px; right: 30px; color: #fff; font-size: 10px; }

.toc { display: grid; border-bottom: 1px solid var(--route-ink); grid-template-columns: repeat(3, 1fr); }
.toc article {
  position: relative;
  isolation: isolate;
  min-height: 196px;
  overflow: hidden;
  padding: 28px 30px;
  border-right: 1px solid var(--route-ink);
  cursor: pointer;
  transition: background 220ms ease, color 220ms ease, transform 220ms ease;
}
.toc article > * { position: relative; z-index: 1; }
.toc article::before {
  position: absolute;
  z-index: 0;
  top: 12px;
  right: 22px;
  color: rgba(23, 23, 20, 0.1);
  content: attr(data-step);
  font-family: var(--bird-font-mono);
  font-size: 76px;
  font-weight: 700;
  line-height: 0.9;
  transition: color 220ms ease, transform 220ms ease;
}
.toc article::after {
  position: absolute;
  z-index: 0;
  top: 0;
  left: -72%;
  width: 48%;
  height: 100%;
  background: rgba(255, 255, 255, 0.13);
  content: '';
  pointer-events: none;
  transform: skewX(-18deg);
  transition: transform 520ms cubic-bezier(0.2, 0.7, 0.2, 1);
}
.toc article:hover,
.toc article:focus-visible,
.toc article.active { background: var(--route-blue); color: #fff; transform: translateY(-4px); }
.toc article:hover::before,
.toc article:focus-visible::before,
.toc article.active::before { color: var(--route-yellow); transform: translate(-8px, 3px); }
.toc article:hover::after,
.toc article:focus-visible::after,
.toc article.active::after { transform: translateX(285%) skewX(-18deg); }
.toc article:last-child { border-right: 0; }
.toc small { font-family: var(--bird-font-mono); font-size: 11px; }
.toc h2 { margin: 48px 0 0; font-size: 30px; font-weight: 620; line-height: 1.48; }

a:focus-visible,
button:focus-visible,
.toc article:focus-visible { outline: 3px solid var(--route-yellow); outline-offset: 3px; }

:global(.dark) .brand-route-home {
  --route-paper: #171817;
  --route-ink: #f1eee5;
  --route-muted: #aaa59c;
}
:global(.dark) .brand-route-home::after { mix-blend-mode: screen; }
:global(.dark) .flight-stage { --route-ink: #171714; --route-paper: #f3efe5; color: #171714; }
:global(.dark) .toc article::before { color: rgba(241, 238, 229, 0.1); }

@media (max-width: 1400px) {
  .cover h1 { font-size: 94px; }
  .deck { grid-template-columns: 1fr; gap: 24px; }
  .cta-stack { align-items: flex-start; }
}

@media (max-width: 1180px) {
  .mast-actions { gap: 13px; }
  .cover { height: auto; max-height: none; grid-template-columns: 1fr; }
  .cover-main { min-height: 610px; }
  .cover h1 { font-size: 88px; }
  .flight-stage { min-height: 690px; border-top: 1px solid var(--route-ink); border-left: 0; }
}

@media (max-width: 800px) {
  .mast { height: 82px; padding: 0 16px; grid-template-columns: 1fr auto; }
  .brand { font-size: 27px; }
  .issue,
  .mast-actions > a:not(.build-link) { display: none; }
  .mast-tools { display: none; }
  .mast-actions { gap: 8px; }
  .mobile-menu-toggle { display: inline-flex; width: 46px; height: 46px; }
  .mobile-menu {
    position: absolute;
    top: 81px;
    right: 0;
    left: 0;
    display: grid;
    min-height: 64px;
    padding: 12px 16px;
    border-bottom: 1px solid var(--route-ink);
    background: var(--route-paper);
    box-shadow: 0 14px 24px rgba(23, 23, 20, 0.12);
    grid-template-columns: auto repeat(3, minmax(0, 1fr));
    align-items: center;
    gap: 8px;
  }
  .mobile-menu-tools { display: flex; align-items: center; gap: 6px; }
  .mobile-menu > a {
    display: inline-flex;
    min-height: 44px;
    align-items: center;
    justify-content: center;
    border-left: 1px solid rgba(23, 23, 20, 0.18);
    font-size: 13px;
    font-weight: 700;
  }
  .build-link { height: 50px; padding: 0 18px; }
  .cover-main { min-height: 555px; padding: 26px 16px 22px; }
  .index span:last-child { display: none; }
  .cover h1 { margin: 26px 0 32px; font-size: 54px; line-height: 1.18; }
  .deck { grid-template-columns: 1fr; gap: 26px; }
  .deck p { font-size: 16px; line-height: 1.65; }
  .cta-stack { align-items: stretch; }
  .buttons { display: grid; width: 100%; grid-template-columns: 1fr 1.4fr; }
  .secondary-cta,
  .primary-cta { min-width: 0; height: 60px; padding: 0 10px; font-size: 16px; white-space: nowrap; }
  .cta-note { text-align: right; }
  .flight-stage { min-height: 620px; }
  .flight-art { inset: 54px -74px 116px; }
  .stage-label.right { display: none; }
  .stage-note { padding: 22px 24px; }
  .stage-note strong { max-width: 70%; font-size: 18px; }
  .route-status { top: 27px; right: 22px; }
  .toc { grid-template-columns: 1fr; }
  .toc article { min-height: 168px; border-right: 0; border-bottom: 1px solid var(--route-ink); }
  .toc article:last-child { border-bottom: 0; }
  .toc h2 { margin-top: 38px; font-size: 27px; }
}

@media (max-width: 390px) {
  .brand { font-size: 24px; }
  .wingmark { width: 23px; }
  .wingmark i:nth-child(1) { width: 23px; }
  .wingmark i:nth-child(2) { width: 17px; }
  .wingmark i:nth-child(3) { width: 11px; }
  .build-link { padding: 0 13px; font-size: 12px; }
  .mobile-menu { padding-right: 10px; padding-left: 10px; gap: 4px; }
  .mobile-menu > a { font-size: 12px; }
  .cover h1 { font-size: 49px; }
  .secondary-cta,
  .primary-cta { font-size: 14px; }
}

@media (prefers-reduced-motion: reduce) {
  .brand-route-home *,
  .brand-route-home *::before,
  .brand-route-home *::after { scroll-behavior: auto !important; animation: none !important; transition: none !important; }
}
</style>

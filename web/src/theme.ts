import { computed, ref, watch } from 'vue'
import type { GlobalThemeOverrides } from 'naive-ui'

export type ThemeMode = 'auto' | 'light' | 'dark'
export type ResolvedTheme = 'light' | 'dark'

const STORAGE_KEY = 'ovpanel.theme'

function readMode(): ThemeMode {
  const stored = localStorage.getItem(STORAGE_KEY)
  return stored === 'light' || stored === 'dark' ? stored : 'auto'
}

const media = window.matchMedia('(prefers-color-scheme: dark)')
const systemDark = ref(media.matches)
media.addEventListener('change', (event) => {
  systemDark.value = event.matches
})

const mode = ref<ThemeMode>(readMode())

export const resolvedTheme = computed<ResolvedTheme>(() => {
  if (mode.value === 'auto') {
    return systemDark.value ? 'dark' : 'light'
  }
  return mode.value
})

watch(
  mode,
  (value) => {
    localStorage.setItem(STORAGE_KEY, value)
  },
  { flush: 'sync' },
)

watch(
  resolvedTheme,
  (value) => {
    document.documentElement.dataset.theme = value
    document.querySelector('meta[name="theme-color"]')?.setAttribute('content', value === 'dark' ? '#111816' : '#F5F6F3')
  },
  { immediate: true, flush: 'sync' },
)

export function useTheme() {
  return {
    mode,
    resolvedTheme,
    setMode(value: ThemeMode) {
      mode.value = value
    },
  }
}

const fontFamily =
  'system-ui, -apple-system, "Segoe UI", "PingFang SC", "Hiragino Sans GB", "Microsoft YaHei", sans-serif'

export function themeOverrides(dark: boolean): GlobalThemeOverrides {
  if (dark) {
    return {
      common: {
        primaryColor: '#55D6BE',
        primaryColorHover: '#74E2CE',
        primaryColorPressed: '#3FBFAA',
        primaryColorSuppl: '#55D6BE',
        bodyColor: '#111816',
        cardColor: '#192420',
        modalColor: '#192420',
        popoverColor: '#1E2B26',
        borderColor: '#2A3A33',
        dividerColor: '#2A3A33',
        textColorBase: '#EDF4EE',
        textColor1: '#EDF4EE',
        textColor2: '#A5B5AA',
        textColor3: '#82958B',
        placeholderColor: '#70837A',
        borderRadius: '8px',
        borderRadiusSmall: '6px',
        fontFamily,
      },
      Input: {
        color: '#192420',
        colorFocus: '#192420',
        border: '1px solid #3B4F46',
        borderHover: '1px solid #55D6BE',
        borderFocus: '1px solid #55D6BE',
        boxShadowFocus: '0 0 0 3px rgba(85, 214, 190, 0.18)',
        textColor: '#EDF4EE',
        placeholderColor: '#82958B',
        caretColor: '#55D6BE',
      },
      Button: {
        heightMedium: '40px',
        heightLarge: '44px',
        fontWeight: '600',
        borderRadiusMedium: '8px',
        borderRadiusLarge: '8px',
        textColorPrimary: '#0B1512',
        textColorHoverPrimary: '#0B1512',
        textColorPressedPrimary: '#0B1512',
        textColorFocusPrimary: '#0B1512',
      },
      Card: {
        color: '#192420',
        borderColor: '#2A3A33',
        borderRadius: '12px',
      },
      Tag: {
        borderRadius: '999px',
      },
      Alert: {
        borderRadius: '8px',
      },
    }
  }

  return {
    common: {
      primaryColor: '#006B5E',
      primaryColorHover: '#00594E',
      primaryColorPressed: '#004A40',
      primaryColorSuppl: '#006B5E',
      bodyColor: '#F5F6F3',
      cardColor: '#FFFFFF',
      modalColor: '#FFFFFF',
      popoverColor: '#FFFFFF',
      borderColor: '#E2E5DE',
      dividerColor: '#E2E5DE',
      textColorBase: '#14221C',
      textColor1: '#14221C',
      textColor2: '#57665D',
      textColor3: '#64726A',
      placeholderColor: '#7B8880',
      borderRadius: '8px',
      borderRadiusSmall: '6px',
      fontFamily,
    },
    Input: {
      color: '#FFFFFF',
      colorFocus: '#FFFFFF',
      border: '1px solid #CBD2C7',
      borderHover: '1px solid #006B5E',
      borderFocus: '1px solid #006B5E',
      boxShadowFocus: '0 0 0 3px rgba(0, 107, 94, 0.14)',
      textColor: '#14221C',
      placeholderColor: '#64726A',
      caretColor: '#006B5E',
    },
    Button: {
      heightMedium: '40px',
      heightLarge: '44px',
      fontWeight: '600',
      borderRadiusMedium: '8px',
      borderRadiusLarge: '8px',
      textColorPrimary: '#FFFFFF',
      textColorHoverPrimary: '#FFFFFF',
      textColorPressedPrimary: '#FFFFFF',
      textColorFocusPrimary: '#FFFFFF',
    },
    Card: {
      color: '#FFFFFF',
      borderColor: '#E2E5DE',
      borderRadius: '12px',
    },
    Tag: {
      borderRadius: '999px',
    },
    Alert: {
      borderRadius: '8px',
    },
  }
}

/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{ts,tsx}'],
  theme: {
    container: {
      center: true,
      padding: { DEFAULT: '1rem', sm: '1.5rem', lg: '2rem' },
      screens: { '2xl': '1400px' },
    },
    extend: {
      colors: {
        border: 'hsl(var(--border))',
        input: 'hsl(var(--input))',
        ring: 'hsl(var(--ring))',
        background: 'hsl(var(--background))',
        foreground: 'hsl(var(--foreground))',
        primary: {
          DEFAULT: 'hsl(var(--primary))',
          foreground: 'hsl(var(--primary-foreground))',
        },
        secondary: {
          DEFAULT: 'hsl(var(--secondary))',
          foreground: 'hsl(var(--secondary-foreground))',
        },
        destructive: {
          DEFAULT: 'hsl(var(--destructive))',
          foreground: 'hsl(var(--destructive-foreground))',
        },
        muted: {
          DEFAULT: 'hsl(var(--muted))',
          foreground: 'hsl(var(--muted-foreground))',
        },
        accent: {
          DEFAULT: 'hsl(var(--accent))',
          foreground: 'hsl(var(--accent-foreground))',
        },
        card: {
          DEFAULT: 'hsl(var(--card))',
          foreground: 'hsl(var(--card-foreground))',
        },
        success: {
          DEFAULT: 'hsl(var(--success))',
          foreground: 'hsl(var(--success-foreground))',
        },
        warning: {
          DEFAULT: 'hsl(var(--warning))',
          foreground: 'hsl(var(--warning-foreground))',
        },
        // 2077 辅助霓虹色：只用于装饰与图表，不参与语义状态。
        neon: {
          cyan: 'hsl(var(--neon-cyan))',
          magenta: 'hsl(var(--neon-magenta))',
          yellow: 'hsl(var(--neon-yellow))',
        },
      },
      // 2077 的直角质感：圆角压到近 0，靠切角与硬边框表达层级。
      borderRadius: {
        none: '0',
        sm: '1px',
        DEFAULT: '2px',
        md: '2px',
        lg: '3px',
        xl: '4px',
        full: '9999px',
      },
      fontFamily: {
        // 展示体：拉丁/数字用 Chakra Petch，中文回落到系统黑体，
        // 这样中英混排时英文仍然保持 2077 的硬朗几何感。
        display: [
          'Chakra Petch',
          'PingFang SC',
          'Microsoft YaHei',
          'Noto Sans SC',
          'system-ui',
          'sans-serif',
        ],
        sans: [
          'Inter',
          'PingFang SC',
          'Microsoft YaHei',
          'Noto Sans SC',
          'system-ui',
          'sans-serif',
        ],
        mono: [
          'JetBrains Mono',
          'ui-monospace',
          'SFMono-Regular',
          'Menlo',
          'Consolas',
          'monospace',
        ],
      },
      boxShadow: {
        // 霓虹外发光分层：sm 用于常态，md 用于可交互元素悬停，lg 用于焦点态。
        'neon-sm': '0 0 0 1px hsl(var(--primary) / 0.22), 0 0 12px -4px hsl(var(--primary) / 0.45)',
        'neon-md':
          '0 0 0 1px hsl(var(--primary) / 0.4), 0 0 18px -4px hsl(var(--primary) / 0.6), 0 0 42px -12px hsl(var(--primary) / 0.5)',
        'neon-cyan':
          '0 0 0 1px hsl(var(--neon-cyan) / 0.35), 0 0 20px -6px hsl(var(--neon-cyan) / 0.55)',
        'neon-alert':
          '0 0 0 1px hsl(var(--destructive) / 0.4), 0 0 20px -6px hsl(var(--destructive) / 0.55)',
        'panel': '0 18px 40px -28px hsl(220 60% 2% / 0.9)',
      },
      keyframes: {
        'fade-in': { from: { opacity: '0' }, to: { opacity: '1' } },
        'slide-up': {
          from: { opacity: '0', transform: 'translateY(6px)' },
          to: { opacity: '1', transform: 'translateY(0)' },
        },
        'slide-in-left': {
          from: { opacity: '0', transform: 'translateX(-10px)' },
          to: { opacity: '1', transform: 'translateX(0)' },
        },
        shimmer: {
          '100%': { transform: 'translateX(100%)' },
        },
        // 顶部读条扫描：一条亮线沿顶栏横向扫过，8s 一轮，暗示数据链路在线。
        sweep: {
          '0%': { transform: 'translateX(-100%)' },
          '100%': { transform: 'translateX(400%)' },
        },
        // 呼吸式霓虹，用于在线状态与关键数值。
        'neon-breathe': {
          '0%, 100%': { opacity: '1' },
          '50%': { opacity: '0.55' },
        },
        // 警示条纹缓慢流动，暗示「此区域仍在处理」。
        'stripe-drift': {
          '0%': { backgroundPosition: '0 0' },
          '100%': { backgroundPosition: '28px 0' },
        },
        // 故障切片：文字在两条水平带内瞬移，配合 RGB 色散。
        'glitch-a': {
          '0%, 100%': { transform: 'translate(0)', clipPath: 'inset(0 0 0 0)' },
          '20%': { transform: 'translate(-2px)', clipPath: 'inset(12% 0 62% 0)' },
          '40%': { transform: 'translate(2px)', clipPath: 'inset(58% 0 18% 0)' },
          '60%': { transform: 'translate(-1px)', clipPath: 'inset(32% 0 44% 0)' },
        },
        'glitch-b': {
          '0%, 100%': { transform: 'translate(0)', clipPath: 'inset(0 0 0 0)' },
          '25%': { transform: 'translate(2px)', clipPath: 'inset(64% 0 12% 0)' },
          '45%': { transform: 'translate(-2px)', clipPath: 'inset(22% 0 58% 0)' },
          '70%': { transform: 'translate(1px)', clipPath: 'inset(46% 0 30% 0)' },
        },
        'flicker': {
          '0%, 100%': { opacity: '1' },
          '45%': { opacity: '1' },
          '46%': { opacity: '0.55' },
          '48%': { opacity: '1' },
          '72%': { opacity: '1' },
          '73%': { opacity: '0.7' },
          '75%': { opacity: '1' },
        },
        // 图表数值变化时的一次性脉冲，代替「数字突然跳变」的廉价感。
        'value-tick': {
          '0%': { opacity: '0.35' },
          '100%': { opacity: '1' },
        },
      },
      animation: {
        'fade-in': 'fade-in 200ms ease-out',
        'slide-up': 'slide-up 240ms cubic-bezier(0.16, 1, 0.3, 1)',
        'slide-in-left': 'slide-in-left 260ms cubic-bezier(0.16, 1, 0.3, 1)',
        shimmer: 'shimmer 1.6s infinite',
        sweep: 'sweep 8s cubic-bezier(0.65, 0, 0.35, 1) infinite',
        'neon-breathe': 'neon-breathe 2.8s ease-in-out infinite',
        'stripe-drift': 'stripe-drift 900ms linear infinite',
        glitch: 'glitch-a 3.6s steps(1, end) infinite',
        'glitch-alt': 'glitch-b 3.6s steps(1, end) infinite',
        flicker: 'flicker 6s linear infinite',
        'value-tick': 'value-tick 420ms ease-out',
      },
    },
  },
  plugins: [],
}

// 在样式生效前确定主题，避免刷新时闪白。
;(function () {
  var saved = localStorage.getItem('ovpanel.theme')
  var prefersDark = window.matchMedia('(prefers-color-scheme: dark)').matches
  var theme = saved === 'light' || saved === 'dark' ? saved : prefersDark ? 'dark' : 'light'
  document.documentElement.dataset.theme = theme
})()

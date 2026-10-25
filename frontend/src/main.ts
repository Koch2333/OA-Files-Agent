import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { useUi } from './stores/ui'
import { installAuth } from './auth-install'
import { installErrorReporting } from './errorReporting'
import { installBackendHealth } from './utils/backendHealth'
import { installDropGuard } from './utils/dropTarget'
import './styles/base.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
useUi().apply()
installAuth()
installErrorReporting(app)
installBackendHealth()
// dropping a file anywhere never navigates the window to it (Electron/Chromium would otherwise open the file in place of the app)
installDropGuard(window, (t) => useUi().toast(t, 'info'))
app.mount('#app')

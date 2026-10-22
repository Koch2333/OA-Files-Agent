import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { useUi } from './stores/ui'
import { installAuth } from './auth-install'
import { installErrorReporting } from './errorReporting'
import { installBackendHealth } from './utils/backendHealth'
import './styles/base.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
useUi().apply()
installAuth()
installErrorReporting(app)
installBackendHealth()
app.mount('#app')

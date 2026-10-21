import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import { router } from './router'
import { useUi } from './stores/ui'
import './styles/base.css'

const app = createApp(App)
app.use(createPinia())
app.use(router)
useUi().apply()
app.mount('#app')

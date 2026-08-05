import { mount } from 'svelte'
import './style.css'
import App from './App.svelte'

const target = document.getElementById('app')
if (!target) throw new Error('#app mount point missing from index.html')

export default mount(App, { target })

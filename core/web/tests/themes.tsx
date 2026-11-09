import { createRoot } from 'react-dom/client'
import { consumeAccessTokenFromFragment } from '../src/authClient'
import ThemeToggle from '../src/ThemeToggle'
import '../../../brand/tokens.css'
import '../src/styles.css'
consumeAccessTokenFromFragment()
createRoot(document.getElementById('root')!).render(<footer className="shell-footer"><ThemeToggle /></footer>)

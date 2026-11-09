import { ThemePicker } from '../../../../packages/settings/ThemePicker'
import { read, save } from '../accountTheme'
export default function ThemeToggle({ sync = true }: {sync?: boolean}) { return <ThemePicker read={read} save={save} sync={sync} /> }

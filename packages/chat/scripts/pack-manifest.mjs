// `npm pack` the built package into dist-packages/ and write the manifest the runner serves:
// {"packages":[{"name","version","file","sha512"}]}. The README goes beside the tarball so the
// runner can serve it as the package's docs_url.
import { createHash } from 'node:crypto'
import { execFileSync } from 'node:child_process'
import { copyFileSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
const out = join(root, 'dist-packages')
mkdirSync(out, { recursive: true })

const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'))
const result = JSON.parse(execFileSync('npm', ['pack', '--json', '--pack-destination', out], { cwd: root, encoding: 'utf8' }))
const file = result[0].filename
const sha512 = createHash('sha512').update(readFileSync(join(out, file))).digest('base64')
copyFileSync(join(root, 'README.md'), join(out, 'README.md'))
writeFileSync(join(out, 'manifest.json'), JSON.stringify({
  packages: [{ name: pkg.name, version: pkg.version, file, sha512: `sha512-${sha512}`, readme: 'README.md' }],
}, null, 2) + '\n')
console.log(`packed ${file} (${pkg.name}@${pkg.version}) into ${out}`)

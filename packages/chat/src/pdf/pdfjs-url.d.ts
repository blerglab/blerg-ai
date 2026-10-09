// Vite resolves `import x from 'file?url'` to the bundled asset's URL (the pdf.js worker here).
// vite/client declares the same module; this copy keeps the loader compiling where it is not loaded.
declare module '*?url' {
  const url: string
  export default url
}

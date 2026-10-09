// Everything pdf.js needs, in ONE lazily loaded module: the library and its worker. The worker's
// URL import lives here, not in the loader, because a library build inlines it as a data URL — a
// megabyte and a half that must stay out of the chat bundle and ride in this chunk instead. An
// app that builds the package from source (the runner) gets the worker as a file of its own.
import * as pdfjs from 'pdfjs-dist'
import workerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'

pdfjs.GlobalWorkerOptions.workerSrc = workerUrl

export default pdfjs

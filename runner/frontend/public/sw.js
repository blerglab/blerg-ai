self.addEventListener('push', function(event) {
  const data = event.data ? event.data.json() : { title: 'blerg-runner', body: 'Session update' }
  event.waitUntil(
    // Suppress OS notifications while a blerg-runner window is visible — the user is
    // already looking at the app (in-page toasts cover it). Without this, waking
    // a desktop delivers the whole pending burst as stacked notifications even
    // though the tab is right there.
    clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(wins) {
      if (wins.some(function(w) { return w.visibilityState === 'visible' })) return
      return self.registration.showNotification(data.title || 'blerg-runner', {
        body: data.body || '',
        icon: '/icon-192.png',
        badge: '/icon-192.png',
        // One notification per target: repeats for the same session replace the
        // existing notification instead of stacking.
        tag: data.url || '/',
        data: { url: data.url || '/' }
      })
    })
  )
})

self.addEventListener('notificationclick', function(event) {
  event.notification.close()
  event.waitUntil(
    clients.openWindow(event.notification.data.url || '/')
  )
})

self.addEventListener('pushsubscriptionchange', function(event) {
  // Authentication belongs to the page. Never persist a bearer token in a
  // service worker or re-subscribe a browser that may have turned push off.
  event.waitUntil(clients.matchAll({ type: 'window', includeUncontrolled: true }).then(function(wins) {
    wins.forEach(function(win) { win.postMessage({ type: 'push_subscription_changed' }) })
  }))
})

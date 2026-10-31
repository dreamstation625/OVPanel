let unauthorizedHandler: (() => void) | null = null

export function registerUnauthorizedHandler(handler: () => void) {
  unauthorizedHandler = handler
}

export function notifyUnauthorized() {
  unauthorizedHandler?.()
}

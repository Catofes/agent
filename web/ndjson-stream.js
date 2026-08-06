(function (root, factory) {
  const api = factory()
  if (typeof module === 'object' && module.exports) module.exports = api
  else root.NDJSONStream = api
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  function createParser(onEvent) {
    if (typeof onEvent !== 'function') throw new TypeError('onEvent must be a function')
    const decoder = new TextDecoder()
    let buffer = ''

    function parseReadyLines(final) {
      const lines = buffer.split('\n')
      buffer = final ? '' : lines.pop()
      if (final && lines.length && lines[lines.length - 1] === '') lines.pop()
      for (const line of lines) {
        const trimmed = line.trim()
        if (trimmed) onEvent(JSON.parse(trimmed))
      }
    }

    return {
      push(chunk, done = false) {
        buffer += decoder.decode(chunk || new Uint8Array(), { stream: !done })
        parseReadyLines(done)
      }
    }
  }

  return { createParser }
})

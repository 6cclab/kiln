// The globals every hooks module sees, run once per engine before any mod
// loads. goja has no async generators, so nothing here uses one; the Go side
// supplies __native (base64, UTF-8, logging, clock).
//
// Mods see: h and Fragment (JSX; bundles compile against __h and __frag),
// btoa/atob, TextEncoder/TextDecoder, Uint8Array.prototype.toBase64 and
// Uint8Array.fromBase64, structuredClone, console, performance.now and
// crypto.randomUUID. Everything prefixed __ is the engine's own.
(function (g, native) {
  'use strict'

  var ELEMENT = Symbol('kiln.element')
  var TAG = Symbol('kiln.tag')

  // --- elements -------------------------------------------------------------

  function flatten(list, out) {
    for (var i = 0; i < list.length; i++) {
      var c = list[i]
      if (c === null || c === undefined || c === false || c === true || c === '') continue
      if (Array.isArray(c)) flatten(c, out)
      else out.push(c)
    }
    return out
  }

  function makeElement(type, props, children) {
    var p = {}
    var key
    if (props) {
      var keys = Object.keys(props)
      for (var i = 0; i < keys.length; i++) {
        var k = keys[i]
        if (k === 'key') key = props[k]
        else if (k !== 'children') p[k] = props[k]
      }
    }
    var el = { type: type, props: p, children: flatten(children, []) }
    if (key !== undefined && key !== null) el.key = String(key)
    Object.defineProperty(el, ELEMENT, { value: true })
    return el
  }

  function constructor(type) {
    var f = function (props) {
      var children = props && props.children !== undefined ? [props.children] : []
      return makeElement(type, props, children)
    }
    f[TAG] = type
    Object.defineProperty(f, 'name', { value: type })
    return Object.freeze(f)
  }

  function h(tag, props) {
    var children = Array.prototype.slice.call(arguments, 2)
    if (typeof tag === 'function') {
      if (tag[TAG]) return makeElement(tag[TAG], props, children)
      var p = Object.assign({}, props)
      if (children.length > 0) p.children = children.length === 1 ? children[0] : children
      return tag(p)
    }
    return makeElement(String(tag), props, children)
  }

  // <>...</>: a column Box around the children.
  function Fragment(props) {
    var children = props && props.children !== undefined ? [props.children] : []
    return makeElement('Box', { flexDirection: 'column' }, children)
  }

  // The terminal's element table, what $.ui.resolve(e) returns there.
  var terminalTable = Object.freeze({
    Box: constructor('Box'),
    Text: constructor('Text'),
    Button: constructor('Button'),
    Raster: constructor('Raster'),
    Image: constructor('Image'),
    Link: constructor('Link'),
    Markdown: constructor('Markdown'),
  })

  // --- web APIs the worker has ----------------------------------------------

  function TextEncoder() {}
  TextEncoder.prototype.encoding = 'utf-8'
  TextEncoder.prototype.encode = function (input) {
    return new Uint8Array(native.utf8Encode(input === undefined ? '' : String(input)))
  }

  function TextDecoder(label) {
    if (label !== undefined && !/^utf-?8$/i.test(String(label))) {
      throw new RangeError('TextDecoder: only utf-8 is supported')
    }
  }
  TextDecoder.prototype.encoding = 'utf-8'
  TextDecoder.prototype.decode = function (input) {
    if (input === undefined) return ''
    if (input instanceof ArrayBuffer) return native.utf8Decode(input, 0, input.byteLength)
    if (ArrayBuffer.isView(input)) return native.utf8Decode(input.buffer, input.byteOffset, input.byteLength)
    throw new TypeError('TextDecoder.decode takes an ArrayBuffer or a view of one')
  }

  Object.defineProperty(Uint8Array.prototype, 'toBase64', {
    value: function () {
      return native.base64Encode(this.buffer, this.byteOffset, this.byteLength)
    },
    writable: true,
    configurable: true,
  })
  Object.defineProperty(Uint8Array, 'fromBase64', {
    value: function (s) {
      return new Uint8Array(native.base64Decode(String(s)))
    },
    writable: true,
    configurable: true,
  })

  function structuredClone(value) {
    if (value === undefined) return undefined
    return JSON.parse(JSON.stringify(value))
  }

  var console = {
    log: function () { native.log('info', Array.prototype.slice.call(arguments).map(String).join(' ')) },
    info: function () { native.log('info', Array.prototype.slice.call(arguments).map(String).join(' ')) },
    debug: function () { native.log('debug', Array.prototype.slice.call(arguments).map(String).join(' ')) },
    warn: function () { native.log('warn', Array.prototype.slice.call(arguments).map(String).join(' ')) },
    error: function () { native.log('error', Array.prototype.slice.call(arguments).map(String).join(' ')) },
  }

  // --- the claude-code module -------------------------------------------------

  function clone(v) {
    return v === undefined ? undefined : JSON.parse(JSON.stringify(v))
  }

  function atom(ref, initial) {
    if (!ref || typeof ref.plugin !== 'string' || typeof ref.key !== 'string') {
      throw new TypeError('atom takes { plugin, key } as string literals')
    }
    return Object.freeze({ plugin: ref.plugin, key: ref.key, initial: clone(initial) })
  }

  function read($, a) {
    return $.state.get({ plugin: a.plugin, key: a.key }).then(function (v) {
      return v === undefined ? clone(a.initial) : v
    })
  }

  function update($, a, fn) {
    return read($, a).then(function (prev) {
      var value = fn(prev)
      return $.state.set({ plugin: a.plugin, key: a.key, value: value }).then(function () {
        return value
      })
    })
  }

  function notInKiln(name) {
    return function () {
      throw new Error(name + ' from claude-code is not available in kiln (mods v1)')
    }
  }

  var claudeCode = Object.freeze({
    atom: atom,
    read: read,
    update: update,
    derive: notInKiln('derive'),
    memberOf: notInKiln('memberOf'),
  })

  // --- the engine's helpers -------------------------------------------------

  function deepFreeze(o) {
    if (o === null || typeof o !== 'object' || Object.isFrozen(o)) return o
    Object.freeze(o)
    var keys = Object.keys(o)
    for (var i = 0; i < keys.length; i++) deepFreeze(o[keys[i]])
    return o
  }

  function then(p, ok, fail) {
    Promise.resolve(p).then(ok, fail)
  }

  function isAsyncIterator(v) {
    return v !== null && typeof v === 'object' && typeof v.next === 'function' &&
      typeof v[Symbol.asyncIterator] === 'function'
  }

  // Runs a streaming hook's generator to its end; chunks it yields go nowhere
  // (kiln streams none through mods), its return value is the step's result.
  function drain(gen) {
    return new Promise(function (resolve, reject) {
      function step(r) {
        if (r.done) return resolve(r.value)
        gen.next().then(step, reject)
      }
      gen.next().then(step, reject)
    })
  }

  // What next(e) returns on a streaming event: an async iterator that yields
  // nothing and returns what the rest of the chain resolves to.
  function streamOf(p) {
    var done = false
    var it = {
      next: function () {
        if (done) return Promise.resolve({ done: true, value: undefined })
        done = true
        return Promise.resolve(p).then(function (v) { return { done: true, value: v } })
      },
      return: function (v) {
        done = true
        return Promise.resolve({ done: true, value: v })
      },
      throw: function (err) {
        done = true
        return Promise.reject(err)
      },
    }
    it[Symbol.asyncIterator] = function () { return it }
    return it
  }

  function rejected(message) {
    return Promise.reject(new Error(message))
  }

  // Builds one hook dispatch's $: every documented method, each routed to the
  // engine's call(frame, name, args...). The engine answers what kiln
  // implements and rejects the rest with a message naming the method.
  function makeDollar(call, frame, names) {
    var $ = {}
    for (var i = 0; i < names.length; i++) {
      var parts = names[i].split('.')
      var ns = $[parts[0]] || ($[parts[0]] = {})
      ns[parts[1]] = bind(call, frame, names[i])
    }
    return $
  }
  function bind(call, frame, name) {
    return function () {
      var args = [frame, name]
      for (var i = 0; i < arguments.length; i++) args.push(arguments[i])
      return call.apply(undefined, args)
    }
  }

  function assign(a, b) {
    return Object.assign({}, a, b)
  }

  g.h = h
  g.Fragment = Fragment
  g.__h = h
  g.__frag = Fragment
  g.TextEncoder = TextEncoder
  g.TextDecoder = TextDecoder
  g.btoa = native.btoa
  g.atob = native.atob
  g.structuredClone = structuredClone
  g.console = console
  g.performance = { now: native.monotonicMs }
  g.crypto = { randomUUID: native.randomUUID }

  return {
    ELEMENT: ELEMENT,
    terminalTable: terminalTable,
    claudeCode: claudeCode,
    deepFreeze: deepFreeze,
    then: then,
    isAsyncIterator: isAsyncIterator,
    drain: drain,
    streamOf: streamOf,
    rejected: rejected,
    makeDollar: makeDollar,
    assign: assign,
  }
})

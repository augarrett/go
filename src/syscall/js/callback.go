// Copyright 2018 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// +build js,wasm

package js

import "sync"

var pendingCallbacks = Global.Get("Array").New()

var makeCallbackHelper = Global.Call("eval", `
	(function(id, pendingCallbacks, resolveCallbackPromise) {
		return function() {
			pendingCallbacks.push({ id: id, args: arguments });
			resolveCallbackPromise();
		};
	})
`)

var makeEventCallbackHelper = Global.Call("eval", `
	(function(preventDefault, stopPropagation, stopImmediatePropagation, fn) {
		return function(event) {
			if (preventDefault) {
				event.preventDefault();
			}
			if (stopPropagation) {
				event.stopPropagation();
			}
			if (stopImmediatePropagation) {
				event.stopImmediatePropagation();
			}
			fn(event);
		};
	})
`)

var (
	callbacksMu    sync.Mutex
	callbacks             = make(map[uint32]func([]Value))
	nextCallbackID uint32 = 1
)

// Callback is a Go function that got wrapped for use as a JavaScript callback.
// A Callback can be passed to functions of this package that accept interface{},
// for example Value.Set and Value.Call.
type Callback struct {
	id        uint32
	enqueueFn Value // the JavaScript function that queues the callback for execution
}

// NewCallback returns a wrapped callback function. It can be passed to functions of this package
// that accept interface{}, for example Value.Set and Value.Call.
//
// Invoking the callback in JavaScript will queue the Go function fn for execution.
// This execution happens asynchronously on a special goroutine that handles all callbacks and preserves
// the order in which the callbacks got called.
// As a consequence, if one callback blocks this goroutine, other callbacks will not be processed.
// A blocking callback should therefore explicitly start a new goroutine.
//
// Callback.Close must be called to free up resources when the callback will not be used any more.
func NewCallback(fn func(args []Value)) Callback {
	callbackLoopOnce.Do(func() {
		go callbackLoop()
	})

	callbacksMu.Lock()
	id := nextCallbackID
	nextCallbackID++
	callbacks[id] = fn
	callbacksMu.Unlock()
	return Callback{
		id:        id,
		enqueueFn: makeCallbackHelper.Invoke(id, pendingCallbacks, resolveCallbackPromise),
	}
}

type EventCallbackFlag int

const (
	// PreventDefault can be used with NewEventCallback to call event.preventDefault synchronously.
	PreventDefault EventCallbackFlag = 1 << iota
	// StopPropagation can be used with NewEventCallback to call event.stopPropagation synchronously.
	StopPropagation
	// StopImmediatePropagation can be used with NewEventCallback to call event.stopImmediatePropagation synchronously.
	StopImmediatePropagation
)

// NewEventCallback returns a wrapped callback function, just like NewCallback, but the callback expects to have
// exactly one argument, the event. Depending on flags, it will synchronously call event.preventDefault,
// event.stopPropagation and/or event.stopImmediatePropagation before queuing the Go function fn for execution.
func NewEventCallback(flags EventCallbackFlag, fn func(event Value)) Callback {
	c := NewCallback(func(args []Value) {
		fn(args[0])
	})
	return Callback{
		id: c.id,
		enqueueFn: makeEventCallbackHelper.Invoke(
			flags&PreventDefault != 0,
			flags&StopPropagation != 0,
			flags&StopImmediatePropagation != 0,
			c,
		),
	}
}

func (c Callback) Close() {
	callbacksMu.Lock()
	delete(callbacks, c.id)
	callbacksMu.Unlock()
}

var callbackLoopOnce sync.Once

func callbackLoop() {
	for {
		sleepUntilCallback()
		for {
			cb := pendingCallbacks.Call("shift")
			if cb == Undefined {
				break
			}

			id := uint32(cb.Get("id").Int())
			callbacksMu.Lock()
			f, ok := callbacks[id]
			callbacksMu.Unlock()
			if !ok {
				Global.Get("console").Call("error", "call to closed callback")
				continue
			}

			argsObj := cb.Get("args")
			args := make([]Value, argsObj.Length())
			for i := range args {
				args[i] = argsObj.Index(i)
			}
			f(args)
		}
	}
}

// sleepUntilCallback is defined in the runtime package
func sleepUntilCallback()

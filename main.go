package main

// The host calls this plugin only through the C entry points in entry.c, which
// call the Go exports below.

/*
#include "abi.h"
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
)

func main() {}

//export cliproxyPluginInit
func cliproxyPluginInit(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	C.entry_store_host(host)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.entry_plugin_call)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.entry_plugin_free)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.entry_plugin_shutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, err := handleMethod(C.GoString(method), requestBytes)
	if err != nil {
		writeResponse(response, errorEnvelope("plugin_error", err.Error()))
		return 1
	}
	writeResponse(response, raw)
	return 0
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	keeper.shutdown()
	hostClosed.Store(true)
}

// hostError is a failed host callback. Status carries the upstream HTTP status when the host reports one.
type hostError struct {
	Status  int
	Message string
}

func (e *hostError) Error() string {
	if e.Status > 0 {
		return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message)
	}
	return e.Message
}

// hostClosed blocks host calls after shutdown, when the host frees its callback table.
var hostClosed atomic.Bool

func callHost(method string, payload any) (json.RawMessage, error) {
	if hostClosed.Load() {
		return nil, fmt.Errorf("%s: host has shut the plugin down", method)
	}
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode %s request: %w", method, err)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))

	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		cPayload := C.CBytes(rawPayload)
		if cPayload == nil {
			return nil, fmt.Errorf("allocate %s request", method)
		}
		defer C.free(cPayload)
		requestPtr = (*C.uint8_t)(cPayload)
	}

	var response C.cliproxy_buffer
	callCode := C.entry_call_host(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.entry_free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("%s returned no response (code %d)", method, int(callCode))
	}

	var env pluginabi.Envelope
	if err := json.Unmarshal(rawResponse, &env); err != nil {
		return nil, fmt.Errorf("decode %s response: %w", method, err)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, &hostError{Status: env.Error.HTTPStatus, Message: env.Error.Message}
		}
		return nil, fmt.Errorf("%s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("%s returned code %d", method, int(callCode))
	}
	return env.Result, nil
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func okEnvelope(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(pluginabi.Envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, _ := pluginabi.NewErrorEnvelope(code, message)
	return raw
}
